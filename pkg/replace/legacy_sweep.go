package replace

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type LegacySweepOptions struct {
	Roots     []string
	OlderThan time.Duration
	Now       time.Time
	DryRun    bool
}

type LegacySweepResult struct {
	Candidates int
	Removed    int
	Protected  int
}

// SweepLegacyParts is explicit maintenance for the old artifact layout.
// Current jobs use scoped names and never create these files. Each root is
// walked once, instead of listing a destination directory for every job.
func SweepLegacyParts(ctx context.Context, protection ArtifactProtection, options LegacySweepOptions) (LegacySweepResult, error) {
	if options.OlderThan <= 0 {
		return LegacySweepResult{}, errors.New("legacy cleanup age must be positive")
	}
	if protection == nil {
		return LegacySweepResult{}, errors.New("legacy part cleanup requires publish journal protection")
	}
	now := options.Now
	if now.IsZero() {
		now = time.Now()
	}
	sweep := &legacySweep{
		protection: protection,
		cutoff:     now.Add(-options.OlderThan),
		candidates: make(map[string]fs.FileInfo),
		seen:       make(map[string]struct{}),
	}
	for _, root := range options.Roots {
		if !filepath.IsAbs(root) || filepath.Clean(root) == string(filepath.Separator) {
			return sweep.result, fmt.Errorf("unsafe legacy cleanup root %q", root)
		}
		err := filepath.WalkDir(filepath.Clean(root), func(path string, entry fs.DirEntry, walkErr error) error {
			return sweep.visit(ctx, path, entry, walkErr)
		})
		if err != nil {
			return sweep.result, fmt.Errorf("scan legacy artifacts: %w", err)
		}
	}
	sweep.result.Candidates = len(sweep.candidates)
	if options.DryRun {
		return sweep.result, nil
	}
	for path, expected := range sweep.candidates {
		if err := ctx.Err(); err != nil {
			return sweep.result, err
		}
		removed, err := removeLegacyPart(path, expected)
		if err != nil {
			return sweep.result, err
		}
		if removed {
			sweep.result.Removed++
		}
	}
	return sweep.result, nil
}

// legacySweep collects removable legacy parts across possibly overlapping
// roots; seen keeps a nested root from being walked twice.
type legacySweep struct {
	protection ArtifactProtection
	cutoff     time.Time
	candidates map[string]fs.FileInfo
	seen       map[string]struct{}
	result     LegacySweepResult
}

func (s *legacySweep) visit(ctx context.Context, path string, entry fs.DirEntry, walkErr error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if errors.Is(walkErr, os.ErrNotExist) {
		return nil
	}
	if walkErr != nil {
		return walkErr
	}
	if _, ok := s.seen[path]; ok {
		if entry.IsDir() {
			return filepath.SkipDir
		}
		return nil
	}
	if entry.IsDir() {
		s.seen[path] = struct{}{}
		return nil
	}
	if entry.Type()&fs.ModeType != 0 || !legacyPartName(entry.Name()) {
		return nil
	}
	info, err := entry.Info()
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.ModTime().After(s.cutoff) {
		return nil
	}
	protected, err := s.protection.PublishArtifactProtected(ctx, path)
	if err != nil {
		return fmt.Errorf("check legacy part protection for %q: %w", path, err)
	}
	if protected {
		s.result.Protected++
		return nil
	}
	s.candidates[path] = info
	return nil
}

// removeLegacyPart deletes a candidate only if it is still the file the walk
// saw. A file that changed since then is an error, not a deletion.
func removeLegacyPart(path string, expected fs.FileInfo) (bool, error) {
	current, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("inspect legacy artifact %q: %w", path, err)
	}
	if !current.Mode().IsRegular() || !os.SameFile(expected, current) || current.Size() != expected.Size() || !current.ModTime().Equal(expected.ModTime()) {
		return false, fmt.Errorf("legacy artifact changed during cleanup: %q", path)
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return false, fmt.Errorf("remove legacy artifact %q: %w", path, err)
	}
	return true, nil
}

func legacyPartName(name string) bool {
	// The legacy layout always followed the final MKV name. A current
	// <name>.mkv.job-<id>.anvil-part must never match this predicate.
	index := strings.LastIndex(name, PartSuffix)
	if index <= len(".mkv") || !strings.HasSuffix(name[:index], ".mkv") {
		return false
	}
	suffix := name[index+len(PartSuffix):]
	switch suffix {
	case "", ".pre-dovi", ".dovi-fixed":
		return true
	default:
		return false
	}
}
