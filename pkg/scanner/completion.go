package scanner

import (
	"path/filepath"
	"sync"
	"time"
)

const (
	completionMarkLimit = 64 * 1024
	completionMarkTTL   = 24 * time.Hour
)

// CompletionTracker records filesystem completion signals for scanner walks.
// Its zero value is ready for use.
type CompletionTracker struct {
	mu      sync.Mutex
	marks   map[string]time.Time
	pruneAt time.Time
}

// Mark records that a writer completed or moved path into a watched library.
func (t *CompletionTracker) Mark(path string, at time.Time) {
	if t == nil || path == "" {
		return
	}
	absPath, err := filepath.Abs(path)
	if err != nil {
		return
	}
	absPath = filepath.Clean(absPath)

	t.mu.Lock()
	defer t.mu.Unlock()
	if t.marks == nil {
		t.marks = make(map[string]time.Time)
	}

	if t.pruneAt.IsZero() || !at.Before(t.pruneAt) || len(t.marks) >= completionMarkLimit {
		cutoff := at.Add(-completionMarkTTL)
		t.pruneAt = at.Add(completionMarkTTL)
		for markedPath, markedAt := range t.marks {
			if markedAt.After(cutoff) {
				expiresAt := markedAt.Add(completionMarkTTL)
				if expiresAt.Before(t.pruneAt) {
					t.pruneAt = expiresAt
				}
				continue
			}
			delete(t.marks, markedPath)
		}
	}

	if _, exists := t.marks[absPath]; !exists && len(t.marks) >= completionMarkLimit {
		var oldestPath string
		var oldestAt time.Time
		for markedPath, markedAt := range t.marks {
			if oldestAt.IsZero() || markedAt.Before(oldestAt) {
				oldestPath = markedPath
				oldestAt = markedAt
			}
		}
		delete(t.marks, oldestPath)
	}
	t.marks[absPath] = at
	expiresAt := at.Add(completionMarkTTL)
	if t.pruneAt.IsZero() || expiresAt.Before(t.pruneAt) {
		t.pruneAt = expiresAt
	}
}

// CompletedSince reports whether path has a completion mark at or after its
// current modification time. A later write therefore invalidates an earlier
// close signal.
func (t *CompletionTracker) CompletedSince(path string, modTime time.Time) bool {
	if t == nil || path == "" {
		return false
	}
	absPath, err := filepath.Abs(path)
	if err != nil {
		return false
	}
	absPath = filepath.Clean(absPath)

	t.mu.Lock()
	defer t.mu.Unlock()
	markedAt, ok := t.marks[absPath]
	return ok && !modTime.After(markedAt)
}
