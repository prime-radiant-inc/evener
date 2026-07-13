package hubstart

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"primeradiant.com/serf/appwire"
)

// FuzzParseStartup drives hubstart's two real parsers: ParseTUIStartupOptions
// (the serf-tui flag parser, with a stubbed getenv so it is env-independent) and
// NormalizeHubAddress (the hub URL parser). The flag selector bit picks which.
// Oracle: no-panic floor plus, for NormalizeHubAddress, an idempotence
// invariant — re-normalizing a successfully-normalized BaseURL is a fixed point.
func FuzzParseStartup(f *testing.F) {
	seeds := []struct {
		which int
		s     string
	}{
		{0, "--hub-addr=127.0.0.1:9180 --debug"},
		{0, "--no-auto-start-hub --state-dir=/tmp/s"},
		{0, "--auth-token tok --log-file=/tmp/l --hub-bin=/x/serf-hub"},
		{0, "--unknown-flag"},
		{0, "--hub-addr"},
		{0, ""},
		{1, "127.0.0.1:9180"},
		{1, "http://localhost:9180/rpc/"},
		{1, "https://hub.example.com"},
		{1, "ftp://bad"},
		{1, "://nohost"},
		{1, ""},
		{1, "http://[::1]:9180"},
	}
	for _, s := range seeds {
		f.Add(s.which, s.s)
	}

	getenv := func(string) string { return "" }

	f.Fuzz(func(t *testing.T, which int, raw string) {
		if which&1 == 0 {
			args := strings.Split(raw, " ")
			// flag parsing must never panic, only return an error.
			_, _ = ParseTUIStartupOptions(args, getenv)
			return
		}

		addr, err := NormalizeHubAddress(raw)
		if err != nil {
			return
		}
		// A normalized address must re-normalize to itself.
		again, err2 := NormalizeHubAddress(addr.BaseURL)
		if err2 != nil {
			t.Fatalf("re-normalize of %q (from %q) failed: %v", addr.BaseURL, raw, err2)
		}
		if again.BaseURL != addr.BaseURL {
			t.Fatalf("NormalizeHubAddress not idempotent:\n in=%q\n once=%q\n twice=%q", raw, addr.BaseURL, again.BaseURL)
		}
	})
}

// FuzzHubStartupCoverage replays the deterministic startup scenarios from the
// package tests through the native fuzz entry point. Keeping one scenario per
// selector also makes active fuzzing bounded.
func FuzzHubStartupCoverage(f *testing.F) {
	for i := 0; i < 30; i++ {
		f.Add(uint8(i))
	}
	f.Fuzz(func(t *testing.T, selector uint8) {
		switch selector % 30 {
		case 0:
			TestResolveAuthToken(t)
		case 1:
			TestResolveAuthTokenMissingWarns(t)
		case 2:
			TestAuthTokenFilePathWithoutHome(t)
		case 3:
			TestStartupErrorScreenAllKinds(t)
		case 4:
			TestCheckHubEnvironment(t)
		case 5:
			TestHubRPCURL(t)
		case 6:
			TestLooksLikeBindFailure(t)
		case 7:
			TestStartupError_ErrorMessagesPerKind(t)
		case 8:
			TestHTTPClientWithBearer(t)
		case 9:
			TestBearerTransport_EmptyTokenPassesThrough(t)
		case 10:
			TestStartHubClientDoesNotAutoStartRemoteHub(t)
		case 11:
			TestStartHubClientReportsMissingHubBinary(t)
		case 12:
			TestStartHubClientHonorsNoAutoStartForLocalHub(t)
		case 13:
			TestStartHubClientPassesStateDirAndLogFileToLocalHub(t)
		case 14:
			TestStartHubClientReloadsAuthTokenAfterAutoStart(t)
		case 15:
			TestStartHubClientDistinguishesStartupFailures(t)
		case 16:
			TestStartHubClientReportsUnhealthyAutoStartedHub(t)
		case 17:
			TestStartHubClientDoesNotAutoStartIncompatibleOrStaleHub(t)
		case 18:
			TestStartHubClientWritesStartupDiagnosticsToLogFile(t)
		case 19:
			TestStartLocalHubReportsImmediateExitOutput(t)
		case 20:
			TestStateHomeForSerfStateDir(t)
		case 21:
			TestClassifyStartHubError(t)
		case 22:
			_, _ = ParseTUIStartupOptions(nil, nil)
			_ = EnvDefault(func(string) string { return "set" }, "key", "fallback")
			_ = (StartupError{Err: errors.New("wrapped")}).Unwrap()
		case 23:
			fuzzDialHubRPC(t, "")
		case 24:
			fuzzDialHubRPC(t, appwire.ProtocolVersion)
		case 25:
			fuzzDialHubRPC(t, "obsolete")
		case 26:
			fuzzStartLocalHub(t)
		case 27:
			fuzzDiagnosticEdges(t)
		case 28:
			fuzzStartHubEdges(t)
		case 29:
			fuzzTransportEdges(t)
		}
	})
}

