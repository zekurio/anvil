package main

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/zekurio/anvil/pkg/config"
	"github.com/zekurio/anvil/pkg/controlapi"
	"github.com/zekurio/anvil/pkg/store"
)

type runningControlAPI struct {
	errors  <-chan error
	cleanup func() error
}

func startControlAPI(ctx context.Context, wg *sync.WaitGroup, cfgProvider func() config.Config, state *store.SQLiteStore, activeWorkers func() int, startedAt time.Time) (runningControlAPI, error) {
	cfg := cfgProvider()
	listener, cleanup, err := controlapi.ListenUnix(cfg.Daemon.ControlSocket)
	if err != nil {
		return runningControlAPI{}, err
	}
	server := &http.Server{
		Handler: controlapi.Service{
			Store: state, Config: cfgProvider, ActiveWorkers: activeWorkers,
			StartedAt: startedAt, DaemonVersion: controlapi.BuildVersion,
		}.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	errorsCh := make(chan error, 1)
	wg.Add(2)
	go func() {
		defer wg.Done()
		err := server.Serve(listener)
		if err != nil && !errors.Is(err, http.ErrServerClosed) && !errors.Is(err, net.ErrClosed) {
			errorsCh <- err
		}
	}()
	go func() {
		defer wg.Done()
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			slog.Error("control API shutdown failed", "error", err)
			if closeErr := server.Close(); closeErr != nil {
				slog.Error("force close control API", "error", closeErr)
			}
		}
	}()
	slog.Info("control API listening", "socket", cfg.Daemon.ControlSocket, "api_version", controlapi.Version)
	return runningControlAPI{errors: errorsCh, cleanup: cleanup}, nil
}
