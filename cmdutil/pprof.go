package cmdutil

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/pprof"

	"primeradiant.com/evener/envvars"
)

// StartLivePprof serves the net/http/pprof handlers on the loopback address
// named by EVENER_PPROF_ADDR, so a long-running process's heap, goroutines,
// and CPU can be profiled without restarting it. It logs and returns the index
// URL on the address it actually bound (port 0 resolves to the kernel-assigned
// port) and a stop function the caller must defer. When the variable is unset
// it binds nothing and returns an empty URL.
//
// Only a misconfigured value (not host:port, or a host that is not loopback)
// is an error. Failing to bind a valid address logs a warning and continues
// without pprof: hub-spawned daemons inherit the hub's value, so a fixed port
// the hub already holds must not stop every daemon from starting.
//
// The handlers live on a private mux. Importing net/http/pprof also registers
// them on http.DefaultServeMux, which no Evener server serves.
func StartLivePprof(logf func(format string, args ...any)) (indexURL string, stop func(), err error) {
	want := envvars.EVENERPprofAddr.Trimmed()
	if want == "" {
		return "", func() {}, nil
	}
	if err := requireLoopback(want); err != nil {
		return "", nil, fmt.Errorf("%s=%q: %w", envvars.EVENERPprofAddr.Name, want, err)
	}
	var lc net.ListenConfig
	ln, err := lc.Listen(context.Background(), "tcp", want)
	if err != nil {
		logf("warning: running without pprof: %s=%q: %v", envvars.EVENERPprofAddr.Name, want, err)
		return "", func() {}, nil
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/debug/pprof/", pprof.Index)
	mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
	mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("/debug/pprof/trace", pprof.Trace)
	srv := &http.Server{Handler: mux}
	go func() { _ = srv.Serve(ln) }()
	indexURL = "http://" + ln.Addr().String() + "/debug/pprof/"
	logf("pprof listening on %s", indexURL)
	return indexURL, func() { _ = srv.Close() }, nil
}

// requireLoopback refuses anything but a valid host:port whose host is a
// loopback IP or "localhost", so the profiling endpoint is never reachable off
// the machine.
func requireLoopback(hostport string) error {
	host, port, err := net.SplitHostPort(hostport)
	if err != nil {
		return err
	}
	if _, err := net.DefaultResolver.LookupPort(context.Background(), "tcp", port); err != nil {
		return err
	}
	if host == "localhost" {
		return nil
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		return nil
	}
	return errors.New("host must be a loopback address (127.0.0.1, [::1], or localhost)")
}
