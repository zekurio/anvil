package replace

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// CleanupBlocker identifies an entry that prevents package residue cleanup.
// The cleanup policy is all-or-nothing so a package with any blocker records
// no cleanup entries.
type CleanupBlocker struct {
	Path   string `json:"path"`
	Reason string `json:"reason"`
}

// ResidueCleanupPlan is the conservative cleanup manifest for one download
// package. Root and Start are resolved paths suitable for the journaled prune
// operation; Entries is empty when Blockers is non-empty.
type ResidueCleanupPlan struct {
	Root     string
	Start    string
	Entries  []CleanupEntry
	Blockers []CleanupBlocker
}

// PlanResidueCleanup inspects the source file's containing directory as though
// the source file were already gone. It records only regular files covered by
// ignorableGlobs, does not follow symlinks, and refuses the entire residue
// cleanup when any remaining file is not explicitly eligible.
func PlanResidueCleanup(root string, sourcePath string, ignorableGlobs []string) (ResidueCleanupPlan, error) {
	root, start, err := cleanupScopePaths(root, sourcePath)
	if err != nil {
		return ResidueCleanupPlan{}, err
	}
	plan := ResidueCleanupPlan{Root: root, Start: start}
	sourceName := filepath.Base(filepath.Clean(sourcePath))
	if err := discoverResidueCleanup(&plan, root, start, sourceName, false, ignorableGlobs); err != nil {
		return ResidueCleanupPlan{}, err
	}
	if len(plan.Blockers) > 0 {
		plan.Entries = nil
	}
	return plan, nil
}

func cleanupScopePaths(root string, sourcePath string) (string, string, error) {
	root = strings.TrimSpace(root)
	sourcePath = strings.TrimSpace(sourcePath)
	if root == "" {
		return "", "", errors.New("cleanup library root is required")
	}
	if sourcePath == "" {
		return "", "", errors.New("cleanup source path is required")
	}

	rootPath, err := filepath.Abs(root)
	if err != nil {
		return "", "", fmt.Errorf("resolve cleanup library root %q: %w", root, err)
	}
	startPath, err := filepath.Abs(filepath.Dir(sourcePath))
	if err != nil {
		return "", "", fmt.Errorf("resolve cleanup source directory %q: %w", sourcePath, err)
	}
	resolvedRoot, err := filepath.EvalSymlinks(rootPath)
	if err != nil {
		return "", "", fmt.Errorf("resolve cleanup library root %q: %w", root, err)
	}
	resolvedStart, err := filepath.EvalSymlinks(startPath)
	if err != nil {
		return "", "", fmt.Errorf("resolve cleanup source directory %q: %w", filepath.Dir(sourcePath), err)
	}
	resolvedRoot = filepath.Clean(resolvedRoot)
	resolvedStart = filepath.Clean(resolvedStart)
	if !inside(resolvedRoot, resolvedStart) {
		return "", "", fmt.Errorf("cleanup source directory %q resolves outside library root %q", filepath.Dir(sourcePath), root)
	}
	info, err := os.Lstat(resolvedStart)
	if err != nil {
		return "", "", fmt.Errorf("inspect cleanup source directory %q: %w", resolvedStart, err)
	}
	if !info.IsDir() {
		return "", "", fmt.Errorf("cleanup source directory %q is not a directory", resolvedStart)
	}
	return resolvedRoot, resolvedStart, nil
}

