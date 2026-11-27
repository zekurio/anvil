package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"time"

	"github.com/zekurio/anvil/pkg/config"
	"github.com/zekurio/anvil/pkg/flow"
)

// LegacyImport reports what ImportLegacyConfig did.
type LegacyImport struct {
	// Imported is false when an earlier start already looked at anvil.toml;
	// nothing was read or written then.
	Imported  bool
	Flows     []LegacyImportedFlow
	Arrs      []string
	Libraries []LegacyImportedLibrary
	// Skipped says, per object, why something in the file was left out.
	Skipped []string
}

// LegacyImportedFlow is one profile that became, or mapped onto, a flow.
type LegacyImportedFlow struct {
	Profile string
	FlowID  int64
	Flow    string
	// Created is false when the profile mapped onto a flow that already
	// existed, as the unmodified default profile does.
	Created bool
	// Issues lists what flow validation objects to. The flow is stored all
	// the same, so the operator can open and fix it; until then its jobs fail
	// with the first issue as the reason.
	Issues []string
}

// LegacyImportedLibrary is one library that moved into the store.
type LegacyImportedLibrary struct {
	Name string
	Flow string
}

// ImportLegacyConfig moves the libraries, Arr connections, and referenced
// profiles of anvil.toml into the store, once. Every profile a library uses
// becomes a flow that resolves to the same profile; the unmodified built-in
// profile maps onto the seeded default flow. Library names stay as they are,
// so existing sources and jobs keep belonging to them, and imported libraries
// are not paused: they were already running.
//
// The whole import is one transaction, and it records that it ran even when
// the file had nothing to import. A second call therefore does nothing, and a
// TOML file edited later is never imported silently.
func (s *SQLiteStore) ImportLegacyConfig(ctx context.Context, legacy config.LegacyConfig, now time.Time) (LegacyImport, error) {
	now = defaultNow(now)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return LegacyImport{}, fmt.Errorf("begin legacy config import: %w", err)
	}
	defer rollback(tx)
	if _, done, err := getMeta(ctx, tx, metaLegacyConfigImported); err != nil || done {
		return LegacyImport{}, err
	}

	result := LegacyImport{Imported: true}
	for _, name := range sortedNames(legacy.Arrs) {
		arr := legacy.Arrs[name]
		arr.Name = name
		_, err := createArrTx(ctx, tx, arr, now)
		switch {
		case errors.Is(err, ErrNameTaken):
			result.Skipped = append(result.Skipped, fmt.Sprintf("arr %q: an Arr connection of that name already exists", name))
		case err != nil:
			return LegacyImport{}, fmt.Errorf("import arr %q: %w", name, err)
		default:
			result.Arrs = append(result.Arrs, name)
		}
	}

	flows := make(map[string]LegacyImportedFlow)
	for _, name := range sortedNames(legacy.Libraries) {
		source := legacy.Libraries[name]
		source.Name = name
		imported, known := flows[source.Profile]
		if !known {
			profile, ok := legacy.Profiles[source.Profile]
			if !ok {
				return LegacyImport{}, fmt.Errorf("import library %q: profile %q is not defined", name, source.Profile)
			}
			profile.Name = source.Profile
			imported, err = importProfileTx(ctx, tx, profile, now)
			if err != nil {
				return LegacyImport{}, fmt.Errorf("import profile %q: %w", source.Profile, err)
			}
			flows[source.Profile] = imported
			result.Flows = append(result.Flows, imported)
		}

		library := source.Runtime()
		library.FlowID = imported.FlowID
		// The TOML allowed relative paths, which the scanner resolved against
		// the daemon's working directory. The store keeps them resolved.
		if library.Path, err = filepath.Abs(library.Path); err != nil {
			return LegacyImport{}, fmt.Errorf("import library %q: resolve path: %w", name, err)
		}
		if library.IsDownload() && library.Download.HandoffPath != "" {
			if library.Download.HandoffPath, err = filepath.Abs(library.Download.HandoffPath); err != nil {
				return LegacyImport{}, fmt.Errorf("import library %q: resolve handoff_path: %w", name, err)
			}
		}
		_, err := createLibraryTx(ctx, tx, library, now)
		switch {
		case errors.Is(err, ErrNameTaken):
			result.Skipped = append(result.Skipped, fmt.Sprintf("library %q: a library of that name already exists", name))
		case err != nil:
			return LegacyImport{}, fmt.Errorf("import library %q: %w", name, err)
		default:
			result.Libraries = append(result.Libraries, LegacyImportedLibrary{Name: name, Flow: imported.Flow})
		}
	}

	if err := setMeta(ctx, tx, metaLegacyConfigImported, encodeTime(now)); err != nil {
		return LegacyImport{}, err
	}
	if err := tx.Commit(); err != nil {
		return LegacyImport{}, fmt.Errorf("commit legacy config import: %w", err)
	}
	return result, nil
}

