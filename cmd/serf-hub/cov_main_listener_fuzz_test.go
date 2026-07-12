package main

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"sync"
	"testing"
)

type fuzzHubServer struct {
	ctx       context.Context
	listenErr error
	shutdown  chan struct{}
	once      sync.Once
}

func (s *fuzzHubServer) ListenAndServe() error {
	if s.ctx != nil {
		<-s.ctx.Done()
	}
	return s.listenErr
}

func (s *fuzzHubServer) Shutdown(context.Context) error {
	s.once.Do(func() { close(s.shutdown) })
	return errors.New("ignored shutdown error")
}

type fuzzHubCompanion struct{ shutdown chan struct{} }

func (c *fuzzHubCompanion) Shutdown(context.Context) error {
	close(c.shutdown)
	return errors.New("ignored companion shutdown error")
}

func FuzzMainListenerLifecycle(f *testing.F) {
	f.Add(byte(0), "127.0.0.1:9180")
	f.Add(byte(1), "0.0.0.0:9180")
	f.Add(byte(2), "[::]:9180")
	f.Add(byte(3), "localhost:0")

	f.Fuzz(func(t *testing.T, mode byte, addr string) {
		switch mode % 4 {
		case 0:
			got := advertisedHubHost(addr, func() (string, error) { return "host", nil })
			want := addr
			if len(addr) >= len("0.0.0.0:") && addr[:len("0.0.0.0:")] == "0.0.0.0:" ||
				len(addr) >= len("[::]:") && addr[:len("[::]:")] == "[::]:" {
				want = "host" + addr[len(addr)-1:]
				if i := lastColon(addr); i >= 0 {
					want = "host" + addr[i:]
				}
			}
			if got != want {
				t.Fatalf("advertisedHubHost(%q) = %q, want %q", addr, got, want)
			}
		case 1:
			if got := advertisedHubHost("0.0.0.0:9180", func() (string, error) { return "host", nil }); got != "host:9180" {
				t.Fatalf("wildcard host = %q", got)
			}
			if got := advertisedHubHost("[::]:7", func() (string, error) { return "", errors.New("hostname") }); got != "localhost:7" {
				t.Fatalf("fallback host = %q", got)
			}
		case 2:
			ctx, cancel := context.WithCancel(context.Background())
			srv := &fuzzHubServer{ctx: ctx, listenErr: http.ErrServerClosed, shutdown: make(chan struct{})}
			companion := &fuzzHubCompanion{shutdown: make(chan struct{})}
			cancel()
			if err := serveHub(ctx, srv, companion); err != nil {
				t.Fatalf("serveHub shutdown: %v", err)
			}
			select {
			case <-srv.shutdown:
			default:
				t.Fatal("server was not shut down")
			}
			select {
			case <-companion.shutdown:
			default:
				t.Fatal("companion was not shut down")
			}
		case 3:
			want := errors.New("listen")
			ctx, cancel := context.WithCancel(context.Background())
			srv := &fuzzHubServer{listenErr: want, shutdown: make(chan struct{})}
			if err := serveHub(ctx, srv, nil); !errors.Is(err, want) {
				t.Fatalf("serveHub error = %v", err)
			}
			cancel()
			<-srv.shutdown
			ctx, cancel = context.WithCancel(context.Background())
			srv = &fuzzHubServer{listenErr: nil, shutdown: make(chan struct{})}
			if err := serveHub(ctx, srv, nil); err != nil {
				t.Fatalf("serveHub nil error = %v", err)
			}
			cancel()
			<-srv.shutdown
		}
	})
}

func FuzzMainOptions(f *testing.F) {
	f.Add(byte(0), "")
	f.Add(byte(1), "127.0.0.1:1")
	f.Add(byte(2), "/tmp/serf")
	f.Add(byte(3), "extra")
	f.Add(byte(4), "")
	f.Add(byte(5), "")

	f.Fuzz(func(t *testing.T, mode byte, value string) {
		var args []string
		wantErr := false
		switch mode % 6 {
		case 0:
			args = nil
		case 1:
			args = []string{"-addr", value, "-config", "/config"}
		case 2:
			args = []string{"-serf=" + value}
		case 3:
			args, wantErr = []string{value}, true
		case 4:
			args, wantErr = []string{"-unknown"}, true
		case 5:
			args, wantErr = []string{"-h"}, true
		}
		var stderr bytes.Buffer
		opts, err := parseHubOptions(args, &stderr)
		if (err != nil) != wantErr {
			t.Fatalf("parseHubOptions(%q) error = %v", args, err)
		}
		if mode%6 == 1 && err == nil && (opts.addr != value || opts.configPath != "/config") {
			t.Fatalf("options = %+v", opts)
		}
		if mode%6 == 2 && err == nil && opts.serfBinary != value {
			t.Fatalf("serf binary = %q", opts.serfBinary)
		}
		if mode%6 == 5 && stderr.Len() == 0 {
			t.Fatal("help output is empty")
		}
	})
}

func lastColon(s string) int {
	for i := len(s) - 1; i >= 0; i-- {
		if s[i] == ':' {
			return i
		}
	}
	return -1
}
