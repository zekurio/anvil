//go:build linux

package scanner

import (
	"path/filepath"
	"time"
)

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