func fuzzStartHubEdges(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	client := appwire.NewClient(noopAppWireTransport{})
	_, _ = StartHubClient(ctx, HubStartConfig{RawAddr: "://", LogFile: ""})
	_, _ = StartHubClient(ctx, HubStartConfig{RawAddr: "127.0.0.1:0", AutoStart: false, HealthTimeout: time.Nanosecond})
	got, err := StartHubClient(ctx, HubStartConfig{
		RawAddr:             "127.0.0.1:9180",
		DialHub:             func(context.Context, HubAddress, *http.Client) (*appwire.Client, error) { return client, nil },
		CheckHubEnvironment: func(context.Context, HubAddress, *http.Client, string) error { return nil },
	})
	if err != nil || got.Client == nil {
		t.Fatalf("initial success: runtime=%+v err=%v", got, err)
	}

	started := false
	dials := 0
	_, _ = StartHubClient(ctx, HubStartConfig{
		RawAddr: "127.0.0.1:9180", HubBin: writeTempExecutable(t), AutoStart: true, HealthTimeout: time.Nanosecond,
		DialHub: func(context.Context, HubAddress, *http.Client) (*appwire.Client, error) {
			dials++
			if started {
				return nil, StartupError{Kind: StartupErrorIncompatibleAPI, Detail: "late terminal"}
			}
			return nil, errors.New("down")
		},
		StartLocalHub: func(HubStartRequest) error { started = true; return nil },
	})
	if dials < 2 {
		t.Fatalf("dials=%d", dials)
	}

	started = false
	_, _ = StartHubClient(ctx, HubStartConfig{
		RawAddr: "127.0.0.1:9180", HubBin: writeTempExecutable(t), AutoStart: true, HealthTimeout: time.Nanosecond,
		DialHub: func(context.Context, HubAddress, *http.Client) (*appwire.Client, error) {
			if !started {
				return nil, errors.New("down")
			}
			return client, nil
		},
		StartLocalHub:       func(HubStartRequest) error { started = true; return nil },
		CheckHubEnvironment: func(context.Context, HubAddress, *http.Client, string) error { return errors.New("environment") },
	})
	exitBin := filepath.Join(t.TempDir(), "exit-hub")
	if err := os.WriteFile(exitBin, []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	withLocalHubImmediateExitWindow(t, time.Second)
	_, _ = StartHubClient(ctx, HubStartConfig{
		RawAddr: "127.0.0.1:9180", HubBin: exitBin, AutoStart: true, HealthTimeout: time.Nanosecond,
		DialHub: func(context.Context, HubAddress, *http.Client) (*appwire.Client, error) {
			return nil, errors.New("down")
		},
	})
}

func fuzzTransportEdges(t *testing.T) {
	t.Helper()
	_, _ = dialHubRPC(context.Background(), HubAddress{BaseURL: "http://127.0.0.1:0"}, &http.Client{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		_, _, _ = conn.Read(r.Context())
		_ = conn.Close(websocket.StatusInternalError, "stop")
	}))
	_, _ = dialHubRPC(context.Background(), HubAddress{BaseURL: srv.URL}, srv.Client())
	srv.Close()

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	_, _ = waitForHubHealth(canceled, HubAddress{}, &http.Client{}, time.Second,
		func(context.Context, HubAddress, *http.Client) (*appwire.Client, error) {
			return nil, errors.New("down")
		})
	_ = checkHubEnvironment(context.Background(), HubAddress{BaseURL: "://"}, &http.Client{}, "/state")
}

