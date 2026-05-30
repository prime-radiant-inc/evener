package hubstart

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"primeradiant.com/serf/internal/appwire"
)

func TestHubAddressNormalization(t *testing.T) {
	tests := []struct {
		name     string
		raw      string
		baseURL  string
		bindAddr string
		local    bool
	}{
		{
			name:     "host port",
			raw:      "127.0.0.1:9999",
			baseURL:  "http://127.0.0.1:9999",
			bindAddr: "127.0.0.1:9999",
			local:    true,
		},
		{
			name:     "localhost url",
			raw:      "http://localhost:9180/",
			baseURL:  "http://localhost:9180",
			bindAddr: "localhost:9180",
			local:    true,
		},
		{
			name:     "remote url",
			raw:      "http://example.com:9180",
			baseURL:  "http://example.com:9180",
			bindAddr: "example.com:9180",
			local:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := NormalizeHubAddress(tt.raw)
			if err != nil {
				t.Fatalf("NormalizeHubAddress: %v", err)
			}
			if got.BaseURL != tt.baseURL || got.BindAddr != tt.bindAddr || got.IsLocal != tt.local {
				t.Fatalf("got %+v", got)
			}
		})
	}
}

func TestStartHubClientDoesNotAutoStartRemoteHub(t *testing.T) {
	started := false
	_, err := StartHubClient(context.Background(), HubStartConfig{
		RawAddr:       "http://hubbox.example:9180",
		AutoStart:     true,
		HealthTimeout: time.Millisecond,
		DialHub: func(context.Context, HubAddress, *http.Client) (*appwire.Client, error) {
			return nil, errors.New("connection refused")
		},
		StartLocalHub: func(HubStartRequest) error {
			started = true
			return nil
		},
	})
	if err == nil {
		t.Fatal("StartHubClient returned nil error")
	}
	if started {
		t.Fatal("StartHubClient tried to auto-start a remote hub")
	}
	var startupErr StartupError
	if !errors.As(err, &startupErr) || startupErr.Kind != StartupErrorRemoteNoAutoStart {
		t.Fatalf("error=%v, want remote no-autostart startup error", err)
	}
}

func TestStartHubClientReportsMissingHubBinary(t *testing.T) {
	_, err := StartHubClient(context.Background(), HubStartConfig{
		RawAddr:       "127.0.0.1:9180",
		AutoStart:     true,
		HealthTimeout: time.Millisecond,
		LookPath: func(string) (string, error) {
			return "", os.ErrNotExist
		},
		DialHub: func(context.Context, HubAddress, *http.Client) (*appwire.Client, error) {
			return nil, errors.New("connection refused")
		},
	})
	var startupErr StartupError
	if !errors.As(err, &startupErr) || startupErr.Kind != StartupErrorMissingHubBinary {
		t.Fatalf("error=%v, want missing binary startup error", err)
	}
}

func TestStartHubClientHonorsNoAutoStartForLocalHub(t *testing.T) {
	started := false
	_, err := StartHubClient(context.Background(), HubStartConfig{
		RawAddr:       "127.0.0.1:9180",
		AutoStart:     false,
		HealthTimeout: time.Millisecond,
		DialHub: func(context.Context, HubAddress, *http.Client) (*appwire.Client, error) {
			return nil, errors.New("connection refused")
		},
		StartLocalHub: func(HubStartRequest) error {
			started = true
			return nil
		},
	})
	if err == nil {
		t.Fatal("StartHubClient returned nil error")
	}
	if started {
		t.Fatal("StartHubClient auto-started despite no-auto-start")
	}
	var startupErr StartupError
	if !errors.As(err, &startupErr) || startupErr.Kind != StartupErrorHubUnavailable {
		t.Fatalf("error=%v, want hub unavailable startup error", err)
	}
}

