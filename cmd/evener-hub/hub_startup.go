package hub

import (
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"

	"primeradiant.com/evener/cmd/evener-hub/internal/hubedge"
	"primeradiant.com/evener/cmdutil"
	"primeradiant.com/evener/internal/appserver"
	"primeradiant.com/evener/internal/hubendpoint"
)

// startDiagnostics starts the hub's optional diagnostics: the live pprof
// listener and the raw AppWire frame trace. The returned stop closes the trace
// first and then stops pprof, the reverse of the order they started in. On
// error nothing is left running.
func startDiagnostics(opts hubOptions, stderr io.Writer) (*appserver.WebSocketTrace, func(), error) {
	_, stopPprof, err := cmdutil.StartLivePprof(func(format string, args ...any) {
		_, _ = fmt.Fprintf(stderr, "[hub] "+format+"\n", args...)
	})
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "[hub] %v\n", err)
		return nil, nil, err
	}
	if opts.appwireTrace == "" {
		return nil, stopPprof, nil
	}
	tracePath, absErr := filepath.Abs(opts.appwireTrace)
	if absErr != nil {
		stopPprof()
		_, _ = fmt.Fprintf(stderr, "[hub] appwire trace path: %v\n", absErr)
		return nil, nil, fmt.Errorf("resolve appwire trace path: %w", absErr)
	}
	trace, err := appserver.NewWebSocketTrace(tracePath)
	if err != nil {
		stopPprof()
		_, _ = fmt.Fprintf(stderr, "[hub] appwire trace: %v\n", err)
		return nil, nil, fmt.Errorf("create appwire trace: %w", err)
	}
	_, _ = fmt.Fprintf(stderr, "[hub] recording raw browser AppWire frames at %s; this file contains sensitive data\n", tracePath)
	return trace, func() {
		if closeErr := trace.Close(); closeErr != nil {
			_, _ = fmt.Fprintf(stderr, "[hub] close appwire trace: %v\n", closeErr)
		}
		stopPprof()
	}, nil
}

// publishEndpoint publishes the hub's endpoint record. It is called only after
// the actual listener and health handler exist: a record does not mean ready
// until a reader verifies this boot on the live socket. The returned retire
// withdraws the record and must run before the hub lock is released.
func publishEndpoint(web *WebServer, endpoint EndpointConfig, configIdentity, hubStateRoot, boundAddr string) (hubendpoint.Record, func() error, error) {
	record := hubendpoint.Record{
		SchemaVersion:       hubendpoint.SchemaVersion,
		BootID:              hubBootID,
		PID:                 os.Getpid(),
		StartedAt:           web.startedAt,
		ConfigPath:          configIdentity,
		HubStateRoot:        hubStateRoot,
		RequestedListenAddr: endpoint.ListenAddr.String(),
		ListenSource:        endpoint.ListenSource,
		BoundAddr:           boundAddr,
	}
	if endpoint.PublicURL != nil {
		record.PublicURL = endpoint.PublicURL.String()
	}
	if origin, originErr := hubendpoint.LocalOrigin(boundAddr); originErr == nil {
		record.LocalURL = origin.String()
		if ip := net.ParseIP(origin.Hostname()); ip != nil && ip.IsLoopback() {
			record.LoopbackURL = origin.String()
		}
	}
	retire, err := hubendpoint.Publish(hubStateRoot, record)
	if err != nil {
		return hubendpoint.Record{}, nil, fmt.Errorf("publish hub endpoint: %w", err)
	}
	return record, retire, nil
}

// announceHub tells the operator where the hub is listening and how to sign in.
func announceHub(stderr io.Writer, record hubendpoint.Record, runDir, hubStateRoot, authToken string) {
	_, _ = fmt.Fprintf(stderr, "[hub] evener-hub %s listening on %s (run_dir=%s)\n", Version, record.BoundAddr, runDir)
	_, _ = fmt.Fprintf(stderr, "[hub] configured listener: %s (%s)\n", record.RequestedListenAddr, record.ListenSource)
	if origin, originErr := browserLoginOrigin(record, hubHostname); originErr == nil {
		_, _ = fmt.Fprintf(stderr, "[hub] Browser address: %s (local: %s)\n", origin, record.LocalURL)
		if isTerminalWriter(stderr) {
			link := hubedge.AuthURLFor(origin, authToken)
			_, _ = fmt.Fprintf(stderr, "[hub] Private sign-in link (treat as a password): %s\n", link)
		}
	} else {
		_, _ = fmt.Fprintf(stderr, "[hub] cannot advertise browser origin: %v\n", originErr)
	}
	_, _ = fmt.Fprintln(stderr, "[hub] Keep this terminal running while you use Evener in the browser.")
	_, _ = fmt.Fprintln(stderr, "[hub] retrieve a browser login URL deliberately with evener hub login-url [--config PATH]; treat it as a password")
	_, _ = fmt.Fprintf(stderr, "[hub] Auth token file: %s (use as Authorization: Bearer ... for scripted clients)\n", filepath.Join(hubStateRoot, hubedge.TokenFileName))
}
