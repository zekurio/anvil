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
	return a.AllocateFrom(workerID, a.TotalThreads, activeWorkers)
}

func (a Allocator) AllocateFrom(workerID string, threads int, workers int) domain.ResourceAllocation {
	if workers < 1 {
		workers = 1
	}
	allocated := threads / workers
	if allocated < 1 {
		allocated = 1
	}
	return domain.ResourceAllocation{
		WorkerID: workerID,
		Threads:  allocated,
	}
}

func (a Allocator) AvailableThreads(allocatedThreads int) int {
	if allocatedThreads < 0 {
		allocatedThreads = 0
	}
	available := a.TotalThreads - allocatedThreads
	if available < 0 {
		return 0
	}
	return available
}
