package cmdutil

import (
	"fmt"
	"net"
	"net/http"
	"strings"
	"testing"

	"primeradiant.com/evener/envvars"
)

func discardLogf(string, ...any) {}

func TestStartLivePprofIsOffWhenUnset(t *testing.T) {
	t.Setenv(envvars.EVENERPprofAddr.Name, "")
	url, stop, err := StartLivePprof(discardLogf)
	if err != nil {
		t.Fatalf("StartLivePprof: %v", err)
	}
	defer stop()
	if url != "" {
		t.Fatalf("StartLivePprof served %q with %s unset; want no listener", url, envvars.EVENERPprofAddr.Name)
	}
}

func TestStartLivePprofRejectsNonLoopbackAddresses(t *testing.T) {
	for _, addr := range []string{
		":0",              // every interface
		"0.0.0.0:0",       // every IPv4 interface
		"[::]:0",          // every IPv6 interface
		"192.0.2.1:0",     // a routable address
		"example.com:0",   // a name that is not localhost
		"127.0.0.1",       // no port
		"127.0.0.1:99999", // a port out of range
	} {
		t.Run(addr, func(t *testing.T) {
			t.Setenv(envvars.EVENERPprofAddr.Name, addr)
			got, stop, err := StartLivePprof(discardLogf)
			if err == nil {
				stop()
				t.Fatalf("StartLivePprof(%q) bound %q; want a refusal", addr, got)
			}
			if !strings.Contains(err.Error(), envvars.EVENERPprofAddr.Name) {
				t.Fatalf("error %q does not name %s", err, envvars.EVENERPprofAddr.Name)
			}
		})
	}
}

func TestStartLivePprofServesProfilesOnLoopback(t *testing.T) {
	for _, addr := range []string{"127.0.0.1:0", "localhost:0"} {
		t.Run(addr, func(t *testing.T) {
			t.Setenv(envvars.EVENERPprofAddr.Name, addr)
			index, stop, err := StartLivePprof(discardLogf)
			if err != nil {
				t.Fatalf("StartLivePprof: %v", err)
			}
			if strings.Contains(index, ":0/") {
				t.Fatalf("StartLivePprof reported %q; want the kernel-assigned port", index)
			}
			for _, path := range []string{"", "goroutine?debug=1", "heap"} {
				resp, err := http.Get(index + path)
				if err != nil {
					t.Fatalf("GET %s: %v", path, err)
				}
				_ = resp.Body.Close()
				if resp.StatusCode != http.StatusOK {
					t.Fatalf("GET %s = %d, want 200", path, resp.StatusCode)
				}
			}
			stop()
			if resp, err := http.Get(index); err == nil {
				_ = resp.Body.Close()
				t.Fatalf("pprof endpoint still answering after stop (status %d)", resp.StatusCode)
			}
		})
	}
}

// TestStartLivePprofWarnsAndContinuesWhenTheAddressIsTaken pins that a
// profiling switch can never stop a process from starting: a hub-spawned
// daemon inherits the hub's EVENER_PPROF_ADDR, and with a fixed port the hub
// already holds it.
func TestStartLivePprofWarnsAndContinuesWhenTheAddressIsTaken(t *testing.T) {
	taken, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() { _ = taken.Close() }()
	t.Setenv(envvars.EVENERPprofAddr.Name, taken.Addr().String())

	var logged strings.Builder
	url, stop, err := StartLivePprof(func(format string, args ...any) {
		fmt.Fprintf(&logged, format+"\n", args...)
	})
	if err != nil {
		t.Fatalf("StartLivePprof on a taken address = %v; want a warning, not an error", err)
	}
	stop()
	if url != "" {
		t.Fatalf("StartLivePprof reported %q on a taken address; want no endpoint", url)
	}
	if !strings.Contains(logged.String(), "warning") || !strings.Contains(logged.String(), envvars.EVENERPprofAddr.Name) {
		t.Fatalf("log = %q; want a warning naming %s", logged.String(), envvars.EVENERPprofAddr.Name)
	}
}
