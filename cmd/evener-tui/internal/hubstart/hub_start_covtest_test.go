package hubstart

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/envvars"
	"primeradiant.com/evener/internal/e2ecap"
)

const stateDirHubHelperEnv = "EVENER_TUI_HUBSTART_STATE_DIR_HELPER"

func init() {
	if os.Getenv(stateDirHubHelperEnv) != "1" {
		return
	}
	_, _ = fmt.Fprintf(os.Stderr, "state=%s\nxdg=%s\n", envvars.EVENERStateDir.Getenv(), envvars.XDGStateHome.Getenv())
	os.Exit(7)
}

// TestCovDialHubRPCWithFrameHandler exercises the observeFrames != nil branch
// in dialHubRPC. It connects to a fake hub and sets a frame handler; the
// handler is wired on the client before Initialize.
func TestCovDialHubRPCWithFrameHandler(t *testing.T) {
	e2ecap.RequireLoopbackBind(t)
	srv := fakeHubServer(t, appwire.ProtocolVersion)
	defer srv.Close()
	addr := HubAddress{BaseURL: srv.URL}

	type observedFrame struct {
		msg appwire.Message
		err error
	}
	observed := make(chan observedFrame, 1)
	handler := func(msg appwire.Message, err error) {
		observed <- observedFrame{msg: msg, err: err}
	}

	client, err := dialHubRPC(context.Background(), addr, srv.Client(), handler)
	if err != nil {
		t.Fatalf("dialHubRPC: %v", err)
	}
	if client == nil {
		t.Fatal("client is nil")
	}
	t.Cleanup(func() { _ = client.Close() })

	select {
	case frame := <-observed:
		if frame.err != nil {
			t.Fatalf("observed frame error: %v", frame.err)
		}
		if frame.msg.Response == nil {
			t.Fatalf("observed frame = %+v, want initialize response", frame.msg)
		}
		resultJSON, err := json.Marshal(frame.msg.Response.Result)
		if err != nil {
			t.Fatalf("encode observed initialize result: %v", err)
		}
		var response appwire.InitializeResponse
		if err := json.Unmarshal(resultJSON, &response); err != nil {
			t.Fatalf("decode initialize response: %v", err)
		}
		if response.ProtocolVersion != appwire.ProtocolVersion {
			t.Fatalf("observed protocol = %q, want %q", response.ProtocolVersion, appwire.ProtocolVersion)
		}
	case <-time.After(time.Second):
		t.Fatal("ordered frame handler did not observe the initialize response")
	}
}

// TestCovStartLocalHubWithStateDir observes the child-process environment at
// the process-launch boundary.
func TestCovStartLocalHubWithStateDir(t *testing.T) {
	withLocalHubImmediateExitWindow(t, 30*time.Second)
	t.Setenv(stateDirHubHelperEnv, "1")
	dir := t.TempDir()
	stateDir := filepath.Join(dir, "state", "evener")
	stateHome := filepath.Dir(stateDir)
	logFile := filepath.Join(dir, "hub.log")
	bin, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}

	err = StartLocalHub(HubStartRequest{
		Binary:   bin,
		BindAddr: "127.0.0.1:9180",
		StateDir: stateDir,
		LogFile:  logFile,
	})
	wantOutput := fmt.Sprintf("state=%s\nxdg=%s\n", stateDir, stateHome)
	if err == nil || !strings.Contains(err.Error(), "exit status 7: "+strings.TrimSpace(wantOutput)) {
		t.Fatalf("StartLocalHub error = %v, want child exit with exact state environment %q", err, wantOutput)
	}
	logged, readErr := os.ReadFile(logFile)
	if readErr != nil {
		t.Fatalf("read hub log: %v", readErr)
	}
	if string(logged) != wantOutput {
		t.Fatalf("hub child environment output = %q, want %q", logged, wantOutput)
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