func discoverResidueCleanup(plan *ResidueCleanupPlan, root string, dir string, sourceName string, parentEligible bool, patterns []string) error {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read cleanup directory %q: %w", dir, err)
	}
	for _, entry := range entries {
		if dir == plan.Start && entry.Name() == sourceName {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		info, err := os.Lstat(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return fmt.Errorf("inspect cleanup entry %q: %w", path, err)
		}

		switch {
		case info.Mode()&os.ModeSymlink != 0:
			plan.Blockers = append(plan.Blockers, CleanupBlocker{Path: path, Reason: "symlink is not eligible for cleanup"})
		case info.IsDir():
			directoryEligible := parentEligible || ignorable(root, path, true, patterns)
			if err := discoverResidueCleanup(plan, root, path, sourceName, directoryEligible, patterns); err != nil {
				return err
			}
		case info.Mode().IsRegular():
			if !parentEligible && !ignorable(root, path, false, patterns) {
				plan.Blockers = append(plan.Blockers, CleanupBlocker{Path: path, Reason: "not matched by download ignorable globs"})
				continue
			}
			identity, err := cleanupEntryIdentity(plan.Root, plan.Start, path)
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			if err != nil {
				return fmt.Errorf("identify cleanup entry %q: %w", path, err)
			}
			plan.Entries = append(plan.Entries, CleanupEntry{Path: path, Identity: identity})
		default:
			plan.Blockers = append(plan.Blockers, CleanupBlocker{Path: path, Reason: fmt.Sprintf("unsafe %s entry is not eligible for cleanup", info.Mode().Type())})
		}
	}
	return nil
}

func (m Manager) cleanupEntries(ctx context.Context, op *PublishOperation) error {
	if len(op.CleanupEntries) == 0 {
		return nil
	}
	for _, entry := range op.CleanupEntries {
		if err := validCleanupEntryPath(op.PruneStart, entry.Path); err != nil {
			return m.conflict(ctx, op, fmt.Sprintf("cleanup entry path is unsafe: %v", err))
		}
		identity, err := cleanupEntryIdentity(op.PruneRoot, op.PruneStart, entry.Path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return m.conflict(ctx, op, fmt.Sprintf("cleanup entry changed: %v", err))
		}
		if identity.SizeBytes != entry.Identity.SizeBytes || !sameFileIdentity(identity, entry.Identity) {
			return m.conflict(ctx, op, fmt.Sprintf("cleanup entry identity changed: %q", entry.Path))
		}
		if err := removeAndSync(entry.Path); err != nil {
			return fmt.Errorf("remove journaled cleanup entry %q: %w", entry.Path, err)
		}
	}
	return nil
}

func validCleanupEntryPath(scope string, path string) error {
	scope = filepath.Clean(scope)
	path = filepath.Clean(path)
	if !filepath.IsAbs(scope) || !filepath.IsAbs(path) {
		return errors.New("cleanup scope and entry path must be absolute")
	}
	if path == scope || !inside(scope, path) {
		return fmt.Errorf("entry %q is outside cleanup scope %q", path, scope)
	}
	return nil
}

func cleanupEntryIdentity(root string, scope string, path string) (FileIdentity, error) {
	if err := validCleanupEntryPath(scope, path); err != nil {
		return FileIdentity{}, err
	}
	exists, err := cleanupDirectory(root, scope)
	if err != nil {
		return FileIdentity{}, err
	}
	if !exists {
		return FileIdentity{}, os.ErrNotExist
	}
	rel, err := filepath.Rel(scope, path)
	if err != nil {
		return FileIdentity{}, err
	}
	current := filepath.Clean(scope)
	cleanPath := filepath.Clean(path)
	for _, component := range strings.Split(rel, string(filepath.Separator)) {
		current = filepath.Join(current, component)
		info, err := os.Lstat(current)
		if err != nil {
			return FileIdentity{}, err
		}
		if current != cleanPath {
			if !info.IsDir() {
				return FileIdentity{}, fmt.Errorf("cleanup parent %q is not a directory", current)
			}
			continue
		}
		if !info.Mode().IsRegular() {
			return FileIdentity{}, fmt.Errorf("cleanup entry %q is not a regular file", path)
		}
		return fileIdentity(info), nil
	}
	return FileIdentity{}, fmt.Errorf("cleanup entry %q has no path components", path)
}

func fileIdentity(info os.FileInfo) FileIdentity {
	identity := FileIdentity{SizeBytes: info.Size()}
	if stat, ok := info.Sys().(*syscall.Stat_t); ok {
		identity.Device = uint64(stat.Dev)
		identity.Inode = uint64(stat.Ino)
	}
	return identity
}

