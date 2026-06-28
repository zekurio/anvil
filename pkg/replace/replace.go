package replace

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/bmatcuk/doublestar/v4"
	"github.com/zekurio/anvil/pkg/domain"
	"github.com/zekurio/anvil/pkg/pipeline"
)

type Manager struct{}

type ReplacementPlan struct {
	Action        string
	Mode          domain.ReplacementMode
	CopyPath      string
	ReplaceTarget string
	BackupPath    string
}

type HandoffPlan struct {
	Action             string
	Mode               domain.HandoffMode
	Destination        string
	CleanupSourceMedia bool
	PruneEmptyDirs     bool
	SourceMediaPath    string
	PruneStart         string
}

func (Manager) Replace(_ context.Context, inputPath string, candidatePath string, mode domain.ReplacementMode) (string, error) {
	plan, err := PlanReplacement(inputPath, candidatePath, mode)
	if err != nil {
		return "", err
	}
	switch plan.Action {
	case "copy":
		if err := copyFile(candidatePath, plan.CopyPath); err != nil {
			return "", err
		}
		return plan.CopyPath, nil
	default:
		return replaceFile(inputPath, candidatePath, plan.ReplaceTarget)
	}
}

func PlanReplacement(inputPath string, candidatePath string, mode domain.ReplacementMode) (ReplacementPlan, error) {
	if inputPath == "" {
		return ReplacementPlan{}, errors.New("replace input path is required")
	}
	if candidatePath == "" {
		return ReplacementPlan{}, errors.New("replace candidate path is required")
	}
	plan := ReplacementPlan{Mode: mode}
	switch mode {
	case domain.ReplacementModeCopy:
		plan.Action = "copy"
		plan.CopyPath = replacementCopyPath(inputPath, filepath.Ext(candidatePath))
	default:
		plan.Action = "replace"
		plan.ReplaceTarget = replaceExtension(inputPath, filepath.Ext(candidatePath))
		plan.BackupPath = inputPath + ".anvil.bak"
	}
	return plan, nil
}

func (m Manager) Handoff(_ context.Context, job *pipeline.JobContext) (string, error) {
	plan, err := PlanHandoff(job)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(plan.Destination), 0o750); err != nil {
		return "", fmt.Errorf("create handoff destination dir: %w", err)
	}
	switch plan.Mode {
	case domain.HandoffModeMove:
		if err := moveFile(job.OutputPath, plan.Destination); err != nil {
			return "", err
		}
	default:
		if err := copyFile(job.OutputPath, plan.Destination); err != nil {
			return "", err
		}
	}
	if plan.CleanupSourceMedia {
		if err := os.Remove(plan.SourceMediaPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			return "", fmt.Errorf("remove source media after handoff: %w", err)
		}
		if plan.PruneEmptyDirs {
			if err := PruneEmptyDirs(job.Library.Path, plan.PruneStart, job.Library.Download.IgnorableGlobs); err != nil {
				return "", err
			}
		}
	}
	return plan.Destination, nil
}

func PlanHandoff(job *pipeline.JobContext) (HandoffPlan, error) {
	if job == nil {
		return HandoffPlan{}, errors.New("handoff job context is required")
	}
	if strings.TrimSpace(job.Library.Download.HandoffPath) == "" {
		return HandoffPlan{}, errors.New("download handoff path is required")
	}
	destination, err := handoffDestination(job, filepath.Ext(job.OutputPath))
	if err != nil {
		return HandoffPlan{}, err
	}
	mode := job.Library.Download.HandoffMode
	action := "copy"
	if mode == domain.HandoffModeMove {
		action = "move"
	}
	return HandoffPlan{
		Action:             action,
		Mode:               mode,
		Destination:        destination,
		CleanupSourceMedia: job.Library.Download.CleanupSourceMedia,
		PruneEmptyDirs:     job.Library.Download.PruneEmptyDirs,
		SourceMediaPath:    job.InputPath,
		PruneStart:         filepath.Dir(job.InputPath),
	}, nil
}

