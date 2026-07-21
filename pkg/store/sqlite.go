package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/zekurio/anvil/pkg/domain"
	_ "modernc.org/sqlite"
)

var (
	ErrNotFound           = errors.New("store: not found")
	ErrIncompatibleSchema = errors.New("store: incompatible database schema; reset the database before starting Anvil")
)

type SQLiteStore struct {
	db *sql.DB
}

type EnqueueJobInput struct {
	SourceID    domain.MediaSourceID
	AssetID     domain.MediaAssetID
	LibraryName domain.LibraryName
	Priority    int
	Now         time.Time
}

type ScanToken struct {
	LibraryName domain.LibraryName
	Sequence    int64
}

type ScanEntry struct {
	SourceKind         domain.SourceKind
	SourceRelativePath string
	SourceFingerprint  domain.FileFingerprint
	AssetRelativePath  string
	AssetRole          domain.MediaAssetRole
	AssetFingerprint   domain.FileFingerprint
	Persist            bool
	Enqueue            bool
}

type ApplyScanInput struct {
	LibraryName domain.LibraryName
	Priority    int
	Entries     []ScanEntry
	CompletedAt time.Time
}

type ApplyScanResult struct {
	Applied      bool
	Sources      int
	Assets       int
	EnqueuedJobs int
	ExistingJobs int
}

type ForceOccurrenceInput struct {
	LibraryName        domain.LibraryName
	SourceKind         domain.SourceKind
	SourceRelativePath string
	SourceFingerprint  domain.FileFingerprint
	AssetRelativePath  string
	AssetRole          domain.MediaAssetRole
	AssetFingerprint   domain.FileFingerprint
	Priority           int
	Now                time.Time
}

type ForceOccurrenceResult struct {
	Source domain.MediaSource
	Asset  domain.MediaAsset
	Job    domain.Job
}

type CompleteJobOccurrenceInput struct {
	JobID                 domain.JobID
	AttemptID             domain.AttemptID
	InputSizeBytes        int64
	OutputSizeBytes       int64
	SourceMediaRemoved    bool
	FinalInputFingerprint *domain.FileFingerprint
	CompletedAt           time.Time
}

type JobListFilter struct {
	LibraryName domain.LibraryName
	States      []domain.JobState
	Limit       int
}

type LibraryStatsFilter struct {
	LibraryName domain.LibraryName
}

type LibraryStats struct {
	LibraryName     domain.LibraryName `json:"library_name"`
	Jobs            int64              `json:"jobs"`
	InputSizeBytes  int64              `json:"input_size_bytes"`
	OutputSizeBytes int64              `json:"output_size_bytes"`
	SavedBytes      int64              `json:"saved_bytes"`
	SavedPercent    float64            `json:"saved_percent"`
}

type JobSummary struct {
	Job        domain.Job
	SourceKind domain.SourceKind
	SourcePath string
	AssetPath  string
	AssetRole  domain.MediaAssetRole
}

func Open(ctx context.Context, path string) (*SQLiteStore, error) {
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("store path is required")
	}
	if err := ensureParentDir(path); err != nil {
		return nil, err
	}

	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open sqlite store: %w", err)
	}
	db.SetMaxOpenConns(1)

	store := &SQLiteStore{db: db}
	if err := store.configure(ctx); err != nil {
		return nil, closeDBOnError(db, err)
	}
	if err := store.migrate(ctx); err != nil {
		return nil, closeDBOnError(db, err)
	}

	return store, nil
}

func OpenReadOnly(ctx context.Context, path string) (*SQLiteStore, error) {
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("store path is required")
	}
	if path == ":memory:" {
		return nil, ErrNotFound
	}
	if !strings.HasPrefix(path, "file:") {
		if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
			return nil, ErrNotFound
		} else if err != nil {
			return nil, fmt.Errorf("stat sqlite store %q: %w", path, err)
		}
	}

	db, err := sql.Open("sqlite", readOnlyDSN(path))
	if err != nil {
		return nil, fmt.Errorf("open read-only sqlite store: %w", err)
	}
	db.SetMaxOpenConns(1)

	store := &SQLiteStore{db: db}
	if err := store.configureReadOnly(ctx); err != nil {
		return nil, closeDBOnError(db, err)
	}
	compatible, exists, err := store.schemaCompatibility(ctx)
	if err != nil {
		return nil, closeDBOnError(db, err)
	}
	if !exists || !compatible {
		return nil, closeDBOnError(db, ErrIncompatibleSchema)
	}
	return store, nil
}

func (s *SQLiteStore) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

func closeDBOnError(db *sql.DB, cause error) error {
	if closeErr := db.Close(); closeErr != nil {
		return errors.Join(cause, fmt.Errorf("close sqlite store after error: %w", closeErr))
	}
	return cause
}
