package replace

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/bmatcuk/doublestar/v4"
	"github.com/zekurio/anvil/pkg/mediapath"
	"github.com/zekurio/anvil/pkg/pipeline"
)

const (
	replacementActionReplace = "replace"
	// anvilCopySuffix marked the copies the retired replacement copy mode
	// published beside their sources. Scans still leave such files alone.
	anvilCopySuffix = ".anvil"

	handoffActionCopy = "copy"
	handoffActionMove = "move"

	handoffDirMode  os.FileMode = 0o2775
	handoffFileMode os.FileMode = 0o664
)

type ReplacementPlan struct {
	ReplaceTarget string
	BackupPath    string
}

// HandoffPlan is how a move publishes: the journal calls a move a handoff,
// and its Action is "move" when the source goes and "copy" when it stays.
type HandoffPlan struct {
	Action             string
	Destination        string
	CleanupSourceMedia bool
	PruneEmptyDirs     bool
	SourceMediaPath    string
	PruneStart         string
}

// PlanReplacement returns the destination and backup path of an in-place
// replacement. The destination itself comes from PlanDestination at stage
// time; outputExt is the container extension the destination carries.
func PlanReplacement(inputPath string, outputExt string) (ReplacementPlan, error) {
	if inputPath == "" {
		return ReplacementPlan{}, errors.New("replace input path is required")
	}
	return ReplacementPlan{
		ReplaceTarget: replaceExtension(inputPath, outputExt),
		BackupPath:    inputPath + ".anvil.bak",
	}, nil
}

// PlanHandoff describes how a moved artifact publishes. The destination must
// already be planned on the job context by the stage step.
func PlanHandoff(job *pipeline.JobContext) (HandoffPlan, error) {
	if job == nil {
		return HandoffPlan{}, errors.New("handoff job context is required")
	}
	destination := strings.TrimSpace(job.DestinationPath)
	if destination == "" {
		return HandoffPlan{}, errors.New("handoff destination is required; the stage step must plan it first")
	}
	publish := job.Profile.Publish
	action := handoffActionMove
	if publish.KeepSource {
		action = handoffActionCopy
	}
	return HandoffPlan{
		Action:             action,
		Destination:        destination,
		CleanupSourceMedia: !publish.KeepSource,
		PruneEmptyDirs:     !publish.KeepSource && publish.PruneEmptyDirs,
		SourceMediaPath:    job.InputPath,
		PruneStart:         filepath.Dir(job.InputPath),
	}, nil
}

type PublishBlock struct {
	Manager Manager
}

func (PublishBlock) Name() string {
	return "publish"
}

func (b PublishBlock) Run(ctx context.Context, job *pipeline.JobContext) error {
	var finalPath string
	var err error
	if job.Profile.Publish.Moves() {
		finalPath, err = b.Manager.Handoff(ctx, job)
	} else {
		finalPath, err = b.Manager.Replace(ctx, job)
	}
	if err != nil {
		return err
	}
	job.FinalPath = finalPath
	return nil
}

func (b PublishBlock) Recover(ctx context.Context, job *pipeline.JobContext) (bool, error) {
	return b.Manager.Recover(ctx, job)
}

func handoffDestination(job *pipeline.JobContext, ext string) (string, error) {
	root := strings.TrimSpace(job.Profile.Publish.Destination)
	if root == "" || !filepath.IsAbs(root) {
		return "", fmt.Errorf("the move needs an absolute destination folder, not %q", root)
	}
	var rel string
	if job.Profile.Publish.PreserveStructure {
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
	return filepath.Join(root, rel), nil
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
		if pathOrBaseMatches(pattern, rel) {
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

func pathOrBaseMatches(pattern string, rel string) bool {
	if pathMatches(pattern, rel) {
		return true
	}
	return !strings.Contains(pattern, "/") && pathMatches(pattern, path.Base(strings.TrimSuffix(rel, "/")))
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