func TestStartHubClientPassesStateDirAndLogFileToLocalHub(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "state", "serf")
	logFile := filepath.Join(t.TempDir(), "serf-tui.log")
	hubBin := filepath.Join(t.TempDir(), "serf-hub")
	writeExecutable(t, hubBin)
	var got HubStartRequest
	started := false
	runtime, err := StartHubClient(context.Background(), HubStartConfig{
		RawAddr:       "127.0.0.1:9180",
		HubBin:        hubBin,
		StateDir:      stateDir,
		LogFile:       logFile,
		AutoStart:     true,
		HealthTimeout: time.Millisecond,
		DialHub: func(context.Context, HubAddress, *http.Client) (*appwire.Client, error) {
			if !started {
				return nil, errors.New("connection refused")
			}
			return appwire.NewClient(noopAppWireTransport{}), nil
		},
		StartLocalHub: func(req HubStartRequest) error {
			started = true
			got = req
			return nil
		},
		CheckHubEnvironment: func(context.Context, HubAddress, *http.Client, string) error {
			return nil
		},
	})
	if err != nil {
		t.Fatalf("StartHubClient: %v", err)
	}
	if runtime.Client == nil {
		t.Fatal("runtime client is nil")
	}
	if got.Binary != hubBin || got.BindAddr != "127.0.0.1:9180" || got.StateDir != stateDir || got.LogFile != logFile {
		t.Fatalf("start request=%+v", got)
	}
}

func TestStartHubClientDistinguishesStartupFailures(t *testing.T) {
	tests := []struct {
		name string
		err  error
		kind StartupErrorKind
	}{
		{name: "bind failure", err: fmt.Errorf("listen tcp 127.0.0.1:9180: bind: address already in use"), kind: StartupErrorBindFailure},
		{name: "other start failure", err: errors.New("permission denied"), kind: StartupErrorUnhealthyHub},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := StartHubClient(context.Background(), HubStartConfig{
				RawAddr:       "127.0.0.1:9180",
				HubBin:        writeTempExecutable(t),
				AutoStart:     true,
				HealthTimeout: time.Millisecond,
				DialHub: func(context.Context, HubAddress, *http.Client) (*appwire.Client, error) {
					return nil, errors.New("connection refused")
				},
				StartLocalHub: func(HubStartRequest) error {
					return tt.err
				},
			})
			var startupErr StartupError
			if !errors.As(err, &startupErr) || startupErr.Kind != tt.kind {
				t.Fatalf("error=%v, want kind %s", err, tt.kind)
			}
		})
	}
}

func TestStartHubClientReportsUnhealthyAutoStartedHub(t *testing.T) {
	_, err := StartHubClient(context.Background(), HubStartConfig{
		RawAddr:       "127.0.0.1:9180",
		HubBin:        writeTempExecutable(t),
		AutoStart:     true,
		HealthTimeout: time.Millisecond,
		DialHub: func(context.Context, HubAddress, *http.Client) (*appwire.Client, error) {
			return nil, errors.New("connection refused")
		},
		StartLocalHub: func(HubStartRequest) error {
			return nil
		},
	})
	var startupErr StartupError
	if !errors.As(err, &startupErr) || startupErr.Kind != StartupErrorUnhealthyHub {
		t.Fatalf("error=%v, want unhealthy hub startup error", err)
	}
}

func TestStartHubClientDoesNotAutoStartIncompatibleOrStaleHub(t *testing.T) {
	tests := []struct {
		name string
		cfg  HubStartConfig
		kind StartupErrorKind
	}{
		{
			name: "incompatible api",
			cfg: HubStartConfig{
				DialHub: func(context.Context, HubAddress, *http.Client) (*appwire.Client, error) {
					return nil, StartupError{Kind: StartupErrorIncompatibleAPI, Detail: "protocol mismatch"}
				},
			},
			kind: StartupErrorIncompatibleAPI,
		},
		{
			name: "stale environment",
			cfg: HubStartConfig{
				StateDir: filepath.Join(t.TempDir(), "state", "serf"),
				DialHub: func(context.Context, HubAddress, *http.Client) (*appwire.Client, error) {
					return appwire.NewClient(noopAppWireTransport{}), nil
				},
				CheckHubEnvironment: func(context.Context, HubAddress, *http.Client, string) error {
					return StartupError{Kind: StartupErrorStaleEnvironment, Detail: "state dir mismatch"}
				},
			},
			kind: StartupErrorStaleEnvironment,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			started := false
			tt.cfg.RawAddr = "127.0.0.1:9180"
			tt.cfg.AutoStart = true
			tt.cfg.HealthTimeout = time.Millisecond
			tt.cfg.StartLocalHub = func(HubStartRequest) error {
				started = true
				return nil
			}
			_, err := StartHubClient(context.Background(), tt.cfg)
			if started {
				t.Fatal("StartHubClient tried to auto-start after terminal startup error")
			}
			var startupErr StartupError
			if !errors.As(err, &startupErr) || startupErr.Kind != tt.kind {
				t.Fatalf("error=%v, want kind %s", err, tt.kind)
			}
		})
	}
}

