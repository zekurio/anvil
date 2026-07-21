package controlapi

import (
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestListenUnixRefusesActiveAndNonSocketPaths(t *testing.T) {
	socketPath := filepath.Join(t.TempDir(), "anvild.sock")
	_, cleanup, err := ListenUnix(socketPath)
	if err != nil {
		t.Fatalf("ListenUnix() error = %v", err)
	}
	t.Cleanup(func() {
		if err := cleanup(); err != nil {
			t.Errorf("cleanup() error = %v", err)
		}
	})
	if _, _, err := ListenUnix(socketPath); err == nil || !strings.Contains(err.Error(), "already accepting connections") {
		t.Fatalf("second ListenUnix() error = %v", err)
	}
	connection, err := net.Dial("unix", socketPath)
	if err != nil {
		t.Fatalf("original socket no longer accepts connections: %v", err)
	}
	if err := connection.Close(); err != nil {
		t.Fatalf("connection.Close() error = %v", err)
	}

	regularPath := filepath.Join(t.TempDir(), "not-a-socket")
	if err := os.WriteFile(regularPath, []byte("keep"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	if _, _, err := ListenUnix(regularPath); err == nil || !strings.Contains(err.Error(), "is not a socket") {
		t.Fatalf("regular-path ListenUnix() error = %v", err)
	}
	data, err := os.ReadFile(regularPath)
	if err != nil || string(data) != "keep" {
		t.Fatalf("regular path changed: data=%q err=%v", data, err)
	}
}

func TestListenUnixReplacesStaleSocket(t *testing.T) {
	socketPath := filepath.Join(t.TempDir(), "anvild.sock")
	stale, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatalf("net.Listen() error = %v", err)
	}
	if err := stale.Close(); err != nil {
		t.Fatalf("stale.Close() error = %v", err)
	}
	listener, cleanup, err := ListenUnix(socketPath)
	if err != nil {
		t.Fatalf("ListenUnix(stale) error = %v", err)
	}
	if listener == nil {
		t.Fatal("ListenUnix(stale) listener = nil")
	}
	if err := cleanup(); err != nil {
		t.Fatalf("cleanup() error = %v", err)
	}
}
