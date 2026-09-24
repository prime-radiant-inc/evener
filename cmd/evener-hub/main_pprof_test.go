package hub

import (
	"bytes"
	"context"
	"net/http"
	"regexp"
	"strings"
	"testing"

	"primeradiant.com/evener/envvars"
)

var hubPprofLogLine = regexp.MustCompile(`pprof listening on (http://\S+/debug/pprof/)`)

// TestRunMainServesLivePprofWhileRunning pins the hub's wiring of
// EVENER_PPROF_ADDR: it logs the address it actually bound, the endpoint
// answers while the hub serves, and it is gone once the hub returns.
func TestRunMainServesLivePprofWhileRunning(t *testing.T) {
	_, cfg, deps := newTraceMainTestDeps(t)
	t.Setenv(envvars.EVENERPprofAddr.Name, "127.0.0.1:0")

	var stderr bytes.Buffer
	var pprofURL string
	var statusWhileServing int
	deps.serve = func(context.Context, hubHTTPServer) error {
		m := hubPprofLogLine.FindStringSubmatch(stderr.String())
		if m == nil {
			return nil
		}
		pprofURL = m[1]
		resp, err := http.Get(pprofURL)
		if err != nil {
			t.Errorf("GET pprof while serving: %v", err)
			return nil
		}
		_ = resp.Body.Close()
		statusWhileServing = resp.StatusCode
		return nil
	}

	if err := runMain([]string{"-addr", cfg.Addr, "-evener", "/bin/evener"}, &stderr, deps); err != nil {
		t.Fatalf("runMain: %v, stderr=%s", err, stderr.String())
	}
	if pprofURL == "" {
		t.Fatalf("hub never logged a live pprof address:\n%s", stderr.String())
	}
	if strings.Contains(pprofURL, ":0/") {
		t.Fatalf("hub logged %q; want the kernel-assigned port", pprofURL)
	}
	if statusWhileServing != http.StatusOK {
		t.Fatalf("pprof index while serving = %d, want 200", statusWhileServing)
	}
	if resp, err := http.Get(pprofURL); err == nil {
		_ = resp.Body.Close()
		t.Fatalf("pprof endpoint still answering after the hub returned (status %d)", resp.StatusCode)
	}
}

func TestRunMainRefusesNonLoopbackPprofAddr(t *testing.T) {
	_, cfg, deps := newTraceMainTestDeps(t)
	t.Setenv(envvars.EVENERPprofAddr.Name, "0.0.0.0:0")
	deps.serve = func(context.Context, hubHTTPServer) error {
		t.Error("hub reached serve with a non-loopback pprof address")
		return nil
	}
	var stderr bytes.Buffer
	err := runMain([]string{"-addr", cfg.Addr, "-evener", "/bin/evener"}, &stderr, deps)
	if err == nil || !strings.Contains(err.Error(), envvars.EVENERPprofAddr.Name) {
		t.Fatalf("runMain error = %v; want a refusal naming %s", err, envvars.EVENERPprofAddr.Name)
	}
}

func TestRunMainStartsNoPprofWhenUnset(t *testing.T) {
	_, cfg, deps := newTraceMainTestDeps(t)
	t.Setenv(envvars.EVENERPprofAddr.Name, "")
	var stderr bytes.Buffer
	if err := runMain([]string{"-addr", cfg.Addr, "-evener", "/bin/evener"}, &stderr, deps); err != nil {
		t.Fatalf("runMain: %v, stderr=%s", err, stderr.String())
	}
	if m := hubPprofLogLine.FindStringSubmatch(stderr.String()); m != nil {
		t.Fatalf("hub started pprof on %s with %s unset", m[1], envvars.EVENERPprofAddr.Name)
	}
}