func cleanupDirectory(root string, path string) (bool, error) {
	root = filepath.Clean(root)
	path = filepath.Clean(path)
	if !filepath.IsAbs(root) || !filepath.IsAbs(path) || !inside(root, path) {
		return false, fmt.Errorf("directory %q is outside cleanup root %q", path, root)
	}
	info, err := os.Lstat(root)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !info.IsDir() {
		return false, fmt.Errorf("cleanup root %q is not a directory", root)
	}
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return false, err
	}
	current := root
	if rel == "." {
		return true, nil
	}
	for _, component := range strings.Split(rel, string(filepath.Separator)) {
		current = filepath.Join(current, component)
		info, err := os.Lstat(current)
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		if !info.IsDir() {
			return false, fmt.Errorf("cleanup directory %q is not a directory", current)
		}
	}
	return true, nil
}

func pruneEmptyDirectories(root string, start string) error {
	root, start, err := pruneCleanupPaths(root, start)
	if err != nil {
		return err
	}
	removed, err := pruneEmptyDirectoryTree(root, start)
	if err != nil {
		return err
	}
	if !removed {
		return nil
	}
	for dir := filepath.Dir(start); dir != root; dir = filepath.Dir(dir) {
		removed, err := removeEmptyCleanupDirectory(root, dir)
		if err != nil {
			return err
		}
		if !removed {
			return nil
		}
	}
	return nil
}

func pruneEmptyDirectoryTree(root string, dir string) (bool, error) {
	exists, err := cleanupDirectory(root, dir)
	if err != nil {
		return false, fmt.Errorf("inspect cleanup directory %q: %w", dir, err)
	}
	if !exists {
		return true, nil
	}
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return true, nil
	}
	if err != nil {
		return false, fmt.Errorf("read cleanup directory %q: %w", dir, err)
	}
	for _, entry := range entries {
		path := filepath.Join(dir, entry.Name())
		info, err := os.Lstat(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return false, fmt.Errorf("inspect cleanup directory entry %q: %w", path, err)
		}
		if !info.IsDir() {
			continue
		}
		if _, err := pruneEmptyDirectoryTree(root, path); err != nil {
			return false, err
		}
	}
	return removeEmptyCleanupDirectory(root, dir)
}

func removeEmptyCleanupDirectory(root string, dir string) (bool, error) {
	exists, err := cleanupDirectory(root, dir)
	if err != nil {
		return false, fmt.Errorf("inspect cleanup directory %q: %w", dir, err)
	}
	if !exists {
		return true, nil
	}
	if err := os.Remove(dir); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return true, nil
		}
		if errors.Is(err, syscall.ENOTEMPTY) || errors.Is(err, syscall.EEXIST) {
			return false, nil
		}
		return false, fmt.Errorf("remove empty cleanup directory %q: %w", dir, err)
	}
	if err := syncDir(filepath.Dir(dir)); err != nil {
		return false, fmt.Errorf("sync cleanup directory parent %q: %w", filepath.Dir(dir), err)
	}
	return true, nil
}

func pruneCleanupPaths(root string, start string) (string, string, error) {
	root = strings.TrimSpace(root)
	start = strings.TrimSpace(start)
	if root == "" || start == "" {
		return "", "", errors.New("cleanup root and start are required")
	}
	rootPath, err := filepath.Abs(root)
	if err != nil {
		return "", "", fmt.Errorf("resolve cleanup root %q: %w", root, err)
	}
	startPath, err := filepath.Abs(start)
	if err != nil {
		return "", "", fmt.Errorf("resolve cleanup start %q: %w", start, err)
	}
	if !inside(rootPath, startPath) {
		return "", "", fmt.Errorf("cleanup start %q is outside root %q", start, root)
	}
	resolvedRoot, err := filepath.EvalSymlinks(rootPath)
	if err != nil {
		return "", "", fmt.Errorf("resolve cleanup root %q: %w", root, err)
	}
	rel, err := filepath.Rel(rootPath, startPath)
	if err != nil {
		return "", "", fmt.Errorf("resolve cleanup start %q: %w", start, err)
	}
	return filepath.Clean(resolvedRoot), filepath.Join(filepath.Clean(resolvedRoot), rel), nil
}
