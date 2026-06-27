package staging

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/zekurio/anvil/pkg/domain"
	"github.com/zekurio/anvil/pkg/pipeline"
)

func TestManagerPrepareCreatesOutputPath(t *testing.T) {
	root := t.TempDir()
	job := &pipeline.JobContext{
		Job:       domain.Job{ID: 12},
		Attempt:   domain.Attempt{ID: 3},
		Profile:   domain.Profile{Container: "mkv"},
		InputPath: "/media/movie.mp4",
	}
	if err := (Manager{Root: root}).Prepare(job); err != nil {
		t.Fatalf("Prepare() error = %v", err)
	}
	if job.StagingDir == "" || job.OutputPath == "" {
		t.Fatalf("staging dir/output path were not set: %+v", job)
	}
	if _, err := os.Stat(job.StagingDir); err != nil {
		t.Fatalf("staging dir stat: %v", err)
	}
	if filepath.Ext(job.OutputPath) != ".mkv" {
		t.Fatalf("output path = %q, want .mkv extension", job.OutputPath)
	}
}