func fuzzDialHubRPC(t *testing.T, protocol string) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close(websocket.StatusNormalClosure, "")
		_, data, err := conn.Read(r.Context())
		if err != nil {
			return
		}
		var msg appwire.Message
		if json.Unmarshal(data, &msg) != nil || msg.Request == nil {
			return
		}
		out, _ := json.Marshal(appwire.ResponseMessage(msg.Request.ID, appwire.InitializeResponse{ProtocolVersion: protocol}))
		_ = conn.Write(r.Context(), websocket.MessageText, out)
	}))
	defer srv.Close()
	addr := HubAddress{BaseURL: srv.URL}
	client, err := dialHubRPC(context.Background(), addr, srv.Client())
	if protocol == "obsolete" {
		if err == nil {
			t.Fatal("obsolete protocol accepted")
		}
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	_ = client.Close()
}

func fuzzStartLocalHub(t *testing.T) {
	t.Helper()
	withLocalHubImmediateExitWindow(t, time.Millisecond)
	dir := t.TempDir()
	bin := filepath.Join(dir, "hub")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nsleep 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := StartLocalHub(HubStartRequest{Binary: bin, BindAddr: "127.0.0.1:0", StateDir: filepath.Join(dir, "state", "serf")}); err != nil {
		t.Fatal(err)
	}
	if err := StartLocalHub(HubStartRequest{Binary: filepath.Join(dir, "missing"), BindAddr: "127.0.0.1:0"}); err == nil {
		t.Fatal("missing executable started")
	}
	_ = StartLocalHub(HubStartRequest{Binary: bin, BindAddr: "127.0.0.1:0", LogFile: filepath.Join(dir, "missing", "hub.log")})
	_ = StartLocalHub(HubStartRequest{Binary: bin, BindAddr: "127.0.0.1:0", LogFile: dir})
	silent := filepath.Join(dir, "silent")
	if err := os.WriteFile(silent, []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	withLocalHubImmediateExitWindow(t, time.Second)
	_ = StartLocalHub(HubStartRequest{Binary: silent, BindAddr: "127.0.0.1:0"})

	previousOpen := openHubOutput
	openHubOutput = func(name string, flag int, perm os.FileMode) (*os.File, error) {
		if name == os.DevNull {
			return nil, errors.New("null unavailable")
		}
		return previousOpen(name, flag, perm)
	}
	_ = StartLocalHub(HubStartRequest{Binary: bin, BindAddr: "127.0.0.1:0"})
	openHubOutput = previousOpen

	withLocalHubImmediateExitWindow(t, time.Millisecond)
	previousRelease := releaseHubProcess
	releaseHubProcess = func(*os.Process) error { return errors.New("release failed") }
	_ = StartLocalHub(HubStartRequest{Binary: bin, BindAddr: "127.0.0.1:0"})
	releaseHubProcess = previousRelease
}

func fuzzDiagnosticEdges(t *testing.T) {
	t.Helper()
	WriteStartupDiagnostic("", errors.New("ignored"))
	WriteStartupDiagnostic(filepath.Join(t.TempDir(), "ignored"), nil)
	bad := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(bad, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	WriteStartupDiagnostic(filepath.Join(bad, "log"), errors.New("mkdir fails"))
	WriteStartupDiagnostic(t.TempDir(), errors.New("open fails"))
	WriteStartupDiagnostic(filepath.Join(t.TempDir(), "plain.log"), errors.New("plain"))
}