// LegacyConfigImported reports whether the one-time import has run.
func (s *SQLiteStore) LegacyConfigImported(ctx context.Context) (bool, error) {
	_, done, err := getMeta(ctx, s.db, metaLegacyConfigImported)
	return done, err
}

// importProfileTx turns one TOML profile into a flow, or finds the flow that
// already is that profile.
func importProfileTx(ctx context.Context, tx *sql.Tx, profile config.ProfileConfig, now time.Time) (LegacyImportedFlow, error) {
	graph := flow.FromProfile(profile.ToDomain())
	imported := LegacyImportedFlow{Profile: profile.Name}
	for _, issue := range flow.Validate(graph) {
		if issue.Severity == flow.SeverityError {
			imported.Issues = append(imported.Issues, issue.Message)
		}
	}

	name := profile.Name
	if profile.Name == config.DefaultProfileName && sameFlow(graph, flow.Default()) {
		// The built-in profile at its defaults is the seeded flow.
		name = DefaultFlowName
		if existing, found, err := findFlowByNameTx(ctx, tx, name); err != nil {
			return LegacyImportedFlow{}, err
		} else if found && sameFlow(existing.Graph, graph) {
			imported.FlowID, imported.Flow = existing.ID, existing.Name
			return imported, nil
		}
	}
	// A name that is taken belongs to a flow the operator made; the import
	// never overwrites one.
	for attempt, base := 1, name; ; attempt++ {
		_, taken, err := findFlowByNameTx(ctx, tx, name)
		if err != nil {
			return LegacyImportedFlow{}, err
		}
		if !taken {
			break
		}
		name = fmt.Sprintf("%s (imported)", base)
		if attempt > 1 {
			name = fmt.Sprintf("%s (imported %d)", base, attempt)
		}
	}
	created, err := createFlowTx(ctx, tx, FlowInput{
		Name:        name,
		Description: fmt.Sprintf("Imported from profile %q of anvil.toml.", profile.Name),
		Graph:       graph,
	}, now)
	if err != nil {
		return LegacyImportedFlow{}, err
	}
	imported.FlowID, imported.Flow, imported.Created = created.ID, created.Name, true
	return imported, nil
}

// sameFlow reports whether two graphs mean the same. A graph with error
// issues has no canonical form and equals nothing.
func sameFlow(left, right flow.Graph) bool {
	leftCanonical, err := flow.Canonical(left)
	if err != nil {
		return false
	}
	rightCanonical, err := flow.Canonical(right)
	return err == nil && string(leftCanonical) == string(rightCanonical)
}

func findFlowByNameTx(ctx context.Context, tx *sql.Tx, name string) (Flow, bool, error) {
	found, err := scanFlow(tx.QueryRowContext(ctx, `SELECT `+flowColumns+` FROM flows WHERE name = ?`, name))
	if errors.Is(err, ErrNotFound) {
		return Flow{}, false, nil
	}
	return found, err == nil, err
}

func sortedNames[V any](values map[string]V) []string {
	names := make([]string, 0, len(values))
	for name := range values {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
