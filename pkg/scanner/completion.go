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
	absPath, ok := completionPath(path)
	if t == nil || !ok {
		return
	}

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

// Invalidate removes the completion confidence for path. Filesystem event
// processing calls it in event-sequence order when a new write may have begun.
func (t *CompletionTracker) Invalidate(path string) {
	absPath, ok := completionPath(path)
	if t == nil || !ok {
		return
	}

	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.marks, absPath)
}

// Reset removes all completion confidence after filesystem event ordering is
// lost, such as when the inotify queue overflows.
func (t *CompletionTracker) Reset() {
	if t == nil {
		return
	}

	t.mu.Lock()
	defer t.mu.Unlock()
	t.marks = nil
	t.pruneAt = time.Time{}
}

// CompletedSince reports whether path has a completion mark at or after its
// current modification time. Explicit mutation invalidation preserves event
// ordering; the modification-time check is an additional defense.
func (t *CompletionTracker) CompletedSince(path string, modTime time.Time) bool {
	absPath, pathOK := completionPath(path)
	if t == nil || !pathOK {
		return false
	}

	t.mu.Lock()
	defer t.mu.Unlock()
	markedAt, marked := t.marks[absPath]
	return marked && !modTime.After(markedAt)
}

func completionPath(path string) (string, bool) {
	if path == "" {
		return "", false
	}
	absPath, err := filepath.Abs(path)
	if err != nil {
		return "", false
	}
	return filepath.Clean(absPath), true
}
