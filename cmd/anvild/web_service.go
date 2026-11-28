package main

import (
	"context"
	"errors"
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

// webClaim is what the web UI needs from outside the daemon: its address and
// its password. Both are settled before the store is opened.
type webClaim struct {
	listener net.Listener
	password string
}

// claimWeb reads the password and binds the web UI's address without serving
// on it. It runs before the store is opened, so a start that cannot have its
// web UI, because the address is taken or the password file cannot be read,
// fails without having migrated the store or recovered a job. Connections
// that arrive before startWebService wait in the listener's backlog.
//
// The password comes first: running without sign-in is never the fallback,
// and a daemon that cannot sign anyone in has no use for the address.
func claimWeb(ctx context.Context, cfg config.Config) (webClaim, error) {
	password, err := cfg.Web.LoadPassword()
	if err != nil {
		return webClaim{}, err
	}
	var listenConfig net.ListenConfig
	listener, err := listenConfig.Listen(ctx, "tcp", cfg.Web.Listen)
	if err != nil {
		return webClaim{}, fmt.Errorf("the web UI cannot listen on %s (web.listen): %w", cfg.Web.Listen, err)
	}
	if err := checkBoundLoopback(cfg.Web, password, listener.Addr()); err != nil {
		return webClaim{}, errors.Join(err, listener.Close())
	}
	return webClaim{listener: listener, password: password}, nil
}

// checkBoundLoopback holds a web UI without a password to the address it
// really bound. Validate decides loopback by how web.listen is spelled, and
// takes "localhost" at its word; the listener resolved it through the hosts
// file or DNS, which can name any interface.
func checkBoundLoopback(web config.WebConfig, password string, addr net.Addr) error {
	if password != "" || web.AllowUnauthenticated {
		return nil
	}
	if tcp, ok := addr.(*net.TCPAddr); ok && tcp.IP.IsLoopback() {
		return nil
	}
	return fmt.Errorf("web.listen %q bound %s, which is reachable from other machines, but no password is set; set web.password_file (or web.password), or set web.allow_unauthenticated = true to serve the UI without sign-in", web.Listen, addr)
}

// release gives the address back. After startWebService the server owns the
// listener and has closed it by the time the daemon returns, which is not an
// error here.
func (c webClaim) release() error {
	if err := c.listener.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
		return fmt.Errorf("close web listener: %w", err)
	}
	return nil
}

// newWebServer builds the web server with the password claimWeb read.
func newWebServer(password string, deps webServiceDeps) *webapi.Server {
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
	return server
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
