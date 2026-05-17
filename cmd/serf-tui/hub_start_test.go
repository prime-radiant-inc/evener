package main

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
			got, err := normalizeHubAddress(tt.raw)
			if err != nil {
				t.Fatalf("normalizeHubAddress: %v", err)
			}
			if got.BaseURL != tt.baseURL || got.BindAddr != tt.bindAddr || got.IsLocal != tt.local {
				t.Fatalf("got %+v", got)
			}
		})
	}
}

func TestStartHubClientDoesNotAutoStartRemoteHub(t *testing.T) {
	started := false
	_, err := startHubClient(context.Background(), hubStartConfig{
		RawAddr:       "http://hubbox.example:9180",
		AutoStart:     true,
		HealthTimeout: time.Millisecond,
		DialHub: func(context.Context, hubAddress, *http.Client) (*appwire.Client, error) {
			return nil, errors.New("connection refused")
		},
		StartLocalHub: func(hubStartRequest) error {
			started = true
			return nil
		},
	})
	if err == nil {
		t.Fatal("startHubClient returned nil error")
	}
	if started {
		t.Fatal("startHubClient tried to auto-start a remote hub")
	}
	var startupErr startupError
	if !errors.As(err, &startupErr) || startupErr.Kind != startupErrorRemoteNoAutoStart {
		t.Fatalf("error=%v, want remote no-autostart startup error", err)
	}
}

func TestStartHubClientReportsMissingHubBinary(t *testing.T) {
	_, err := startHubClient(context.Background(), hubStartConfig{
		RawAddr:       "127.0.0.1:9180",
		AutoStart:     true,
		HealthTimeout: time.Millisecond,
		LookPath: func(string) (string, error) {
			return "", os.ErrNotExist
		},
		DialHub: func(context.Context, hubAddress, *http.Client) (*appwire.Client, error) {
			return nil, errors.New("connection refused")
		},
	})
	var startupErr startupError
	if !errors.As(err, &startupErr) || startupErr.Kind != startupErrorMissingHubBinary {
		t.Fatalf("error=%v, want missing binary startup error", err)
	}
}

func TestStartHubClientHonorsNoAutoStartForLocalHub(t *testing.T) {
	started := false
	_, err := startHubClient(context.Background(), hubStartConfig{
		RawAddr:       "127.0.0.1:9180",
		AutoStart:     false,
		HealthTimeout: time.Millisecond,
		DialHub: func(context.Context, hubAddress, *http.Client) (*appwire.Client, error) {
			return nil, errors.New("connection refused")
		},
		StartLocalHub: func(hubStartRequest) error {
			started = true
			return nil
		},
	})
	if err == nil {
		t.Fatal("startHubClient returned nil error")
	}
	if started {
		t.Fatal("startHubClient auto-started despite no-auto-start")
	}
	var startupErr startupError
	if !errors.As(err, &startupErr) || startupErr.Kind != startupErrorHubUnavailable {
		t.Fatalf("error=%v, want hub unavailable startup error", err)
	}
}

func TestStartHubClientPassesStateDirAndLogFileToLocalHub(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "state", "serf")
	logFile := filepath.Join(t.TempDir(), "serf-tui.log")
	hubBin := filepath.Join(t.TempDir(), "serf-hub")
	writeExecutable(t, hubBin)
	var got hubStartRequest
	started := false
	runtime, err := startHubClient(context.Background(), hubStartConfig{
		RawAddr:       "127.0.0.1:9180",
		HubBin:        hubBin,
		StateDir:      stateDir,
		LogFile:       logFile,
		AutoStart:     true,
		HealthTimeout: time.Millisecond,
		DialHub: func(context.Context, hubAddress, *http.Client) (*appwire.Client, error) {
			if !started {
				return nil, errors.New("connection refused")
			}
			return appwire.NewClient(noopAppWireTransport{}), nil
		},
		StartLocalHub: func(req hubStartRequest) error {
			started = true
			got = req
			return nil
		},
		CheckHubEnvironment: func(context.Context, hubAddress, *http.Client, string) error {
			return nil
		},
	})
	if err != nil {
		t.Fatalf("startHubClient: %v", err)
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
		kind startupErrorKind
	}{
		{name: "bind failure", err: fmt.Errorf("listen tcp 127.0.0.1:9180: bind: address already in use"), kind: startupErrorBindFailure},
		{name: "other start failure", err: errors.New("permission denied"), kind: startupErrorUnhealthyHub},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := startHubClient(context.Background(), hubStartConfig{
				RawAddr:       "127.0.0.1:9180",
				HubBin:        writeTempExecutable(t),
				AutoStart:     true,
				HealthTimeout: time.Millisecond,
				DialHub: func(context.Context, hubAddress, *http.Client) (*appwire.Client, error) {
					return nil, errors.New("connection refused")
				},
				StartLocalHub: func(hubStartRequest) error {
					return tt.err
				},
			})
			var startupErr startupError
			if !errors.As(err, &startupErr) || startupErr.Kind != tt.kind {
				t.Fatalf("error=%v, want kind %s", err, tt.kind)
			}
		})
	}
}

