package resources

import (
	"runtime"

	"github.com/zekurio/anvil/pkg/domain"
)

type Allocator struct {
	TotalThreads int
}

func NewAllocator(totalThreads int) Allocator {
	if totalThreads < 1 {
		totalThreads = runtime.NumCPU()
	}
	if totalThreads < 1 {
		totalThreads = 1
	}
	return Allocator{TotalThreads: totalThreads}
}

func (a Allocator) Allocate(workerID string, activeWorkers int) domain.ResourceAllocation {
	if activeWorkers < 1 {
		activeWorkers = 1
	}
	threads := a.TotalThreads / activeWorkers
	if threads < 1 {
		threads = 1
	}
	return domain.ResourceAllocation{
		WorkerID: workerID,
		Threads:  threads,
	}
}
