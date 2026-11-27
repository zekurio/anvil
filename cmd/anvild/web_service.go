package main

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"sync"

	"github.com/zekurio/anvil/pkg/config"
	"github.com/zekurio/anvil/pkg/control"
	"github.com/zekurio/anvil/pkg/controlapi"
	"github.com/zekurio/anvil/pkg/store"
	"github.com/zekurio/anvil/pkg/webapi"
	"github.com/zekurio/anvil/pkg/worker"
	"github.com/zekurio/anvil/web"
)

// webServiceDeps is what the web UI and its API are served from.
type webServiceDeps struct {
	store      *store.SQLiteStore
	jobs       controlapi.Service
	runtime    *runtimeConfig
	progress   *worker.ProgressTracker
	configPath string
}

// listenWeb claims the web UI's address. It runs after the store is open and
// the runtime snapshot is built, so nothing is served that is not ready, and a
// taken port stops the daemon with the address in the error instead of leaving
// it running without the UI its operator expects.
func listenWeb(ctx context.Context, cfg config.Config) (net.Listener, error) {
	var listenConfig net.ListenConfig
	listener, err := listenConfig.Listen(ctx, "tcp", cfg.Web.Listen)
	if err != nil {
		return nil, fmt.Errorf("the web UI cannot listen on %s (web.listen): %w", cfg.Web.Listen, err)
	}
	return listener, nil
}

// newWebServer builds the web server with the password in effect. A password
// file that cannot be read fails the start: running without sign-in is never
// the fallback.
func newWebServer(cfg config.Config, deps webServiceDeps) (*webapi.Server, error) {
	password, err := cfg.Web.LoadPassword()
	if err != nil {
		return nil, err
	}
	ui, built := web.Dist()
	server := &webapi.Server{
		Store:      deps.store,
		Jobs:       deps.jobs,
		Config:     deps.runtime.Get,
		Refresh:    deps.runtime.Refresh,
		Progress:   deps.progress,
		UI:         ui,
		UIBuilt:    built,
		ConfigPath: deps.configPath,
		Version:    control.BuildVersion,
	}
	server.SetPassword(password)
	return server, nil
}

// startWebService serves the UI and the API on an already-claimed listener. A
// web server that stops by itself is logged and the daemon keeps encoding:
// the queue does not depend on the UI, and anvilctl still reaches it.
func startWebService(ctx context.Context, wg *sync.WaitGroup, listener net.Listener, server *webapi.Server, cfg config.Config) {
	wg.Go(func() {
		if err := server.Serve(ctx, listener); err != nil {
			slog.Error("web service stopped", "error", err)
		}
	})
	slog.Info("web UI listening", "address", listener.Addr().String(), "sign_in", cfg.Web.AuthRequired(), "ui_built", server.UIBuilt)
	if !cfg.Web.AuthRequired() && !config.LoopbackHost(cfg.Web.ListenHost()) {
		slog.Warn("the web UI is reachable from other machines without a password (web.allow_unauthenticated); anyone who can reach it can replace and delete media", "address", listener.Addr().String())
	}
}
