package replace

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/zekurio/anvil/pkg/domain"
	"github.com/zekurio/anvil/pkg/pipeline"
)

type memoryJournal struct {
	operations map[domain.JobID]PublishOperation
	failUpdate PublishStage
}

func (j *memoryJournal) GetPublishOperation(_ context.Context, jobID domain.JobID) (PublishOperation, bool, error) {
	op, ok := j.operations[jobID]
	return op, ok, nil
}

func (j *memoryJournal) CreatePublishOperation(_ context.Context, operation PublishOperation) error {
	if j.operations == nil {
		j.operations = make(map[domain.JobID]PublishOperation)
	}
	if _, exists := j.operations[operation.JobID]; exists {
		return errors.New("operation exists")
	}
	j.operations[operation.JobID] = operation
	return nil
}

func (j *memoryJournal) UpdatePublishOperation(_ context.Context, operation PublishOperation, previous PublishStage) error {
	if operation.Stage == j.failUpdate {
		j.failUpdate = ""
		return errors.New("injected journal update failure")
	}
	current, ok := j.operations[operation.JobID]
	if !ok || current.Stage != previous {
		return errors.New("unexpected previous stage")
	}
	j.operations[operation.JobID] = operation
	return nil
}

func testManager() Manager {
	return Manager{Journal: &memoryJournal{operations: make(map[domain.JobID]PublishOperation)}}
}

func TestPublishRecoveryAcrossFilesystemBoundaries(t *testing.T) {
	boundaries := []PublishBoundary{
		BoundaryPrepared,
		BoundaryDestinationCreated,
		BoundaryPublished,
		BoundaryDestinationMode,
		BoundaryArtifactRemoved,
		BoundarySourceRemoved,
		BoundaryDirectoriesPruned,
		BoundarySourceCleaned,
		BoundaryCommitted,
	}
	for _, boundary := range boundaries {
		t.Run(string(boundary), func(t *testing.T) {
			job := handoffTestJob(t, domain.HandoffModeMove, true, true)
			journal := &memoryJournal{operations: make(map[domain.JobID]PublishOperation)}
			crashed := false
			manager := Manager{
				Journal: journal,
				Hook: func(got PublishBoundary) error {
					if got == boundary && !crashed {
						crashed = true
						return errors.New("injected crash")
					}
					return nil
				},
			}
			if _, err := manager.Handoff(context.Background(), job); !errors.Is(err, ErrPublishPending) {
				t.Fatalf("Handoff() error = %v, want pending recovery", err)
			}
			if !crashed {
				t.Fatalf("boundary %q was not reached", boundary)
			}
			recovered, err := manager.Recover(context.Background(), &pipeline.JobContext{Job: job.Job})
			if err != nil {
				t.Fatalf("Recover() error = %v", err)
			}
			if !recovered {
				t.Fatal("Recover() recovered = false, want true")
			}
			op := journal.operations[job.Job.ID]
			if op.Stage != PublishStageCommitted {
				t.Fatalf("stage = %q, want committed", op.Stage)
			}
			if got := readFile(t, op.DestinationPath); got != "encoded-output" {
				t.Fatalf("destination = %q, want encoded output", got)
			}
			if _, err := os.Stat(op.ArtifactPath); !os.IsNotExist(err) {
				t.Fatalf("artifact stat = %v, want removed", err)
			}
			if _, err := os.Stat(op.CleanupSourcePath); !os.IsNotExist(err) {
				t.Fatalf("source stat = %v, want removed", err)
			}
		})
	}
}

func TestCrossFilesystemMoveRecoversAfterPublicationBeforeJournalUpdate(t *testing.T) {
	job := handoffTestJob(t, domain.HandoffModeMove, false, false)
	journal := &memoryJournal{
		operations: make(map[domain.JobID]PublishOperation),
		failUpdate: PublishStagePublished,
	}
	digestCalls := 0
	manager := Manager{
		Journal: journal,
		LinkArtifact: func(_, _ string) error {
			return syscall.EXDEV
		},
		DigestFile: func(ctx context.Context, path string) (string, error) {
			digestCalls++
			return (Manager{}).digest(ctx, path)
		},
	}
	if _, err := manager.Handoff(context.Background(), job); !errors.Is(err, ErrPublishPending) {
		t.Fatalf("Handoff() error = %v, want pending recovery", err)
	}
	op := journal.operations[job.Job.ID]
	if op.Stage != PublishStagePrepared {
		t.Fatalf("stage = %q, want prepared after failed update", op.Stage)
	}
	if _, err := os.Stat(op.ArtifactPath); err != nil {
		t.Fatalf("artifact removed before published journal update: %v", err)
	}
	if digestCalls != 0 {
		t.Fatalf("normal publish digest calls = %d, want 0", digestCalls)
	}
	if _, err := manager.Recover(context.Background(), &pipeline.JobContext{Job: job.Job}); err != nil {
		t.Fatalf("Recover() error = %v", err)
	}
	if digestCalls != 2 {
		t.Fatalf("recovery digest calls = %d, want source and destination", digestCalls)
	}
	if journal.operations[job.Job.ID].DigestValue == "" {
		t.Fatal("recovery digest was not journaled")
	}
}

