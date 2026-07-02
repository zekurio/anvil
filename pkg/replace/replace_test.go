package replace

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"syscall"
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

func TestHandoffPublishesImportFriendlyModes(t *testing.T) {
	oldUmask := syscall.Umask(0o077)
	t.Cleanup(func() {
		syscall.Umask(oldUmask)
	})

	root := t.TempDir()
	imports := filepath.Join(t.TempDir(), "imports")
	input := filepath.Join(root, "SomeShowS01", "SomeShowS01E01", "episode_1.mkv")
	candidate := filepath.Join(t.TempDir(), "output.mkv")
	writeFile(t, input, "source")
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
			},
		},
		InputPath:  input,
		OutputPath: candidate,
	}

	finalPath, err := (Manager{}).Handoff(context.Background(), job)
	if err != nil {
		t.Fatalf("Handoff() error = %v", err)
	}
	assertHandoffDirMode(t, imports)
	assertHandoffDirMode(t, filepath.Join(imports, "SomeShowS01"))
	assertHandoffDirMode(t, filepath.Join(imports, "SomeShowS01", "SomeShowS01E01"))
	assertMode(t, finalPath, 0o664)
	if got := readFile(t, finalPath); got != "encoded" {
		t.Fatalf("final content = %q, want encoded", got)
	}
}

func TestPlanReplacementCopyAndReplace(t *testing.T) {
	copyPlan, err := PlanReplacement("/media/movie.mp4", "/tmp/output.mkv", domain.ReplacementModeCopy)
	if err != nil {
		t.Fatalf("PlanReplacement(copy) error = %v", err)
	}
	if copyPlan.Action != "copy" || copyPlan.CopyPath != "/media/movie.anvil.mkv" {
		t.Fatalf("copy plan = %+v, want copy path", copyPlan)
	}

	replace, err := PlanReplacement("/media/movie.mp4", "/tmp/output.mkv", domain.ReplacementModeReplace)
	if err != nil {
		t.Fatalf("PlanReplacement(replace) error = %v", err)
	}
	if replace.Action != "replace" || replace.ReplaceTarget != "/media/movie.mkv" || replace.BackupPath != "/media/movie.mp4.anvil.bak" {
		t.Fatalf("replace plan = %+v, want target and backup", replace)
	}
}

func TestIsAnvilCopyOutputPath(t *testing.T) {
	tests := []struct {
		name string
		path string
		want bool
	}{
		{name: "mkv copy output", path: "Movie.anvil.mkv", want: true},
		{name: "case insensitive", path: "Movie.ANVIL.mkv", want: true},
		{name: "nested path", path: filepath.Join("Season", "Episode.anvil.mp4"), want: true},
		{name: "title contains anvil", path: "The.Anvil.2020.mkv", want: false},
		{name: "no extension", path: "Movie.anvil", want: false},
		{name: "backup", path: "Movie.mkv.anvil.bak", want: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsAnvilCopyOutputPath(tt.path); got != tt.want {
				t.Fatalf("IsAnvilCopyOutputPath(%q) = %v, want %v", tt.path, got, tt.want)
			}
		})
	}
}

func TestPlanHandoffDestinationAndCleanup(t *testing.T) {
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
			Path: "/downloads",
			Download: domain.DownloadLibraryPolicy{
				HandoffPath:          "/imports/tv",
				HandoffMode:          domain.HandoffModeMove,
				PreserveRelativePath: true,
				CleanupSourceMedia:   true,
				PruneEmptyDirs:       true,
			},
		},
		InputPath:  "/downloads/SomeShowS01/SomeShowS01E01/episode_1.mkv",
		OutputPath: "/tmp/staging/output.mkv",
	}
	plan, err := PlanHandoff(job)
	if err != nil {
		t.Fatalf("PlanHandoff() error = %v", err)
	}
	wantDestination := filepath.Join("/imports/tv", "SomeShowS01", "SomeShowS01E01", "episode_1.mkv")
	if plan.Destination != wantDestination || plan.Action != "move" {
		t.Fatalf("handoff plan = %+v, want move to %q", plan, wantDestination)
	}
	if !plan.CleanupSourceMedia || !plan.PruneEmptyDirs || plan.PruneStart != filepath.Dir(job.InputPath) {
		t.Fatalf("cleanup plan = %+v, want source cleanup and prune start", plan)
	}
}