func TestStartHubClientReportsUnhealthyAutoStartedHub(t *testing.T) {
	_, err := startHubClient(context.Background(), hubStartConfig{
		RawAddr:       "127.0.0.1:9180",
		HubBin:        writeTempExecutable(t),
		AutoStart:     true,
		HealthTimeout: time.Millisecond,
		DialHub: func(context.Context, hubAddress, *http.Client) (*appwire.Client, error) {
			return nil, errors.New("connection refused")
		},
		StartLocalHub: func(hubStartRequest) error {
			return nil
		},
	})
	var startupErr startupError
	if !errors.As(err, &startupErr) || startupErr.Kind != startupErrorUnhealthyHub {
		t.Fatalf("error=%v, want unhealthy hub startup error", err)
	}
}

func TestStartHubClientDoesNotAutoStartIncompatibleOrStaleHub(t *testing.T) {
	tests := []struct {
		name string
		cfg  hubStartConfig
		kind startupErrorKind
	}{
		{
			name: "incompatible api",
			cfg: hubStartConfig{
				DialHub: func(context.Context, hubAddress, *http.Client) (*appwire.Client, error) {
					return nil, startupError{Kind: startupErrorIncompatibleAPI, Detail: "protocol mismatch"}
				},
			},
			kind: startupErrorIncompatibleAPI,
		},
		{
			name: "stale environment",
			cfg: hubStartConfig{
				StateDir: filepath.Join(t.TempDir(), "state", "serf"),
				DialHub: func(context.Context, hubAddress, *http.Client) (*appwire.Client, error) {
					return appwire.NewClient(noopAppWireTransport{}), nil
				},
				CheckHubEnvironment: func(context.Context, hubAddress, *http.Client, string) error {
					return startupError{Kind: startupErrorStaleEnvironment, Detail: "state dir mismatch"}
				},
			},
			kind: startupErrorStaleEnvironment,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			started := false
			tt.cfg.RawAddr = "127.0.0.1:9180"
			tt.cfg.AutoStart = true
			tt.cfg.HealthTimeout = time.Millisecond
			tt.cfg.StartLocalHub = func(hubStartRequest) error {
				started = true
				return nil
			}
			_, err := startHubClient(context.Background(), tt.cfg)
			if started {
				t.Fatal("startHubClient tried to auto-start after terminal startup error")
			}
			var startupErr startupError
			if !errors.As(err, &startupErr) || startupErr.Kind != tt.kind {
				t.Fatalf("error=%v, want kind %s", err, tt.kind)
			}
		})
	}
}

func TestStartupErrorScreenNamesFailureKind(t *testing.T) {
	tests := []struct {
		name string
		err  startupError
		want string
	}{
		{name: "missing binary", err: startupError{Kind: startupErrorMissingHubBinary, Detail: "not found"}, want: "Cannot find serf-hub binary"},
		{name: "bind failure", err: startupError{Kind: startupErrorBindFailure, Addr: "http://127.0.0.1:9180", Detail: "address already in use"}, want: "Hub failed to bind"},
		{name: "unhealthy", err: startupError{Kind: startupErrorUnhealthyHub, Addr: "http://127.0.0.1:9180", Detail: "timeout"}, want: "did not become healthy"},
		{name: "incompatible", err: startupError{Kind: startupErrorIncompatibleAPI, Addr: "http://127.0.0.1:9180", Detail: "old protocol"}, want: "Hub API is incompatible"},
		{name: "stale", err: startupError{Kind: startupErrorStaleEnvironment, Addr: "http://127.0.0.1:9180", Detail: "state dir mismatch"}, want: "different state/auth environment"},
		{name: "remote", err: startupError{Kind: startupErrorRemoteNoAutoStart, Addr: "http://hubbox.example:9180", Detail: "connection refused"}, want: "Remote Hub is not reachable"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			screen := startupErrorScreen(tt.err)
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
	_, err := startHubClient(context.Background(), hubStartConfig{
		RawAddr:       "http://hubbox.example:9180",
		LogFile:       logFile,
		AutoStart:     true,
		HealthTimeout: time.Millisecond,
		DialHub: func(context.Context, hubAddress, *http.Client) (*appwire.Client, error) {
			return nil, errors.New("connection refused")
		},
	})
	if err == nil {
		t.Fatal("startHubClient returned nil error")
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
	err := startLocalHub(hubStartRequest{
		Binary:   bin,
		BindAddr: "127.0.0.1:9180",
		LogFile:  filepath.Join(t.TempDir(), "hub.log"),
	})
	if err == nil || !strings.Contains(err.Error(), "address already in use") {
		t.Fatalf("startLocalHub error=%v, want immediate exit output", err)
	}
}

func withLocalHubImmediateExitWindow(t *testing.T, window time.Duration) {
	t.Helper()
	previous := localHubImmediateExitWindow
	localHubImmediateExitWindow = window
	t.Cleanup(func() {
		localHubImmediateExitWindow = previous
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