func TestMatchingExistingDestinationResumesAndConflictIsDistinct(t *testing.T) {
	t.Run("matching", func(t *testing.T) {
		job := handoffTestJob(t, domain.HandoffModeCopy, false, false)
		plan, err := PlanHandoff(job)
		if err != nil {
			t.Fatal(err)
		}
		writeFile(t, plan.Destination, "encoded-output")
		journal := &memoryJournal{operations: make(map[domain.JobID]PublishOperation)}
		manager := Manager{Journal: journal}
		if _, err := manager.Handoff(context.Background(), job); err != nil {
			t.Fatalf("Handoff() error = %v", err)
		}
		if journal.operations[job.Job.ID].Stage != PublishStageCommitted {
			t.Fatalf("stage = %q, want committed", journal.operations[job.Job.ID].Stage)
		}
	})

	t.Run("conflict", func(t *testing.T) {
		job := handoffTestJob(t, domain.HandoffModeMove, true, true)
		writeFile(t, job.OutputPath, "ANVIL-episode-A")
		plan, err := PlanHandoff(job)
		if err != nil {
			t.Fatal(err)
		}
		writeFile(t, plan.Destination, "ANVIL-episode-B")
		journal := &memoryJournal{operations: make(map[domain.JobID]PublishOperation)}
		_, err = (Manager{Journal: journal}).Handoff(context.Background(), job)
		if !errors.Is(err, ErrPublishConflict) {
			t.Fatalf("Handoff() error = %v, want conflict", err)
		}
		op := journal.operations[job.Job.ID]
		if op.Stage != PublishStageConflict {
			t.Fatalf("stage = %q, want conflict", op.Stage)
		}
		if got := readFile(t, plan.Destination); got != "ANVIL-episode-B" {
			t.Fatalf("destination changed to %q", got)
		}
		if got := readFile(t, job.OutputPath); got != "ANVIL-episode-A" {
			t.Fatalf("artifact changed to %q", got)
		}
		if got := readFile(t, job.InputPath); got != "source-media" {
			t.Fatalf("source changed to %q", got)
		}
	})
}

func TestRecoveryToleratesLostStageUpdatesAfterCleanup(t *testing.T) {
	for _, failedStage := range []PublishStage{PublishStageSourceCleaned, PublishStageCommitted} {
		t.Run(string(failedStage), func(t *testing.T) {
			job := handoffTestJob(t, domain.HandoffModeMove, true, true)
			journal := &memoryJournal{
				operations: make(map[domain.JobID]PublishOperation),
				failUpdate: failedStage,
			}
			manager := Manager{Journal: journal}
			if _, err := manager.Handoff(context.Background(), job); !errors.Is(err, ErrPublishPending) {
				t.Fatalf("Handoff() error = %v, want pending recovery", err)
			}
			if failedStage == PublishStageSourceCleaned && journal.operations[job.Job.ID].Stage != PublishStagePublished {
				t.Fatalf("stage = %q, want published", journal.operations[job.Job.ID].Stage)
			}
			if failedStage == PublishStageCommitted && journal.operations[job.Job.ID].Stage != PublishStageSourceCleaned {
				t.Fatalf("stage = %q, want source_cleaned", journal.operations[job.Job.ID].Stage)
			}
			if _, err := os.Stat(job.OutputPath); !os.IsNotExist(err) {
				t.Fatalf("artifact stat = %v, want already removed", err)
			}
			if _, err := os.Stat(job.InputPath); !os.IsNotExist(err) {
				t.Fatalf("source stat = %v, want already removed", err)
			}
			if _, err := manager.Recover(context.Background(), &pipeline.JobContext{Job: job.Job}); err != nil {
				t.Fatalf("Recover() error = %v", err)
			}
			if journal.operations[job.Job.ID].Stage != PublishStageCommitted {
				t.Fatalf("recovered stage = %q, want committed", journal.operations[job.Job.ID].Stage)
			}
		})
	}
}

