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
// and CPU can be profiled without restarting it. It returns the index URL on
// the address it actually bound (port 0 resolves to the kernel-assigned port)
// and a stop function the caller must defer. When the variable is unset it
// binds nothing and returns an empty URL.
//
// The handlers live on a private mux. Importing net/http/pprof also registers
// them on http.DefaultServeMux, which no Evener server serves.
func StartLivePprof() (indexURL string, stop func(), err error) {
	want := envvars.EVENERPprofAddr.Trimmed()
	if want == "" {
		return "", func() {}, nil
	}
	ln, err := listenLoopback(want)
	if err != nil {
		return "", nil, fmt.Errorf("%s=%q: %w", envvars.EVENERPprofAddr.Name, want, err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/debug/pprof/", pprof.Index)
	mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
	mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("/debug/pprof/trace", pprof.Trace)
	srv := &http.Server{Handler: mux}
	go func() { _ = srv.Serve(ln) }()
	return "http://" + ln.Addr().String() + "/debug/pprof/", func() { _ = srv.Close() }, nil
}

func listenLoopback(hostport string) (net.Listener, error) {
	if err := requireLoopback(hostport); err != nil {
		return nil, err
	}
	var lc net.ListenConfig
	return lc.Listen(context.Background(), "tcp", hostport)
}

// requireLoopback refuses any host:port whose host is not a loopback IP or
// "localhost", so the profiling endpoint is never reachable off the machine.
func requireLoopback(hostport string) error {
	host, _, err := net.SplitHostPort(hostport)
	if err != nil {
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
