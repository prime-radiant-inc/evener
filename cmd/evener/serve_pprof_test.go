package main

import (
	"net"
	"net/http"
	"strings"
	"testing"

	"primeradiant.com/evener/cmdutil"
	"primeradiant.com/evener/envvars"
)

// TestServeKeepsLivePprofUpWhileServing pins the daemon's wiring of
// EVENER_PPROF_ADDR: the endpoint answers while the daemon serves and is gone
// once it returns.
func TestServeKeepsLivePprofUpWhileServing(t *testing.T) {
	t.Setenv(envvars.EVENERPprofAddr.Name, "127.0.0.1:0")
	deps, state, args := newClearServeDeps(t)

	var pprofURL string
	deps.startLivePprof = func() (string, func(), error) {
		addr, stop, err := cmdutil.StartLivePprof()
		pprofURL = addr
		return addr, stop, err
	}
	var statusWhileServing int
	deps.serveHTTP = func(*http.Server, net.Listener) error {
		defer state.srv.shutdown()
		if pprofURL == "" {
			return http.ErrServerClosed
		}
		resp, err := http.Get(pprofURL)
		if err != nil {
			t.Errorf("GET pprof while serving: %v", err)
			return http.ErrServerClosed
		}
		_ = resp.Body.Close()
		statusWhileServing = resp.StatusCode
		return http.ErrServerClosed
	}

	if err := runServeWithDeps(args, deps); err != nil {
		t.Fatalf("serve: %v", err)
	}
	if pprofURL == "" {
		t.Fatal("serve never started the live pprof endpoint")
	}
	if statusWhileServing != http.StatusOK {
		t.Fatalf("pprof index while serving = %d, want 200", statusWhileServing)
	}
	if resp, err := http.Get(pprofURL); err == nil {
		_ = resp.Body.Close()
		t.Fatalf("pprof endpoint still answering after serve returned (status %d)", resp.StatusCode)
	}
}

func TestServeRefusesNonLoopbackPprofAddr(t *testing.T) {
	t.Setenv(envvars.EVENERPprofAddr.Name, "0.0.0.0:0")
	deps, state, args := newClearServeDeps(t)
	deps.serveHTTP = func(*http.Server, net.Listener) error {
		t.Error("serve reached its listener with a non-loopback pprof address")
		state.srv.shutdown()
		return http.ErrServerClosed
	}
	err := runServeWithDeps(args, deps)
	if err == nil || !strings.Contains(err.Error(), envvars.EVENERPprofAddr.Name) {
		t.Fatalf("serve error = %v; want a refusal naming %s", err, envvars.EVENERPprofAddr.Name)
	}
}