func TestReplaceCopyLeavesInputInPlace(t *testing.T) {
	dir := t.TempDir()
	input := filepath.Join(dir, "movie.mkv")
	candidate := filepath.Join(dir, "candidate.mkv")
	writeFile(t, input, "source")
	writeFile(t, candidate, "encoded")

	finalPath, err := (Manager{}).Replace(context.Background(), input, candidate, domain.ReplacementModeCopy)
	if err != nil {
		t.Fatalf("Replace() error = %v", err)
	}
	if finalPath == input {
		t.Fatalf("copy final path = input path %q", input)
	}
	if got := readFile(t, input); got != "source" {
		t.Fatalf("input content = %q, want source", got)
	}
	if got := readFile(t, finalPath); got != "encoded" {
		t.Fatalf("copy content = %q, want encoded", got)
	}
}

func TestReplaceCopyRefusesExistingDestination(t *testing.T) {
	dir := t.TempDir()
	input := filepath.Join(dir, "movie.mkv")
	candidate := filepath.Join(dir, "candidate.mkv")
	copyPath := filepath.Join(dir, "movie.anvil.mkv")
	writeFile(t, input, "source")
	writeFile(t, candidate, "encoded")
	writeFile(t, copyPath, "existing")

	if _, err := (Manager{}).Replace(context.Background(), input, candidate, domain.ReplacementModeCopy); err == nil {
		t.Fatal("Replace() error = nil, want existing copy refusal")
	}
	if got := readFile(t, copyPath); got != "existing" {
		t.Fatalf("copy content = %q, want existing", got)
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

func TestReplaceRenamesNonMKVSourceToMKVTarget(t *testing.T) {
	dir := t.TempDir()
	input := filepath.Join(dir, "movie.mp4")
	candidate := filepath.Join(dir, "candidate.mkv")
	writeFile(t, input, "source")
	writeFile(t, candidate, "encoded")

	finalPath, err := (Manager{}).Replace(context.Background(), input, candidate, domain.ReplacementModeReplace)
	if err != nil {
		t.Fatalf("Replace() error = %v", err)
	}
	wantFinal := filepath.Join(dir, "movie.mkv")
	if finalPath != wantFinal {
		t.Fatalf("final path = %q, want %q", finalPath, wantFinal)
	}
	if _, err := os.Stat(input); !os.IsNotExist(err) {
		t.Fatalf("input stat = %v, want old non-MKV path removed", err)
	}
	if got := readFile(t, finalPath); got != "encoded" {
		t.Fatalf("final content = %q, want encoded", got)
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

func TestPruneEmptyDirsRejectsSymlinkStartOutsideRoot(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	outsideEpisode := filepath.Join(outside, "episode")
	ignorable := filepath.Join(outsideEpisode, ".nfs123")
	writeFile(t, ignorable, "lock")

	link := filepath.Join(root, "linked-episode")
	if err := os.Symlink(outsideEpisode, link); err != nil {
		t.Skipf("create symlink: %v", err)
	}

	err := PruneEmptyDirs(root, link, []string{"**/.nfs*"})
	if err == nil {
		t.Fatal("PruneEmptyDirs() error = nil, want symlink escape refusal")
	}
	if got := readFile(t, ignorable); got != "lock" {
		t.Fatalf("outside ignorable content = %q, want lock", got)
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

func assertMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %q: %v", path, err)
	}
	got := info.Mode() & (os.ModeSetgid | os.ModePerm)
	if got != want {
		t.Fatalf("mode %q = %v, want %v", path, got, want)
	}
}

func assertHandoffDirMode(t *testing.T, path string) {
	t.Helper()
	want := os.FileMode(0o775)
	if runtime.GOOS == "linux" {
		want |= os.ModeSetgid
	}
	assertMode(t, path, want)
}