func TestReplacementRecoversAtEveryMutationBoundary(t *testing.T) {
	boundaries := []PublishBoundary{
		BoundaryPrepared,
		BoundaryBackupLinked,
		BoundaryOriginalBackedUp,
		BoundaryDestinationCreated,
		BoundaryPublished,
		BoundaryArtifactRemoved,
		BoundaryBackupRemoved,
		BoundarySourceCleaned,
		BoundaryCommitted,
	}
	for _, boundary := range boundaries {
		t.Run(string(boundary), func(t *testing.T) {
			dir := t.TempDir()
			input := filepath.Join(dir, "movie.mp4")
			staging := filepath.Join(t.TempDir(), "staging")
			candidate := filepath.Join(staging, "output.mkv")
			writeFile(t, input, "original")
			writeFile(t, candidate, "replacement")
			job := replacementJob(41, input, candidate, domain.ReplacementModeReplace)
			job.StagingDir = staging
			journal := &memoryJournal{operations: make(map[domain.JobID]PublishOperation)}
			crashed := false
			manager := Manager{Journal: journal, Hook: func(got PublishBoundary) error {
				if got == boundary && !crashed {
					crashed = true
					return errors.New("injected crash")
				}
				return nil
			}}
			if _, err := manager.Replace(context.Background(), job); !errors.Is(err, ErrPublishPending) {
				t.Fatalf("Replace() error = %v, want pending", err)
			}
			if !crashed {
				t.Fatalf("boundary %q was not reached", boundary)
			}
			if _, err := manager.Recover(context.Background(), &pipeline.JobContext{Job: job.Job}); err != nil {
				t.Fatalf("Recover() error = %v", err)
			}
			op := journal.operations[job.Job.ID]
			if op.Stage != PublishStageCommitted {
				t.Fatalf("stage = %q, want committed", op.Stage)
			}
			if got := readFile(t, op.DestinationPath); got != "replacement" {
				t.Fatalf("destination = %q, want replacement", got)
			}
			if _, err := os.Stat(op.BackupPath); !os.IsNotExist(err) {
				t.Fatalf("backup stat = %v, want removed", err)
			}
		})
	}
}

func TestReplacementCopyRecoversMatchingDestinationAndRejectsConflict(t *testing.T) {
	for _, tt := range []struct {
		name      string
		existing  string
		wantError bool
	}{
		{name: "matching", existing: "replacement"},
		{name: "conflict", existing: "different", wantError: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			input := filepath.Join(dir, "movie.mkv")
			candidate := filepath.Join(t.TempDir(), "output.mkv")
			writeFile(t, input, "original")
			writeFile(t, candidate, "replacement")
			destination := replacementCopyPath(input, ".mkv")
			writeFile(t, destination, tt.existing)
			job := replacementJob(77, input, candidate, domain.ReplacementModeCopy)
			journal := &memoryJournal{operations: make(map[domain.JobID]PublishOperation)}
			_, err := (Manager{Journal: journal}).Replace(context.Background(), job)
			if tt.wantError && !errors.Is(err, ErrPublishConflict) {
				t.Fatalf("Replace() error = %v, want conflict", err)
			}
			if !tt.wantError && err != nil {
				t.Fatalf("Replace() error = %v", err)
			}
			if got := readFile(t, destination); got != tt.existing {
				t.Fatalf("destination = %q, want unchanged %q", got, tt.existing)
			}
		})
	}
}

func TestReplacementMoveFallsBackAcrossFilesystems(t *testing.T) {
	dir := t.TempDir()
	input := filepath.Join(dir, "movie.mp4")
	candidate := filepath.Join(t.TempDir(), "output.mkv")
	writeFile(t, input, "original")
	writeFile(t, candidate, "replacement")
	job := replacementJob(88, input, candidate, domain.ReplacementModeReplace)
	journal := &memoryJournal{operations: make(map[domain.JobID]PublishOperation)}
	manager := Manager{
		Journal: journal,
		LinkArtifact: func(_, _ string) error {
			return syscall.EXDEV
		},
	}
	finalPath, err := manager.Replace(context.Background(), job)
	if err != nil {
		t.Fatalf("Replace() error = %v", err)
	}
	if got := readFile(t, finalPath); got != "replacement" {
		t.Fatalf("destination = %q, want replacement", got)
	}
	if _, err := os.Stat(candidate); !os.IsNotExist(err) {
		t.Fatalf("candidate stat = %v, want removed after commit", err)
	}
	if journal.operations[job.Job.ID].Stage != PublishStageCommitted {
		t.Fatalf("stage = %q, want committed", journal.operations[job.Job.ID].Stage)
	}
}

func handoffTestJob(t *testing.T, mode domain.HandoffMode, cleanup, prune bool) *pipeline.JobContext {
	t.Helper()
	root := t.TempDir()
	imports := t.TempDir()
	input := filepath.Join(root, "Show", "Episode", "episode.mkv")
	staging := filepath.Join(t.TempDir(), "staging")
	output := filepath.Join(staging, "output.mkv")
	writeFile(t, input, "source-media")
	writeFile(t, output, "encoded-output")
	return &pipeline.JobContext{
		Job: domain.Job{ID: 23},
		Source: domain.MediaSource{
			Kind:         domain.SourceKindPackage,
			RelativePath: "Show",
		},
		Asset: domain.MediaAsset{RelativePath: "Episode/episode.mkv"},
		Library: domain.Library{
			Kind: domain.LibraryKindDownload,
			Path: root,
			Download: domain.DownloadLibraryPolicy{
				HandoffPath:          imports,
				HandoffMode:          mode,
				PreserveRelativePath: true,
				CleanupSourceMedia:   cleanup,
				PruneEmptyDirs:       prune,
			},
		},
		InputPath:  input,
		OutputPath: output,
		StagingDir: staging,
	}
}

func (j *memoryJournal) String() string {
	return fmt.Sprint(j.operations)
}
