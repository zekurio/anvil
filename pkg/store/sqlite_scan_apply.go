package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/zekurio/anvil/pkg/domain"
)

type scanSourceGroup struct {
	kind        domain.SourceKind
	path        string
	fingerprint domain.FileFingerprint
	entries     map[string]ScanEntry
	persist     bool
}

// rowQuerier is satisfied by both *sql.DB and *sql.Tx.
type rowQuerier interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

func (s *SQLiteStore) BeginLibraryScan(ctx context.Context, libraryName domain.LibraryName) (ScanToken, error) {
	if strings.TrimSpace(string(libraryName)) == "" {
		return ScanToken{}, errors.New("scan library name is required")
	}
	token, err := issueScanToken(ctx, s.db, libraryName)
	if err != nil {
		return ScanToken{}, fmt.Errorf("begin library scan: %w", err)
	}
	return ScanToken{LibraryName: libraryName, Sequence: token}, nil
}

// issueScanToken hands out the next scan sequence for a library. A scan only
// applies to sources no newer token has already applied to.
func issueScanToken(ctx context.Context, q rowQuerier, libraryName domain.LibraryName) (int64, error) {
	var token int64
	err := q.QueryRowContext(ctx, `
INSERT INTO library_scans (library_name, next_token)
VALUES (?, 1)
ON CONFLICT(library_name) DO UPDATE SET next_token = next_token + 1
RETURNING next_token
`, string(libraryName)).Scan(&token)
	return token, err
}

func recordSourceScanTx(ctx context.Context, tx *sql.Tx, libraryName domain.LibraryName, relativePath string, token int64) error {
	_, err := tx.ExecContext(ctx, `
INSERT INTO source_scans VALUES (?, ?, ?)
ON CONFLICT(library_name, relative_path) DO UPDATE SET applied_token = excluded.applied_token
`, string(libraryName), relativePath, token)
	return err
}

func (s *SQLiteStore) ApplyLibraryScan(ctx context.Context, token ScanToken, input ApplyScanInput) (ApplyScanResult, error) {
	if token.Sequence <= 0 {
		return ApplyScanResult{}, errors.New("scan token is required")
	}
	if strings.TrimSpace(string(input.LibraryName)) == "" {
		return ApplyScanResult{}, errors.New("scan library name is required")
	}
	if token.LibraryName != input.LibraryName {
		return ApplyScanResult{}, fmt.Errorf("scan token belongs to library %q, not %q", token.LibraryName, input.LibraryName)
	}
	groups, err := groupScanEntries(input.Entries)
	if err != nil {
		return ApplyScanResult{}, err
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ApplyScanResult{}, fmt.Errorf("begin scan application: %w", err)
	}
	defer rollback(tx)

	superseded, err := scanSupersededTx(ctx, tx, token)
	if err != nil {
		return ApplyScanResult{}, err
	}
	if superseded {
		if err := tx.Commit(); err != nil {
			return ApplyScanResult{}, fmt.Errorf("commit superseded scan: %w", err)
		}
		return ApplyScanResult{}, nil
	}

	snapshot, err := readScanSnapshot(ctx, tx, input)
	if err != nil {
		return ApplyScanResult{}, err
	}
	app := &scanApplication{
		tx:       tx,
		input:    input,
		token:    token,
		now:      defaultNow(input.CompletedAt),
		snapshot: snapshot,
		result:   ApplyScanResult{Applied: true},
	}
	if err := app.apply(ctx, groups); err != nil {
		return ApplyScanResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return ApplyScanResult{}, fmt.Errorf("commit library scan: %w", err)
	}
	if app.result.EnqueuedJobs > 0 {
		s.notifyWork()
	}
	return app.result, nil
}

// scanSupersededTx reports whether a newer scan of the library already
// applied, in which case this one is dropped without error.
func scanSupersededTx(ctx context.Context, tx *sql.Tx, token ScanToken) (bool, error) {
	var issued, applied int64
	err := tx.QueryRowContext(ctx, `
SELECT next_token, applied_token FROM library_scans WHERE library_name = ?
`, string(token.LibraryName)).Scan(&issued, &applied)
	if errors.Is(err, sql.ErrNoRows) {
		return false, fmt.Errorf("apply library scan: %w", ErrNotFound)
	}
	if err != nil {
		return false, fmt.Errorf("read library scan token: %w", err)
	}
	if token.Sequence > issued {
		return false, fmt.Errorf("scan token %d was not issued for library %q", token.Sequence, token.LibraryName)
	}
	return token.Sequence <= applied, nil
}

// scanApplication reconciles one scan's observations with the current
// occurrences inside a single transaction.
type scanApplication struct {
	tx       *sql.Tx
	input    ApplyScanInput
	token    ScanToken
	now      time.Time
	snapshot scanSnapshot
	result   ApplyScanResult

	touchedSources []int64
	touchedAssets  []int64
}

