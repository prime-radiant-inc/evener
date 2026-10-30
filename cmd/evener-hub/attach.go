package hub

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/coder/websocket"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubedge"
	"primeradiant.com/evener/internal/hubendpoint"
)

// The attach subcommand is the remote half of the multi-host bridge: it runs on
// the host that already has a hub, dials that hub's loopback AppWire edge, and
// proxies framed AppWire Messages between the WebSocket and its own
// stdin/stdout. A controller speaking AppWire over an SSH channel (its stdin to
// this process's stdin, this process's stdout back) therefore reaches the hub
// without an additionally exposed port.
//
// It is strictly a client of the running hub. It never starts a hub, never
// takes hostlock, and never binds a port. The hard contract is stdout
// discipline: stdout carries newline-delimited AppWire Message JSON and nothing
// else, so every diagnostic goes to stderr.

type attachOptions struct {
	stdio      bool
	hubURL     string
	urlPresent bool
	// configExplicit records whether the operator (or the controller that
	// spawned this bridge) named the path with --config: a named path must
	// load, while the implicit default path may be absent.
	configExplicit bool
	configPath     string
}

// errNonLoopbackAddr refuses a hub address that would carry the capability token
// off the host. The bridge dials an unencrypted ws:// URL, so anything but
// loopback would put the hub's master token on the wire in cleartext.
var errNonLoopbackAddr = errors.New("attach requires a loopback hub address")

// errNoHubAt reports an attach whose dial reached no hub at the address.
var errNoHubAt = errors.New("no hub at")

// attachDialTimeout bounds the WebSocket handshake. A hub that accepts the TCP
// connection but never completes the upgrade would otherwise hang the bridge
// forever with no way out; the pump that follows is bounded by the caller's
// context (a signal), not by this.
const attachDialTimeout = 15 * time.Second

func parseAttachOptions(args []string, stderr io.Writer) (attachOptions, error) {
	opts := attachOptions{configPath: DefaultConfigPath()}
	fs := flag.NewFlagSet("evener hub attach", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.BoolVar(&opts.stdio, "stdio", false, "proxy AppWire between the hub's loopback /rpc and stdin/stdout")
	fs.StringVar(&opts.hubURL, "hub-url", "", "verified literal-loopback HTTP origin")
	fs.StringVar(&opts.configPath, "config", opts.configPath, "path to hub.toml")
	fs.Usage = func() {
		_, _ = fmt.Fprintf(stderr, "Usage: evener hub attach --stdio [--hub-url ORIGIN] [--config path]\n\n")
		_, _ = fmt.Fprintf(stderr, "Run on the hub host: connect to the local hub's loopback AppWire edge\nand proxy newline-delimited AppWire messages between it and stdin/stdout.\n\n")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return opts, err
	}
	if fs.NArg() != 0 {
		return opts, fmt.Errorf("unexpected arguments: %s", strings.Join(fs.Args(), " "))
	}
	set := setFlagNames(fs)
	opts.configExplicit = set["config"]
	opts.urlPresent = set["hub-url"]
	if !opts.stdio {
		return opts, errors.New("attach requires --stdio")
	}
	return opts, nil
}

// runAttach resolves the hub's loopback address and capability token, then
// runs the bidirectional bridge over the process streams carried in deps.
func runAttach(args []string, stderr io.Writer, deps mainDeps) (err error) {
	var parseOutput bytes.Buffer
	opts, err := parseAttachOptions(args, &parseOutput)
	parseReported := parseOutput.Len() != 0
	_, _ = parseOutput.WriteTo(stderr)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		// flag.Parse already reports syntax failures. Semantic parse errors do not.
		if !parseReported {
			_, _ = fmt.Fprintf(stderr, "[hub] %v\n", err)
		}
		return err
	}
	defer func() {
		if err != nil {
			_, _ = fmt.Fprintf(stderr, "[hub] %v\n", err)
		}
	}()
	ctx, cancel := deps.notifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	// Validate an explicit destination before reading any host credential.
	if opts.urlPresent {
		if _, err := attachOrigin(opts.hubURL); err != nil {
			return err
		}
	}
	report, err := resolveHubEndpoint(ctx, endpointOptions{configPath: opts.configPath, configExplicit: opts.configExplicit}, deps)
	if err != nil {
		return err
	}
	if report.Running == nil {
		return fmt.Errorf("%w: selected hub is %s (%s)", errNoHubAt, report.State, report.Reason)
	}
	record := *report.Running
	if record.LoopbackURL == "" {
		return errNonLoopbackAddr
	}
	origin, err := attachOrigin(record.LoopbackURL)
	if err != nil {
		return err
	}
	if opts.urlPresent {
		selected, _ := attachOrigin(opts.hubURL)
		if selected.String() != origin.String() {
			return hubendpoint.ErrIdentityMismatch
		}
	}
	token, err := readAuthToken(record.HubStateRoot)
	if err != nil {
		return err
	}

	stdin := deps.stdin
	if stdin == nil {
		stdin = os.Stdin
	}
	stdout := deps.stdout
	if stdout == nil {
		stdout = os.Stdout
	}
	stream := appwire.NewStreamTransport(newStdioStream(stdin, stdout))
	// A signal ends the bridge: the pump is one long-lived call, so without this
	// an operator could not stop an attached bridge except by killing it.
	return proxyAppWire(ctx, origin.String(), token, record, stream)
}

