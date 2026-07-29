package main

import (
	"context"
	"log/slog"
	"net"
	"sync"
	"time"

	"github.com/zekurio/anvil/pkg/config"
	"github.com/zekurio/anvil/pkg/controlapi"
	"github.com/zekurio/anvil/pkg/domain"
	"github.com/zekurio/anvil/pkg/scanner"
	"github.com/zekurio/anvil/pkg/store"
)

// controlServiceDeps is the live daemon state the control surface exposes. The
// scheduler accessors are functions because the scheduler starts after the
// socket is claimed, and claiming the socket first is what keeps a second
// daemon from touching the store at all.
type controlServiceDeps struct {
	store         *store.SQLiteStore
	config        func() config.Config
	startedAt     time.Time
	activeWorkers func() int
	cancelJob     func(domain.JobID) bool
}

// startControlService answers control commands on an already-claimed listener.
// The returned channel reports a control-plane failure that should stop the
// daemon; an orderly shutdown closes it without a value.
func startControlService(ctx context.Context, wg *sync.WaitGroup, listener net.Listener, deps controlServiceDeps) <-chan error {
	server := controlapi.Server{Service: controlapi.Service{
		Store:            deps.store,
		Scanner:          scanner.Scanner{Store: deps.store},
		Config:           deps.config,
		ActiveWorkers:    deps.activeWorkers,
		CancelRunningJob: deps.cancelJob,
		StartedAt:        deps.startedAt,
		DaemonVersion:    controlapi.BuildVersion,
	}}
	failures := make(chan error, 1)
	wg.Add(1)
	go func() {
		defer wg.Done()
		defer close(failures)
		if err := server.Serve(ctx, listener); err != nil {
			failures <- err
		}
	}()
	slog.Info("control service listening",
		"socket", listener.Addr().String(),
		"api_version", controlapi.Version,
		"protocol_version", controlapi.ProtocolVersion,
	)
	return failures
}