// eligible reports whether this scan may change a source path. A scoped scan
// with a newer token already owns it.
func (a *scanApplication) eligible(path string) bool {
	return a.snapshot.tokens[path] < a.token.Sequence
}

func (a *scanApplication) apply(ctx context.Context, groups map[string]*scanSourceGroup) error {
	for path := range groups {
		if a.input.SourcePaths != nil && !a.snapshot.scope[path] {
			return fmt.Errorf("scan entry %q is outside source paths", path)
		}
		if !a.eligible(path) {
			delete(groups, path)
		}
	}
	for _, path := range slices.Sorted(maps.Keys(groups)) {
		if err := a.applySource(ctx, groups[path]); err != nil {
			return err
		}
	}
	for path, source := range a.snapshot.sources {
		if _, seen := groups[path]; !seen && a.eligible(path) {
			if err := retireSourceTx(ctx, a.tx, source.ID, a.now); err != nil {
				return err
			}
		}
	}
	if err := touchScanRows(ctx, a.tx, "media_sources", a.touchedSources, a.now); err != nil {
		return err
	}
	if err := touchScanRows(ctx, a.tx, "media_assets", a.touchedAssets, a.now); err != nil {
		return err
	}
	return a.recordTokens(ctx)
}

func (a *scanApplication) applySource(ctx context.Context, group *scanSourceGroup) error {
	if !group.persist {
		return nil
	}
	a.result.Sources++
	source, found := a.snapshot.sources[group.path]
	known := found
	if !known {
		var err error
		known, err = sourcePathKnownTx(ctx, a.tx, a.input.LibraryName, group.path)
		if err != nil {
			return err
		}
	}
	// A path the library has seen before is recorded as processed instead of
	// queued, unless the library requeues existing paths (download libraries).
	skipKnown := known && !a.input.RequeueExisting

	replaced := found && (source.Kind != group.kind || source.Kind == domain.SourceKindFile && !fingerprintsEqual(source.Fingerprint, group.fingerprint))
	if replaced {
		if err := retireSourceTx(ctx, a.tx, source.ID, a.now); err != nil {
			return err
		}
		found = false
	}
	switch {
	case !found:
		status := domain.MediaSourceActive
		if skipKnown {
			status = domain.MediaSourceProcessed
		}
		var err error
		source, err = insertSourceOccurrenceTx(ctx, a.tx, domain.MediaSource{
			LibraryName:  a.input.LibraryName,
			Kind:         group.kind,
			RelativePath: group.path,
			Status:       status,
			Fingerprint:  group.fingerprint,
			FirstSeenAt:  a.now,
			LastSeenAt:   a.now,
		})
		if err != nil {
			return err
		}
	case !fingerprintsEqual(source.Fingerprint, group.fingerprint):
		if _, err := a.tx.ExecContext(ctx, `
UPDATE media_sources
SET kind = ?, size_bytes = ?, mod_time = ?, hash_algorithm = ?, hash_value = ?,
	last_seen_at = ?, updated_at = ?
WHERE id = ?
`, string(group.kind), group.fingerprint.SizeBytes, encodeTimePtr(group.fingerprint.ModTime),
			group.fingerprint.HashAlgorithm, group.fingerprint.HashValue, encodeTime(a.now), encodeTime(a.now), int64(source.ID)); err != nil {
			return fmt.Errorf("refresh source occurrence: %w", err)
		}
	}
	a.touchedSources = append(a.touchedSources, int64(source.ID))

	assets := a.snapshot.assets[source.ID]
	activeAssets := 0
	for _, path := range slices.Sorted(maps.Keys(group.entries)) {
		entry := group.entries[path]
		if !entry.Persist {
			continue
		}
		active, err := a.applyAsset(ctx, source.ID, assets, path, entry, skipKnown)
		if err != nil {
			return err
		}
		if active {
			activeAssets++
		}
	}
	for path, asset := range assets {
		entry, seen := group.entries[path]
		switch {
		case !seen:
			if err := retireAssetTx(ctx, a.tx, asset.ID, a.now); err != nil {
				return err
			}
		case !entry.Persist && asset.Status == domain.MediaAssetActive:
			// Observed but not persisted this scan: it keeps its previous status.
			activeAssets++
		}
	}

	status := domain.MediaSourceProcessed
	if activeAssets > 0 {
		status = domain.MediaSourceActive
	}
	if source.Status != status {
		if _, err := a.tx.ExecContext(ctx, `UPDATE media_sources SET status = ?, updated_at = ? WHERE id = ?`, string(status), encodeTime(a.now), int64(source.ID)); err != nil {
			return err
		}
	}
	return nil
}

