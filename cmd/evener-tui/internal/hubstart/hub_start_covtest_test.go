package hubstart

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
)

// TestCovDialHubRPCWithFrameHandler exercises the observeFrames != nil branch
// in dialHubRPC. It connects to a fake hub and sets a frame handler; the
// handler is wired on the client before Initialize.
func TestCovDialHubRPCWithFrameHandler(t *testing.T) {
	srv := fakeHubServer(t, appwire.ProtocolVersion)
	defer srv.Close()
	addr := HubAddress{BaseURL: srv.URL}

	handler := func(msg appwire.Message, err error) {}

	client, err := dialHubRPC(context.Background(), addr, srv.Client(), handler)
	if err != nil {
		t.Fatalf("dialHubRPC: %v", err)
	}
	if client == nil {
		t.Fatal("client is nil")
	}
	_ = client.Close()
}

// TestCovStartLocalHubWithStateDir exercises the StateDir branch of
// StartLocalHub. The process exits immediately via the helper, but the
// environment setup (StateDir assignment) is exercised.
func TestCovStartLocalHubWithStateDir(t *testing.T) {
	withLocalHubImmediateExitWindow(t, 30*time.Second)
	t.Setenv(immediateExitHubHelperEnv, "1")
	bin, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	stateDir := filepath.Join(t.TempDir(), "state", "evener")
	err = StartLocalHub(HubStartRequest{
		Binary:   bin,
		BindAddr: "127.0.0.1:9180",
		StateDir: stateDir,
		LogFile:  filepath.Join(t.TempDir(), "hub.log"),
	})
	if err == nil || !strings.Contains(err.Error(), "address already in use") {
		t.Fatalf("StartLocalHub error=%v, want immediate exit output", err)
	}
}

// TestCovStartLocalHubNoLogFile exercises the DevNull branch (no log file).
func TestCovStartLocalHubNoLogFile(t *testing.T) {
	withLocalHubImmediateExitWindow(t, 30*time.Second)
	t.Setenv(immediateExitHubHelperEnv, "1")
	bin, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	err = StartLocalHub(HubStartRequest{
		Binary:   bin,
		BindAddr: "127.0.0.1:9180",
	})
	if err == nil || !strings.Contains(err.Error(), "address already in use") {
		t.Fatalf("StartLocalHub error=%v, want immediate exit output", err)
	}
}

// TestCovStartLocalHubBadBinary exercises the cmd.Start error branch.
func TestCovStartLocalHubBadBinary(t *testing.T) {
	err := StartLocalHub(HubStartRequest{
		Binary:   filepath.Join(t.TempDir(), "nonexistent-binary"),
		BindAddr: "127.0.0.1:9180",
		LogFile:  filepath.Join(t.TempDir(), "hub.log"),
	})
	if err == nil {
		t.Fatal("StartLocalHub with bad binary should return error")
	}
}

// TestCovStartLocalHubMkdirAllError exercises the MkdirAll error on a bad
// log file path (a path under a file, not a directory).
func TestCovStartLocalHubMkdirAllError(t *testing.T) {
	bin, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	// Create a file so that MkdirAll under it fails
	badPath := filepath.Join(t.TempDir(), "afile")
	if err := os.WriteFile(badPath, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	err = StartLocalHub(HubStartRequest{
		Binary:   bin,
		BindAddr: "127.0.0.1:9180",
		LogFile:  filepath.Join(badPath, "hub.log"),
	})
	if err == nil {
		t.Fatal("StartLocalHub with bad log dir should return error")
	}
}