type ReplaceBlock struct {
	Manager Manager
}

func (ReplaceBlock) Name() string {
	return "replace"
}

func (b ReplaceBlock) Run(ctx context.Context, job *pipeline.JobContext) error {
	finalPath, err := b.Manager.Replace(ctx, job.InputPath, job.OutputPath, job.Library.Media.ReplacementMode)
	if err != nil {
		return err
	}
	job.FinalPath = finalPath
	return nil
}

type HandoffBlock struct {
	Manager Manager
}

func (HandoffBlock) Name() string {
	return "handoff"
}

func (b HandoffBlock) Run(ctx context.Context, job *pipeline.JobContext) error {
	finalPath, err := b.Manager.Handoff(ctx, job)
	if err != nil {
		return err
	}
	job.FinalPath = finalPath
	return nil
}

func replaceFile(inputPath string, candidatePath string, targetPath string) (string, error) {
	if targetPath == "" {
		targetPath = inputPath
	}
	backupPath := inputPath + ".anvil.bak"
	if err := moveFile(inputPath, backupPath); err != nil {
		return "", fmt.Errorf("backup original before replace: %w", err)
	}
	if err := moveFile(candidatePath, targetPath); err != nil {
		if restoreErr := moveFile(backupPath, inputPath); restoreErr != nil {
			return "", fmt.Errorf("install replacement: %w; restore backup: %v", err, restoreErr)
		}
		return "", fmt.Errorf("install replacement: %w", err)
	}
	if err := os.Remove(backupPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("remove replacement backup: %w", err)
	}
	return targetPath, nil
}

func handoffDestination(job *pipeline.JobContext, ext string) (string, error) {
	var rel string
	if job.Library.Download.PreserveRelativePath {
		if job.Source.Kind == domain.SourceKindPackage && job.Asset.RelativePath != "" {
			rel = filepath.Join(filepath.FromSlash(job.Source.RelativePath), filepath.FromSlash(job.Asset.RelativePath))
		} else {
			rel = filepath.FromSlash(job.Source.RelativePath)
		}
	} else {
		rel = filepath.Base(job.InputPath)
	}
	if ext != "" {
		rel = replaceExtension(rel, ext)
	}
	if unsafeRelativePath(rel) {
		return "", fmt.Errorf("unsafe handoff relative path %q", rel)
	}
	return filepath.Join(job.Library.Download.HandoffPath, rel), nil
}

func replacementCopyPath(inputPath string, ext string) string {
	if ext == "" {
		ext = filepath.Ext(inputPath)
	}
	base := strings.TrimSuffix(inputPath, filepath.Ext(inputPath))
	return base + ".anvil" + ext
}

func replaceExtension(path string, ext string) string {
	if ext == "" {
		ext = filepath.Ext(path)
	}
	return strings.TrimSuffix(path, filepath.Ext(path)) + ext
}

func unsafeRelativePath(path string) bool {
	clean := filepath.Clean(path)
	return filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator))
}

func moveFile(src string, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o750); err != nil {
		return fmt.Errorf("create destination dir: %w", err)
	}
	if err := os.Link(src, dst); err == nil {
		if err := os.Remove(src); err != nil {
			return fmt.Errorf("remove moved source %q: %w", src, err)
		}
		return nil
	} else if errors.Is(err, os.ErrExist) {
		return fmt.Errorf("destination %q already exists", dst)
	} else if !errors.Is(err, syscall.EXDEV) && !errors.Is(err, syscall.EPERM) && !errors.Is(err, syscall.ENOTSUP) {
		if _, statErr := os.Stat(dst); statErr == nil {
			return fmt.Errorf("destination %q already exists", dst)
		}
	}
	if err := copyFile(src, dst); err != nil {
		return err
	}
	if err := os.Remove(src); err != nil {
		return fmt.Errorf("remove moved source %q: %w", src, err)
	}
	return nil
}

