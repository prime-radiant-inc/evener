package hub

import (
	"bytes"
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hostlock"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/internal/hubendpoint"
)

// This fixture uses the real health and RPC routes, config loader, endpoint file,
// singleton lock and token file. Only the process's stdio is an in-memory pipe.
func TestAttachDiscoveryRoundTrips(t *testing.T) {
	for _, selection := range []string{"default config", "explicit config", "matching URL"} {
		t.Run(selection, func(t *testing.T) {
			root, state := endpointTestRoot(t)
			path := endpointTestConfig(t, root, state, "127.0.0.1:0")
			args := []string{"--stdio"}
			if selection == "default config" {
				data, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				path = DefaultConfigPath()
				if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, data, 0600); err != nil {
					t.Fatal(err)
				}
			} else {
				args = append(args, "--config", path)
			}
			release, err := hostlock.AcquireLock(filepath.Join(state, "hub.lock"))
			if err != nil {
				t.Fatal(err)
			}
			defer release()
			const token = "attach-selected-sentinel"
			if err := os.WriteFile(filepath.Join(state, "auth-token"), []byte(token), 0600); err != nil {
				t.Fatal(err)
			}
			// A decoy default-root token proves the selected config owns credential choice.
			defaultState := filepath.Join(root, "state", "evener")
			if err := os.MkdirAll(defaultState, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(defaultState, "auth-token"), []byte("decoy-default-sentinel"), 0600); err != nil {
				t.Fatal(err)
			}
			srv := httptest.NewUnstartedServer(nil)
			web := NewWebServer(hubcore.WebConfig{AuthToken: token, HubAddr: srv.Listener.Addr().String(), HubBootID: "attach-discovery-boot", Past: hubcore.NewPastIndex("")})
			var bearerHealth, authorizedRPC, bridgeRPC atomic.Int32
			handler := web.Handler()
			srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/api/health" && r.Header.Get("Authorization") != "" {
					bearerHealth.Add(1)
				}
				if r.URL.Path == "/rpc" && r.Header.Get("Authorization") == "Bearer "+token {
					authorizedRPC.Add(1)
				}
				if r.URL.Path == "/rpc" && r.Header.Get(bridgeOriginHeader) == "1" {
					bridgeRPC.Add(1)
				}
				handler.ServeHTTP(w, r)
			})
			srv.Start()
			defer srv.Close()
			record := hubendpoint.Record{SchemaVersion: 1, BootID: "attach-discovery-boot", PID: os.Getpid(), StartedAt: web.startedAt, ConfigPath: path, HubStateRoot: state, RequestedListenAddr: "127.0.0.1:0", ListenSource: "config", BoundAddr: srv.Listener.Addr().String(), LocalURL: srv.URL, LoopbackURL: srv.URL}
			remove, err := hubendpoint.Publish(state, record)
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				if err := remove(); err != nil {
					t.Error(err)
				}
			}()
			if selection == "matching URL" {
				args = append(args, "--hub-url", srv.URL+"/")
			}
			bridge, controller := net.Pipe()
			defer bridge.Close()
			defer controller.Close()
			recorder := &attachRecordingConn{ReadWriteCloser: bridge}
			deps := defaultMainDeps()
			deps.stdin = bridge
			deps.stdout = recorder
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			deps.notifyContext = func(context.Context, ...os.Signal) (context.Context, context.CancelFunc) {
				return context.WithCancel(ctx)
			}
			var stderr bytes.Buffer
			done := make(chan error, 1)
			go func() { done <- runAttach(args, &stderr, deps) }()
			client := appwire.NewClient(appwire.NewStreamTransport(controller))
			client.Start(ctx)
			defer client.Close()
			if _, err := client.Initialize(ctx, appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
				t.Fatal(err)
			}
			if _, err := client.ThreadList(ctx, appwire.ThreadListParams{}); err != nil {
				t.Fatal(err)
			}
			if err := client.Close(); err != nil {
				t.Fatal(err)
			}
			if err := <-done; err != nil {
				t.Fatal(err)
			}
			if bearerHealth.Load() != 0 || authorizedRPC.Load() != 1 || bridgeRPC.Load() != 1 {
				t.Fatalf("bearer health=%d authorized rpc=%d bridge rpc=%d", bearerHealth.Load(), authorizedRPC.Load(), bridgeRPC.Load())
			}
			recorder.mu.Lock()
			stdout := recorder.buf.String()
			recorder.mu.Unlock()
			assertOnlyAppWireFrames(t, stdout)
			if strings.Contains(stdout, token) || strings.Contains(stderr.String(), token) {
				t.Fatal("token escaped bridge")
			}
		})
	}
}

func TestAttachDiscoveryRejectsExplicitMismatchBeforeToken(t *testing.T) {
	root, state := endpointTestRoot(t)
	path := endpointTestConfig(t, root, state, "127.0.0.1:0")
	release, err := hostlock.AcquireLock(filepath.Join(state, "hub.lock"))
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	srv := httptest.NewUnstartedServer(nil)
	web := NewWebServer(hubcore.WebConfig{HubAddr: srv.Listener.Addr().String(), HubBootID: "mismatch-boot", Past: hubcore.NewPastIndex("")})
	srv.Config.Handler = web.Handler()
	srv.Start()
	defer srv.Close()
	record := hubendpoint.Record{SchemaVersion: 1, BootID: "mismatch-boot", PID: os.Getpid(), StartedAt: web.startedAt, ConfigPath: path, HubStateRoot: state, RequestedListenAddr: "127.0.0.1:0", ListenSource: "config", BoundAddr: srv.Listener.Addr().String(), LocalURL: srv.URL, LoopbackURL: srv.URL}
	remove, err := hubendpoint.Publish(state, record)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := remove(); err != nil {
			t.Error(err)
		}
	}()
	// No auth-token exists, so reading it before mismatch would return a file error.
	var stdout, stderr bytes.Buffer
	deps := defaultMainDeps()
	deps.stdin = strings.NewReader("")
	deps.stdout = &stdout
	err = runAttach([]string{"--stdio", "--config", path, "--hub-url", "http://127.0.0.1:1"}, &stderr, deps)
	if !errors.Is(err, hubendpoint.ErrIdentityMismatch) || stdout.Len() != 0 {
		t.Fatalf("err=%v stdout=%q", err, stdout.String())
	}
	for _, flags := range [][]string{{"--hub-url", ""}, {"--addr", "127.0.0.1:9180"}} {
		err = runAttach(append([]string{"--stdio", "--config", path}, flags...), &stderr, deps)
		if err == nil || stdout.Len() != 0 {
			t.Fatalf("flags=%v err=%v", flags, err)
		}
		if flags[0] == "--hub-url" && !errors.Is(err, errNonLoopbackAddr) {
			t.Fatalf("empty URL err=%v", err)
		}
	}
}
