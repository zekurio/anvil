package replace

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/zekurio/anvil/pkg/domain"
	"github.com/zekurio/anvil/pkg/pipeline"
)

func TestHandoffMoveCleansOnlyProcessedEpisodeFolder(t *testing.T) {
	root := t.TempDir()
	imports := t.TempDir()
	input := filepath.Join(root, "SomeShowS01", "SomeShowS01E01", "episode_1.mkv")
	other := filepath.Join(root, "SomeShowS01", "SomeShowS01E02", "episode_2.mkv")
	writeFile(t, input, "source")
	writeFile(t, filepath.Join(root, "SomeShowS01", "SomeShowS01E01", ".nfs123"), "lock")
	writeFile(t, other, "other")
	candidate := filepath.Join(t.TempDir(), "output.mkv")
	writeFile(t, candidate, "encoded")

	job := &pipeline.JobContext{
		Source: domain.MediaSource{
			Kind:         domain.SourceKindPackage,
			RelativePath: "SomeShowS01",
		},
		Asset: domain.MediaAsset{
			RelativePath: "SomeShowS01E01/episode_1.mkv",
		},
		Library: domain.Library{
			Kind: domain.LibraryKindDownload,
			Path: root,
			Download: domain.DownloadLibraryPolicy{
				HandoffPath:          imports,
				HandoffMode:          domain.HandoffModeMove,
				PreserveRelativePath: true,
				CleanupSourceMedia:   true,
				PruneEmptyDirs:       true,
				IgnorableGlobs:       []string{"**/.nfs*"},
			},
		},
		InputPath:  input,
		OutputPath: candidate,
	}

	finalPath, err := (Manager{}).Handoff(context.Background(), job)
	if err != nil {
		t.Fatalf("Handoff() error = %v", err)
	}
	if _, err := os.Stat(finalPath); err != nil {
		t.Fatalf("final handoff stat: %v", err)
	}
	if _, err := os.Stat(candidate); !os.IsNotExist(err) {
		t.Fatalf("candidate still exists or unexpected stat error: %v", err)
	}
	if _, err := os.Stat(filepath.Dir(input)); !os.IsNotExist(err) {
		t.Fatalf("processed episode dir still exists or unexpected stat error: %v", err)
	}
	if _, err := os.Stat(other); err != nil {
		t.Fatalf("other episode was removed: %v", err)
	}
}

func TestReplaceSidecarLeavesInputInPlace(t *testing.T) {
	dir := t.TempDir()
	input := filepath.Join(dir, "movie.mkv")
	candidate := filepath.Join(dir, "candidate.mkv")
	writeFile(t, input, "source")
	writeFile(t, candidate, "encoded")

	finalPath, err := (Manager{}).Replace(context.Background(), input, candidate, domain.ReplacementModeSidecar)
	if err != nil {
		t.Fatalf("Replace() error = %v", err)
	}
	if finalPath == input {
		t.Fatalf("sidecar final path = input path %q", input)
	}
	if got := readFile(t, input); got != "source" {
		t.Fatalf("input content = %q, want source", got)
	}
	if got := readFile(t, finalPath); got != "encoded" {
		t.Fatalf("sidecar content = %q, want encoded", got)
	}
}

func TestReplaceSidecarRefusesExistingDestination(t *testing.T) {
	dir := t.TempDir()
	input := filepath.Join(dir, "movie.mkv")
	candidate := filepath.Join(dir, "candidate.mkv")
	sidecar := filepath.Join(dir, "movie.anvil.mkv")
	writeFile(t, input, "source")
	writeFile(t, candidate, "encoded")
	writeFile(t, sidecar, "existing")

	if _, err := (Manager{}).Replace(context.Background(), input, candidate, domain.ReplacementModeSidecar); err == nil {
		t.Fatal("Replace() error = nil, want existing sidecar refusal")
	}
	if got := readFile(t, sidecar); got != "existing" {
		t.Fatalf("sidecar content = %q, want existing", got)
	}
}

func TestReplaceRefusesExistingBackup(t *testing.T) {
	dir := t.TempDir()
	input := filepath.Join(dir, "movie.mkv")
	candidate := filepath.Join(dir, "candidate.mkv")
	backup := input + ".anvil.bak"
	writeFile(t, input, "source")
	writeFile(t, candidate, "encoded")
	writeFile(t, backup, "previous-backup")

	if _, err := (Manager{}).Replace(context.Background(), input, candidate, domain.ReplacementModeReplace); err == nil {
		t.Fatal("Replace() error = nil, want existing backup refusal")
	}
	if got := readFile(t, backup); got != "previous-backup" {
		t.Fatalf("backup content = %q, want previous-backup", got)
	}
	if got := readFile(t, input); got != "source" {
		t.Fatalf("input content = %q, want source", got)
	}
}

func TestHandoffRefusesExistingDestination(t *testing.T) {
	root := t.TempDir()
	imports := t.TempDir()
	input := filepath.Join(root, "Movie.mkv")
	candidate := filepath.Join(t.TempDir(), "output.mkv")
	destination := filepath.Join(imports, "Movie.mkv")
	writeFile(t, input, "source")
	writeFile(t, candidate, "encoded")
	writeFile(t, destination, "existing")

	job := &pipeline.JobContext{
		Source: domain.MediaSource{
			Kind:         domain.SourceKindFile,
			RelativePath: "Movie.mkv",
		},
		Library: domain.Library{
			Kind: domain.LibraryKindDownload,
			Path: root,
			Download: domain.DownloadLibraryPolicy{
				HandoffPath:          imports,
				HandoffMode:          domain.HandoffModeCopy,
				PreserveRelativePath: true,
			},
		},
		InputPath:  input,
		OutputPath: candidate,
	}

	if _, err := (Manager{}).Handoff(context.Background(), job); err == nil {
		t.Fatal("Handoff() error = nil, want existing destination refusal")
	}
	if got := readFile(t, destination); got != "existing" {
		t.Fatalf("destination content = %q, want existing", got)
	}
}

func TestHandoffMoveRefusesExistingDestination(t *testing.T) {
	root := t.TempDir()
	imports := t.TempDir()
	input := filepath.Join(root, "Movie.mkv")
	candidate := filepath.Join(t.TempDir(), "output.mkv")
	destination := filepath.Join(imports, "Movie.mkv")
	writeFile(t, input, "source")
	writeFile(t, candidate, "encoded")
	writeFile(t, destination, "existing")

	job := &pipeline.JobContext{
		Source: domain.MediaSource{
			Kind:         domain.SourceKindFile,
			RelativePath: "Movie.mkv",
		},
		Library: domain.Library{
			Kind: domain.LibraryKindDownload,
			Path: root,
			Download: domain.DownloadLibraryPolicy{
				HandoffPath:          imports,
				HandoffMode:          domain.HandoffModeMove,
				PreserveRelativePath: true,
			},
		},
		InputPath:  input,
		OutputPath: candidate,
	}

	if _, err := (Manager{}).Handoff(context.Background(), job); err == nil {
		t.Fatal("Handoff() error = nil, want existing destination refusal")
	}
	if got := readFile(t, destination); got != "existing" {
		t.Fatalf("destination content = %q, want existing", got)
	}
	if got := readFile(t, candidate); got != "encoded" {
		t.Fatalf("candidate content = %q, want encoded", got)
	}
}

func writeFile(t *testing.T, path string, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write %q: %v", path, err)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %q: %v", path, err)
	}
	return string(data)
}
