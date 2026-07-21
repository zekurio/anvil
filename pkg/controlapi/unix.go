package controlapi

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

func ListenUnix(socketPath string) (net.Listener, func() error, error) {
	socketPath = strings.TrimSpace(socketPath)
	if socketPath == "" {
		return nil, nil, errors.New("control socket path is required")
	}
	if !filepath.IsAbs(socketPath) {
		return nil, nil, fmt.Errorf("control socket path %q must be absolute", socketPath)
	}
	if err := os.MkdirAll(filepath.Dir(socketPath), 0o750); err != nil {
		return nil, nil, fmt.Errorf("create control socket directory: %w", err)
	}
	if info, err := os.Lstat(socketPath); err == nil {
		if info.Mode()&os.ModeSocket == 0 {
			return nil, nil, fmt.Errorf("control socket path %q exists and is not a socket", socketPath)
		}
		connection, dialErr := net.DialTimeout("unix", socketPath, 250*time.Millisecond)
		if dialErr == nil {
			return nil, nil, errors.Join(
				fmt.Errorf("control socket path %q is already accepting connections", socketPath),
				connection.Close(),
			)
		}
		if !errors.Is(dialErr, syscall.ECONNREFUSED) {
			return nil, nil, fmt.Errorf("probe existing control socket: %w", dialErr)
		}
		if err := os.Remove(socketPath); err != nil {
			return nil, nil, fmt.Errorf("remove stale control socket: %w", err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, nil, fmt.Errorf("inspect control socket: %w", err)
	}
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		return nil, nil, fmt.Errorf("listen on control socket: %w", err)
	}
	if err := os.Chmod(socketPath, 0o660); err != nil {
		return nil, nil, errors.Join(
			fmt.Errorf("set control socket permissions: %w", err),
			listener.Close(),
			os.Remove(socketPath),
		)
	}
	created, err := os.Lstat(socketPath)
	if err != nil {
		return nil, nil, errors.Join(
			fmt.Errorf("inspect created control socket: %w", err),
			listener.Close(),
			os.Remove(socketPath),
		)
	}
	cleanup := func() error {
		closeErr := listener.Close()
		if errors.Is(closeErr, net.ErrClosed) {
			closeErr = nil
		}
		info, statErr := os.Lstat(socketPath)
		switch {
		case errors.Is(statErr, os.ErrNotExist):
			return closeErr
		case statErr != nil:
			return errors.Join(closeErr, fmt.Errorf("inspect control socket during cleanup: %w", statErr))
		case !os.SameFile(created, info):
			return errors.Join(closeErr, errors.New("control socket path changed before cleanup"))
		default:
			return errors.Join(closeErr, os.Remove(socketPath))
		}
	}
	return listener, cleanup, nil
}