// proxyAppWire dials the hub's loopback AppWire edge and pumps Messages in both
// directions until either side closes. A failed dial is reported as "no hub at
// <addr>" so the operator sees the actionable cause rather than a bare
// connection error.
func proxyAppWire(ctx context.Context, rawOrigin, token string, record hubendpoint.Record, stream appwire.Transport) error {
	origin, err := attachOrigin(rawOrigin)
	if err != nil {
		return err
	}
	client, err := hubendpoint.HTTPClientWithBearer(nil, token, origin, &record)
	if err != nil {
		return err
	}
	header := http.Header{}
	// Mark the connection remote-originated for the hub it dials (component 05,
	// §"The origin signal is an explicit bridge marker on the connection"): the
	// hub stamps this role into every request's context, so its host-routing
	// origin guard can refuse a remote dispatch — including an attach that would
	// make the host hub dial yet another host — instead of an honest A→B→A cycle
	// recursing past depth 1. The marker is cooperative only.
	header.Set(bridgeOriginHeader, "1")
	dialCtx, cancel := context.WithTimeout(ctx, attachDialTimeout)
	defer cancel()
	ws, err := appwire.DialWebSocketWithHeaders(dialCtx, origin.RPCURL(), client, header)
	if err != nil {
		// A cancel during the handshake is the operator stopping the bridge, not
		// a failure to reach a hub.
		if ctx.Err() != nil {
			return nil
		}
		return fmt.Errorf("%w %s: %w", errNoHubAt, origin.String(), err)
	}
	return pumpBoth(ctx, ws, stream)
}

// pumpBoth runs one goroutine per direction and returns when the first one
// ends, closing both transports so the other unblocks. A clean channel close
// (EOF, or a normal WebSocket close) is not an error.
func pumpBoth(ctx context.Context, a, b appwire.Transport) error {
	errs := make(chan error, 2)
	go pumpTransport(ctx, a, b, errs)
	go pumpTransport(ctx, b, a, errs)
	var err error
	select {
	case err = <-errs:
	case <-ctx.Done():
		// Neither Close below can unblock a pump parked on stdin: stdioStream's
		// Close is a no-op because the stream is the process's own. Waiting only
		// on errs therefore left a signaled bridge unreapable except by SIGKILL.
		err = ctx.Err()
	}
	_ = a.Close()
	_ = b.Close()
	// A canceled context is a requested shutdown, not a failure: an operator
	// pressing Ctrl-C on a healthy bridge must not get a diagnostic and a
	// nonzero exit. A clean EOF and a normal WebSocket close are the same.
	if cleanShutdown(ctx, err) {
		return nil
	}
	return err
}

// cleanShutdown reports whether err ends the bridge normally: the operator
// canceled it, the peer closed the stream cleanly, or the WebSocket closed with
// a normal status. Anything else is a failure worth reporting.
func cleanShutdown(ctx context.Context, err error) bool {
	return ctx.Err() != nil ||
		err == nil ||
		errors.Is(err, io.EOF) ||
		websocket.CloseStatus(err) == websocket.StatusNormalClosure
}

func pumpTransport(ctx context.Context, from, to appwire.Transport, errs chan<- error) {
	for {
		msg, err := from.Recv(ctx)
		if err != nil {
			errs <- err
			return
		}
		if err := to.Send(ctx, msg); err != nil {
			errs <- err
			return
		}
	}
}

// newStdioStream adapts separate stdin/stdout handles to the
// io.ReadWriteCloser a StreamTransport needs. Close is a no-op: the process
// exiting is what ends the channel.
func newStdioStream(r io.Reader, w io.Writer) stdioStream {
	return stdioStream{Reader: r, Writer: w}
}

type stdioStream struct {
	io.Reader
	io.Writer
}

func (stdioStream) Close() error { return nil }

// readAuthToken loads the hub's capability token from its state root. The
// basename comes from hubedge.TokenFileName rather than a duplicated literal,
// and the token is trimmed because the on-disk file ends in a newline an
// untrimmed Authorization header value would reject.
func readAuthToken(hubStateRoot string) (string, error) {
	if strings.TrimSpace(hubStateRoot) == "" {
		return "", errors.New("hub state root not configured")
	}
	path := filepath.Join(hubStateRoot, hubedge.TokenFileName)
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read auth token %s: %w", path, err)
	}
	token := strings.TrimSpace(string(data))
	if token == "" {
		return "", fmt.Errorf("empty auth token at %s", path)
	}
	return token, nil
}

// attachOrigin enforces the bridge's stricter literal-loopback/no-proxy policy.
func attachOrigin(raw string) (hubendpoint.Origin, error) {
	origin, err := hubendpoint.ParseOrigin(raw)
	if err != nil || origin.Scheme() != "http" || !isLoopbackHost(origin.Hostname()) {
		return hubendpoint.Origin{}, errNonLoopbackAddr
	}
	return origin, nil
}

func isLoopbackHost(host string) bool {
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
