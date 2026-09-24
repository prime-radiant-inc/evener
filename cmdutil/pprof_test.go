package cmdutil

import (
	"net/http"
	"strings"
	"testing"

	"primeradiant.com/evener/envvars"
)

func TestStartLivePprofIsOffWhenUnset(t *testing.T) {
	t.Setenv(envvars.EVENERPprofAddr.Name, "")
	addr, stop, err := StartLivePprof()
	if err != nil {
		t.Fatalf("StartLivePprof: %v", err)
	}
	defer stop()
	if addr != "" {
		t.Fatalf("StartLivePprof bound %q with %s unset; want no listener", addr, envvars.EVENERPprofAddr.Name)
	}
}

func TestStartLivePprofRejectsNonLoopbackAddresses(t *testing.T) {
	for _, addr := range []string{
		":0",            // every interface
		"0.0.0.0:0",     // every IPv4 interface
		"[::]:0",        // every IPv6 interface
		"192.0.2.1:0",   // a routable address
		"example.com:0", // a name that is not localhost
		"127.0.0.1",     // no port
	} {
		t.Run(addr, func(t *testing.T) {
			t.Setenv(envvars.EVENERPprofAddr.Name, addr)
			got, stop, err := StartLivePprof()
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
			bound, stop, err := StartLivePprof()
			if err != nil {
				t.Fatalf("StartLivePprof: %v", err)
			}
			if strings.HasSuffix(bound, ":0") {
				t.Fatalf("StartLivePprof reported %q; want the kernel-assigned port", bound)
			}
			for _, path := range []string{"/debug/pprof/", "/debug/pprof/goroutine?debug=1", "/debug/pprof/heap"} {
				resp, err := http.Get("http://" + bound + path)
				if err != nil {
					t.Fatalf("GET %s: %v", path, err)
				}
				_ = resp.Body.Close()
				if resp.StatusCode != http.StatusOK {
					t.Fatalf("GET %s = %d, want 200", path, resp.StatusCode)
				}
			}
			stop()
			if resp, err := http.Get("http://" + bound + "/debug/pprof/"); err == nil {
				_ = resp.Body.Close()
				t.Fatalf("pprof endpoint still answering after stop (status %d)", resp.StatusCode)
			}
		})
	}
}
