package resources

import "testing"

func TestAllocatorSplitsThreadsAcrossActiveWorkers(t *testing.T) {
	allocator := NewAllocator(8)

	if got := allocator.Allocate("worker-1", 1).Threads; got != 8 {
		t.Fatalf("threads with 1 active worker = %d, want 8", got)
	}
	if got := allocator.Allocate("worker-1", 3).Threads; got != 2 {
		t.Fatalf("threads with 3 active workers = %d, want 2", got)
	}
	if got := allocator.Allocate("worker-1", 20).Threads; got != 1 {
		t.Fatalf("threads with 20 active workers = %d, want 1", got)
	}
}
