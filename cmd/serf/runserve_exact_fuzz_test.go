//go:build serffuzz

package main

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"primeradiant.com/serf/cmd/serf/internal/rvreg"
	"primeradiant.com/serf/llm"
	"primeradiant.com/serf/llm/providercfg"
	"primeradiant.com/serf/rendezvous"
	"primeradiant.com/serf/server"
)

type exactServeListener struct{ closed chan struct{} }

func (l *exactServeListener) Accept() (net.Conn, error) { <-l.closed; return nil, net.ErrClosed }
func (l *exactServeListener) Close() error {
	select {
	case <-l.closed:
	default:
		close(l.closed)
	}
	return nil
}
func (*exactServeListener) Addr() net.Addr { return exactServeAddr("127.0.0.1:49131") }

type exactServeAddr string

func (a exactServeAddr) Network() string { return "tcp" }
func (a exactServeAddr) String() string  { return string(a) }

func exactServeDeps(t *testing.T) serveDeps {
	t.Helper()
	d := defaultServeDeps()
	d.ensureConfigDirs = func() error { return nil }
	d.seedMarketplaces = func() error { return nil }
	d.newClient = func(string, io.Writer) (*llm.Client, providercfg.Config, bool, func() error, error) {
		c := llm.NewClient()
		c.Register(serveLoggingAdapter{})
		cfg := providercfg.Config{Default: "openai", Instances: []providercfg.InstanceConfig{{Name: "openai", Type: "openai"}}}
		return c, cfg, true, func() error { return nil }, nil
	}
	d.listen = func(context.Context, string, string) (net.Listener, error) {
		return &exactServeListener{closed: make(chan struct{})}, nil
	}
	d.register = func(*rvreg.Registration, string, rendezvous.Entry) error { return nil }
	return d
}

func exactServeArgs(t *testing.T) []string {
	return []string{"--model", "openai/test", "--dir", t.TempDir(), "--state-dir", t.TempDir(), "--run-dir", t.TempDir()}
}

func fuzzRunServeStartupBranches(t *testing.T) {
	t.Setenv("SERF_MODEL", "")
	boom := errors.New("boom")
	tests := []struct {
		name   string
		args   func(*testing.T) []string
		mutate func(*testing.T, *serveDeps)
	}{
		{"cpu error", func(t *testing.T) []string { return append(exactServeArgs(t), "--cpu-profile", "x") }, func(_ *testing.T, d *serveDeps) {
			d.startCPUProfile = func(string) (func(), error) { return nil, boom }
		}},
		{"trace error", func(t *testing.T) []string { return append(exactServeArgs(t), "--trace", "x") }, func(_ *testing.T, d *serveDeps) { d.startTrace = func(string) (func(), error) { return nil, boom } }},
		{"getwd error", func(*testing.T) []string { return []string{"--model", "openai/test"} }, func(_ *testing.T, d *serveDeps) { d.getwd = func() (string, error) { return "", boom } }},
		{"config error", exactServeArgs, func(_ *testing.T, d *serveDeps) { d.ensureConfigDirs = func() error { return boom } }},
		{"client error", exactServeArgs, func(_ *testing.T, d *serveDeps) {
			d.newClient = func(string, io.Writer) (*llm.Client, providercfg.Config, bool, func() error, error) {
				return nil, providercfg.Config{}, false, nil, boom
			}
		}},
		{"listen error", exactServeArgs, func(_ *testing.T, d *serveDeps) {
			d.listen = func(context.Context, string, string) (net.Listener, error) { return nil, boom }
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			d := exactServeDeps(t)
			tc.mutate(t, &d)
			if err := runServeWithDeps(tc.args(t), d); err == nil {
				t.Fatal("expected error")
			}
		})
	}
}

func fuzzRunServeCallbacks(t *testing.T) {
	d := exactServeDeps(t)
	var srv *server.Server
	var stop context.CancelFunc
	d.notifyContext = func(ctx context.Context, _ ...os.Signal) (context.Context, context.CancelFunc) {
		ctx, stop = context.WithCancel(ctx)
		return ctx, stop
	}
	d.newServer = func(cfg server.ServerConfig) serveServer { srv = server.NewServer(cfg); return srv }
	d.serveHTTP = func(_ *http.Server, _ net.Listener) error {
		requests := []struct{ method, path, body string }{
			{"GET", "/status", ""}, {"GET", "/tasks", ""},
			{"POST", "/model", `{"model":"test2"}`},
		}
		for _, q := range requests {
			req := httptest.NewRequest(q.method, q.path, strings.NewReader(q.body))
			rec := httptest.NewRecorder()
			srv.ServeHTTP(rec, req)
		}
		stop()
		return http.ErrServerClosed
	}
	if err := runServeWithDeps(append(exactServeArgs(t), "--verbose", "--max-subagent-depth", "1", "--reasoning-effort", "low"), d); err != nil {
		t.Fatal(err)
	}
}

func FuzzRunServeExactCoverage(f *testing.F) {
	f.Add(uint8(0))
	f.Fuzz(func(t *testing.T, _ uint8) { fuzzRunServeStartupBranches(t); fuzzRunServeCallbacks(t) })
}
