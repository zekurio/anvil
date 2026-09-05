package scanner

import (
	"container/heap"
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
	generation uint64
	mu         sync.Mutex
	marks      map[string]*completionMark
	oldest     completionHeap
}

// Mark records that a writer completed or moved path into a watched library.
func (t *CompletionTracker) Mark(path string, at time.Time) { t.mark(path, at, false) }

// MarkDirectory records a moved-in tree without walking it. A later mutation
// invalidates its ancestor marks before scanners can use that confidence.
func (t *CompletionTracker) MarkDirectory(path string, at time.Time) { t.mark(path, at, true) }

func (t *CompletionTracker) mark(path string, at time.Time, directory bool) {
	absPath, ok := completionPath(path)
	if t == nil || !ok {
		return
	}

	if directory {
		absPath += string(filepath.Separator)
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.markLocked(absPath, at)
}

func (t *CompletionTracker) markLocked(absPath string, at time.Time) {
	if t.marks == nil {
		t.marks = make(map[string]*completionMark)
	}
	cutoff := at.Add(-completionMarkTTL)
	for len(t.oldest) > 0 && !t.oldest[0].at.After(cutoff) {
		t.removeOldest()
	}
	if mark, ok := t.marks[absPath]; ok {
		mark.at = at
		heap.Fix(&t.oldest, mark.index)
		return
	}
	if len(t.marks) >= completionMarkLimit {
		t.removeOldest()
	}
	mark := &completionMark{path: absPath, at: at}
	t.marks[absPath] = mark
	heap.Push(&t.oldest, mark)
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
	t.generation++
	t.removeMark(absPath)
	for parent := absPath; ; parent = filepath.Dir(parent) {
		t.removeMark(parent + string(filepath.Separator))
		if filepath.Dir(parent) == parent {
			break
		}
	}
}

// Reset removes all completion confidence after filesystem event ordering is
// lost, such as when the inotify queue overflows.
func (t *CompletionTracker) Reset() {
	if t == nil {
		return
	}

	t.mu.Lock()
	defer t.mu.Unlock()
	t.generation++
	t.marks = nil
	t.oldest = nil
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
	if marked && !modTime.After(markedAt.at) {
		return true
	}
	for parent := filepath.Dir(absPath); ; parent = filepath.Dir(parent) {
		if mark, ok := t.marks[parent+string(filepath.Separator)]; ok && !modTime.After(mark.at) {
			return true
		}
		if filepath.Dir(parent) == parent {
			break
		}
	}
	return false
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

// The heap has one entry per path, including after repeated marks.
type completionMark struct {
	path  string
	at    time.Time
	index int
}
type completionHeap []*completionMark

func (h completionHeap) Len() int           { return len(h) }
func (h completionHeap) Less(i, j int) bool { return h[i].at.Before(h[j].at) }
func (h completionHeap) Swap(i, j int)      { h[i], h[j] = h[j], h[i]; h[i].index = i; h[j].index = j }
func (h *completionHeap) Push(value any) {
	mark := value.(*completionMark)
	mark.index = len(*h)
	*h = append(*h, mark)
}
func (h *completionHeap) Pop() any {
	old := *h
	mark := old[len(old)-1]
	old[len(old)-1] = nil
	*h = old[:len(old)-1]
	return mark
}
func (t *CompletionTracker) removeOldest() {
	mark := heap.Pop(&t.oldest).(*completionMark)
	delete(t.marks, mark.path)
}

func (t *CompletionTracker) removeMark(path string) {
	if mark, ok := t.marks[path]; ok {
		heap.Remove(&t.oldest, mark.index)
		delete(t.marks, path)
	}
}

func (t *CompletionTracker) currentGeneration() uint64 {
	if t == nil {
		return 0
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.generation
}

func (t *CompletionTracker) completeDirectory(path string, at time.Time, generation uint64) bool {
	if t == nil {
		return false
	}
	absPath, ok := completionPath(path)
	if !ok {
		return false
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.generation != generation {
		return false
	}
	t.markLocked(absPath+string(filepath.Separator), at)
	return true
}
