package replace

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/bmatcuk/doublestar/v4"
	"github.com/zekurio/anvil/pkg/domain"
	"github.com/zekurio/anvil/pkg/mediapath"
	"github.com/zekurio/anvil/pkg/pipeline"
)

const (
	replacementActionCopy    = "copy"
	replacementActionReplace = "replace"
	anvilCopySuffix          = ".anvil"

	handoffActionCopy = "copy"
	handoffActionMove = "move"

	handoffDirMode  os.FileMode = 0o2775
	handoffFileMode os.FileMode = 0o664
)

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
		plan.Action = replacementActionCopy
		plan.CopyPath = replacementCopyPath(inputPath, filepath.Ext(candidatePath))
	default:
		plan.Action = replacementActionReplace
		plan.ReplaceTarget = replaceExtension(inputPath, filepath.Ext(candidatePath))
		plan.BackupPath = inputPath + ".anvil.bak"
	}
	return plan, nil
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
	action := handoffActionCopy
	if mode == domain.HandoffModeMove {
		action = handoffActionMove
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
	finalPath, err := b.Manager.Replace(ctx, job)
	if err != nil {
		return err
	}
	job.FinalPath = finalPath
	return nil
}

func (b ReplaceBlock) Recover(ctx context.Context, job *pipeline.JobContext) (bool, error) {
	return b.Manager.Recover(ctx, job)
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

func (b HandoffBlock) Recover(ctx context.Context, job *pipeline.JobContext) (bool, error) {
	return b.Manager.Recover(ctx, job)
}

func handoffDestination(job *pipeline.JobContext, ext string) (string, error) {
	var rel string
	if job.Library.Download.PreserveRelativePath {
		rel = mediapath.Relative(job.Source, job.Asset)
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
	return base + anvilCopySuffix + ext
}

func IsAnvilCopyOutputPath(path string) bool {
	base := filepath.Base(path)
	ext := filepath.Ext(base)
	stem := strings.TrimSuffix(base, ext)
	return ext != "" && strings.HasSuffix(strings.ToLower(stem), anvilCopySuffix)
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

func prepareHandoffDestination(root string, dir string) error {
	root = filepath.Clean(strings.TrimSpace(root))
	dir = filepath.Clean(dir)
	if root == "" || root == "." || root == string(filepath.Separator) {
		return errors.New("refusing to prepare unsafe handoff root")
	}
	if !inside(root, dir) {
		return fmt.Errorf("handoff destination dir %q is outside root %q", dir, root)
	}
	if err := os.MkdirAll(dir, handoffDirMode); err != nil {
		return fmt.Errorf("create handoff destination dir: %w", err)
	}
	for current := dir; ; current = filepath.Dir(current) {
		if err := os.Chmod(current, handoffDirMode); err != nil {
			return fmt.Errorf("set handoff directory mode %q: %w", current, err)
		}
		if current == root {
			return nil
		}
	}
}

func closeFile(file *os.File, description string, err *error) {
	if closeErr := file.Close(); closeErr != nil {
		wrapped := fmt.Errorf("close %s %q: %w", description, file.Name(), closeErr)
		if *err != nil {
			*err = errors.Join(*err, wrapped)
			return
		}
		*err = wrapped
	}
}

func removeTempFile(path string, err *error) {
	if removeErr := os.Remove(path); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
		wrapped := fmt.Errorf("remove temporary destination %q: %w", path, removeErr)
		if *err != nil {
			*err = errors.Join(*err, wrapped)
			return
		}
		*err = wrapped
	}
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
		if pathMatches(pattern, rel) {
			return true
		}
		if isDir {
			if pathMatches(pattern, rel+"/") {
				return true
			}
			if strings.HasSuffix(pattern, "/**") {
				prefix := strings.TrimSuffix(pattern, "/**")
				if pathMatches(prefix, rel) {
					return true
				}
			}
		}
	}
	return false
}

func pathMatches(pattern string, rel string) bool {
	matched, err := doublestar.PathMatch(pattern, rel)
	return err == nil && matched
}

func inside(root string, path string) bool {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	return rel == "." || (!strings.HasPrefix(rel, ".."+string(filepath.Separator)) && rel != "..")
}