// applyAsset records one persisted asset observation and reports whether the
// asset is still active afterwards.
func (a *scanApplication) applyAsset(ctx context.Context, sourceID domain.MediaSourceID, assets map[string]domain.MediaAsset, path string, entry ScanEntry, skipKnown bool) (bool, error) {
	a.result.Assets++
	role := defaultAssetRole(entry.AssetRole)
	asset, found := assets[path]
	if found && !fingerprintsEqual(asset.Fingerprint, entry.AssetFingerprint) {
		if err := retireAssetTx(ctx, a.tx, asset.ID, a.now); err != nil {
			return false, err
		}
		found = false
	}
	switch {
	case !found:
		status := domain.MediaAssetActive
		if skipKnown {
			status = domain.MediaAssetProcessed
		}
		var err error
		asset, err = insertAssetOccurrenceTx(ctx, a.tx, domain.MediaAsset{
			SourceID:     sourceID,
			RelativePath: path,
			Role:         role,
			Status:       status,
			Fingerprint:  entry.AssetFingerprint,
			FirstSeenAt:  a.now,
			LastSeenAt:   a.now,
		})
		if err != nil {
			return false, err
		}
	case asset.Role != role:
		if _, err := a.tx.ExecContext(ctx, `
UPDATE media_assets
SET role = ?, last_seen_at = ?, updated_at = ?
WHERE id = ?
`, string(role), encodeTime(a.now), encodeTime(a.now), int64(asset.ID)); err != nil {
			return false, fmt.Errorf("refresh asset occurrence: %w", err)
		}
	}
	a.touchedAssets = append(a.touchedAssets, int64(asset.ID))

	active := asset.Status == domain.MediaAssetActive
	if entry.Enqueue && active && !skipKnown {
		if err := a.enqueue(ctx, sourceID, asset.ID); err != nil {
			return false, err
		}
	}
	return active, nil
}

func (a *scanApplication) enqueue(ctx context.Context, sourceID domain.MediaSourceID, assetID domain.MediaAssetID) error {
	if a.snapshot.jobs[assetID] {
		a.result.ExistingJobs++
		return nil
	}
	_, inserted, err := enqueueJobTx(ctx, a.tx, EnqueueJobInput{
		SourceID: sourceID, AssetID: assetID, LibraryName: a.input.LibraryName,
		Priority: a.input.Priority, Now: a.now,
	})
	if err != nil {
		return err
	}
	if inserted {
		a.result.EnqueuedJobs++
	} else {
		a.result.ExistingJobs++
	}
	return nil
}

// recordTokens marks what this scan applied to. A scoped scan stamps each
// source it owned; a full scan advances the library and drops the per-source
// stamps it supersedes.
func (a *scanApplication) recordTokens(ctx context.Context) error {
	library := a.input.LibraryName
	if a.input.SourcePaths != nil {
		for path := range a.snapshot.scope {
			if !a.eligible(path) {
				continue
			}
			if err := recordSourceScanTx(ctx, a.tx, library, path, a.token.Sequence); err != nil {
				return err
			}
		}
		return nil
	}
	if _, err := a.tx.ExecContext(ctx, `DELETE FROM source_scans WHERE library_name = ? AND applied_token <= ?`, string(library), a.token.Sequence); err != nil {
		return err
	}
	if _, err := a.tx.ExecContext(ctx, `
UPDATE library_scans SET applied_token = ?, applied_at = ? WHERE library_name = ?
`, a.token.Sequence, encodeTime(a.now), string(library)); err != nil {
		return fmt.Errorf("record applied library scan: %w", err)
	}
	return nil
}

func groupScanEntries(entries []ScanEntry) (map[string]*scanSourceGroup, error) {
	groups := make(map[string]*scanSourceGroup)
	for _, entry := range entries {
		if strings.TrimSpace(entry.SourceRelativePath) == "" || strings.TrimSpace(entry.AssetRelativePath) == "" {
			return nil, errors.New("scan entry requires source and asset paths")
		}
		if entry.SourceKind != domain.SourceKindFile && entry.SourceKind != domain.SourceKindPackage {
			return nil, fmt.Errorf("scan entry has invalid source kind %q", entry.SourceKind)
		}
		group, ok := groups[entry.SourceRelativePath]
		if !ok {
			group = &scanSourceGroup{kind: entry.SourceKind, path: entry.SourceRelativePath, entries: make(map[string]ScanEntry)}
			groups[entry.SourceRelativePath] = group
		}
		if group.kind != entry.SourceKind {
			return nil, fmt.Errorf("scan source %q has conflicting kinds", entry.SourceRelativePath)
		}
		if _, duplicate := group.entries[entry.AssetRelativePath]; duplicate {
			return nil, fmt.Errorf("scan source %q has duplicate asset %q", entry.SourceRelativePath, entry.AssetRelativePath)
		}
		group.entries[entry.AssetRelativePath] = entry
		if entry.Persist {
			group.persist = true
			group.fingerprint = entry.SourceFingerprint
		}
	}
	return groups, nil
}