func TestStartupErrorScreenNamesFailureKind(t *testing.T) {
	tests := []struct {
		name string
		err  StartupError
		want string
	}{
		{name: "missing binary", err: StartupError{Kind: StartupErrorMissingHubBinary, Detail: "not found"}, want: "Cannot find serf-hub binary"},
		{name: "bind failure", err: StartupError{Kind: StartupErrorBindFailure, Addr: "http://127.0.0.1:9180", Detail: "address already in use"}, want: "Hub failed to bind"},
		{name: "unhealthy", err: StartupError{Kind: StartupErrorUnhealthyHub, Addr: "http://127.0.0.1:9180", Detail: "timeout"}, want: "did not become healthy"},
		{name: "incompatible", err: StartupError{Kind: StartupErrorIncompatibleAPI, Addr: "http://127.0.0.1:9180", Detail: "old protocol"}, want: "Hub API is incompatible"},
		{name: "stale", err: StartupError{Kind: StartupErrorStaleEnvironment, Addr: "http://127.0.0.1:9180", Detail: "state dir mismatch"}, want: "different state/auth environment"},
		{name: "remote", err: StartupError{Kind: StartupErrorRemoteNoAutoStart, Addr: "http://hubbox.example:9180", Detail: "connection refused"}, want: "Remote Hub is not reachable"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			screen := StartupErrorScreen(tt.err)
			for _, want := range []string{"Serf TUI startup failed", tt.want, tt.err.Detail} {
				if !strings.Contains(screen, want) {
					t.Fatalf("screen missing %q:\n%s", want, screen)
				}
			}
		})
	}
}

func TestStartHubClientWritesStartupDiagnosticsToLogFile(t *testing.T) {
	logFile := filepath.Join(t.TempDir(), "startup.log")
	_, err := StartHubClient(context.Background(), HubStartConfig{
		RawAddr:       "http://hubbox.example:9180",
		LogFile:       logFile,
		AutoStart:     true,
		HealthTimeout: time.Millisecond,
		DialHub: func(context.Context, HubAddress, *http.Client) (*appwire.Client, error) {
			return nil, errors.New("connection refused")
		},
	})
	if err == nil {
		t.Fatal("StartHubClient returned nil error")
	}
	data, readErr := os.ReadFile(logFile)
	if readErr != nil {
		t.Fatalf("read log: %v", readErr)
	}
	if got := string(data); !strings.Contains(got, "startup failed") || !strings.Contains(got, "remote-no-autostart") {
		t.Fatalf("log=%q, want startup diagnostic", got)
	}
}

func TestStartLocalHubReportsImmediateExitOutput(t *testing.T) {
	withLocalHubImmediateExitWindow(t, 5*time.Second)
	bin := filepath.Join(t.TempDir(), "serf-hub")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\necho 'listen tcp 127.0.0.1:9180: bind: address already in use' >&2\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	err := StartLocalHub(HubStartRequest{
		Binary:   bin,
		BindAddr: "127.0.0.1:9180",
		LogFile:  filepath.Join(t.TempDir(), "hub.log"),
	})
	if err == nil || !strings.Contains(err.Error(), "address already in use") {
		t.Fatalf("StartLocalHub error=%v, want immediate exit output", err)
	}
}

func withLocalHubImmediateExitWindow(t *testing.T, window time.Duration) {
	t.Helper()
	previous := LocalHubImmediateExitWindow
	LocalHubImmediateExitWindow = window
	t.Cleanup(func() {
		LocalHubImmediateExitWindow = previous
	})
}

func writeExecutable(t *testing.T, path string) {
	t.Helper()
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
}

func writeTempExecutable(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "serf-hub")
	writeExecutable(t, path)
	return path
}

type noopAppWireTransport struct{}

func (noopAppWireTransport) Send(context.Context, appwire.Message) error { return nil }
func (noopAppWireTransport) Recv(context.Context) (appwire.Message, error) {
	return appwire.Message{}, context.Canceled
}
func (noopAppWireTransport) Close() error { return nil }