func copyFile(src string, dst string) error {
	dstDir := filepath.Dir(dst)
	if err := os.MkdirAll(dstDir, 0o750); err != nil {
		return fmt.Errorf("create destination dir: %w", err)
	}
	info, err := os.Stat(src)
	if err != nil {
		return fmt.Errorf("stat source %q: %w", src, err)
	}
	in, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("open source %q: %w", src, err)
	}
	defer in.Close()

	tmp, err := os.CreateTemp(dstDir, "."+filepath.Base(dst)+".tmp-*")
	if err != nil {
		return fmt.Errorf("create temporary destination for %q: %w", dst, err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)

	_, copyErr := io.Copy(tmp, in)
	chmodErr := tmp.Chmod(info.Mode().Perm())
	closeErr := tmp.Close()
	if copyErr != nil {
		return fmt.Errorf("copy %q to temporary destination: %w", src, copyErr)
	}
	if chmodErr != nil {
		return fmt.Errorf("set temporary destination mode: %w", chmodErr)
	}
	if closeErr != nil {
		return fmt.Errorf("close temporary destination for %q: %w", dst, closeErr)
	}
	if err := os.Link(tmpPath, dst); err != nil {
		if errors.Is(err, os.ErrExist) {
			return fmt.Errorf("destination %q already exists", dst)
		}
		return fmt.Errorf("publish temporary destination %q: %w", dst, err)
	}
	return nil
}

func PruneEmptyDirs(root string, start string, ignorableGlobs []string) error {
	root = filepath.Clean(root)
	dir := filepath.Clean(start)
	if root == "." || root == string(filepath.Separator) {
		return errors.New("refusing to prune unsafe root")
	}
	if !inside(root, dir) {
		return fmt.Errorf("prune start %q is outside root %q", dir, root)
	}
	for dir != root {
		kept, err := removeIgnorableAndDetectKept(root, dir, ignorableGlobs)
		if err != nil {
			return err
		}
		if kept {
			return nil
		}
		if err := os.Remove(dir); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("remove empty dir %q: %w", dir, err)
		}
		dir = filepath.Dir(dir)
	}
	return nil
}

func removeIgnorableAndDetectKept(root string, dir string, ignorableGlobs []string) (bool, error) {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return true, fmt.Errorf("read dir %q: %w", dir, err)
	}
	for _, entry := range entries {
		path := filepath.Join(dir, entry.Name())
		if ignorable(root, path, entry.IsDir(), ignorableGlobs) {
			if err := os.RemoveAll(path); err != nil {
				return true, fmt.Errorf("remove ignorable %q: %w", path, err)
			}
			continue
		}
		if !entry.IsDir() {
			return true, nil
		}
		kept, err := removeIgnorableAndDetectKept(root, path, ignorableGlobs)
		if err != nil {
			return true, err
		}
		if kept {
			return true, nil
		}
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return true, fmt.Errorf("remove nested empty dir %q: %w", path, err)
		}
	}
	return false, nil
}

func ignorable(root string, path string, isDir bool, patterns []string) bool {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	rel = filepath.ToSlash(rel)
	for _, pattern := range patterns {
		pattern = strings.TrimSpace(filepath.ToSlash(pattern))
		if pattern == "" {
			continue
		}
		if matched, _ := doublestar.PathMatch(pattern, rel); matched {
			return true
		}
		if isDir {
			if matched, _ := doublestar.PathMatch(pattern, rel+"/"); matched {
				return true
			}
			if strings.HasSuffix(pattern, "/**") {
				prefix := strings.TrimSuffix(pattern, "/**")
				if matched, _ := doublestar.PathMatch(prefix, rel); matched {
					return true
				}
			}
		}
	}
	return false
}

func inside(root string, path string) bool {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	return rel == "." || (!strings.HasPrefix(rel, ".."+string(filepath.Separator)) && rel != "..")
}
