package ffmpeg

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/zekurio/anvil/pkg/domain"
	"github.com/zekurio/anvil/pkg/pipeline"
	"github.com/zekurio/anvil/pkg/process"
	"github.com/zekurio/anvil/pkg/staging"
)

type destinationRunner func(context.Context, process.Command) (process.Result, error)

func (f destinationRunner) Run(ctx context.Context, command process.Command) (process.Result, error) {
	return f(ctx, command)
}

func TestBlockCreatesHandoffDirectoryBeforeEncode(t *testing.T) {
	for _, blocked := range []bool{false, true} {
		name := "missing directory"
		if blocked {
			name = "file blocks directory"
		}
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			job := &pipeline.JobContext{
				Job: domain.Job{ID: 42},
				Library: domain.Library{
					Kind: domain.LibraryKindDownload,
					Download: domain.DownloadLibraryPolicy{
						HandoffPath: filepath.Join(root, "handoff"),
					},
				},
				InputPath: filepath.Join(root, "episode.mkv"),
				Profile:   domain.Profile{Container: "mkv"},
			}
			if err := os.WriteFile(job.InputPath, []byte("source"), 0o600); err != nil {
				t.Fatal(err)
			}
			stage := staging.StageBlock{Manager: staging.Manager{Root: filepath.Join(root, "scratch")}}
			if err := stage.Run(context.Background(), job); err != nil {
				t.Fatal(err)
			}
			destination, output := job.DestinationPath, job.OutputPath
			dir := filepath.Dir(destination)
			if _, err := os.Stat(dir); !os.IsNotExist(err) {
				t.Fatalf("planning changed the handoff directory: %v", err)
			}
			if blocked {
				if err := os.WriteFile(dir, []byte("keep"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			called := false
			block := Block{Encoder: Encoder{Runner: destinationRunner(func(_ context.Context, command process.Command) (process.Result, error) {
				called = true
				info, err := os.Stat(dir)
				if err != nil {
					t.Fatal(err)
				}
				if info.Mode().Perm() != 0o775 {
					t.Fatalf("handoff directory mode = %o, want 775", info.Mode().Perm())
				}
				if got := command.Args[len(command.Args)-1]; got != output {
					t.Fatalf("output = %q, want %q", got, output)
				}
				return process.Result{}, os.WriteFile(output, []byte("encoded"), 0o600)
			})}}
			err := block.Run(context.Background(), job)
			if blocked {
				if err == nil || called {
					t.Fatalf("blocked directory: error = %v, runner called = %t", err, called)
				}
				data, readErr := os.ReadFile(dir)
				if readErr != nil || string(data) != "keep" {
					t.Fatalf("blocking file changed: %q, %v", data, readErr)
				}
				return
			}
			if err != nil || !called {
				t.Fatalf("restored directory: error = %v, runner called = %t", err, called)
			}
			if job.DestinationPath != destination || job.OutputPath != output {
				t.Fatal("planned paths changed")
			}
			data, err := os.ReadFile(job.InputPath)
			if err != nil || string(data) != "source" {
				t.Fatalf("source changed: %q, %v", data, err)
			}
		})
	}
}
