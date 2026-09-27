package hub

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/coder/websocket"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubedge"
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
	stdio bool
	addr  string
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

// attachDialTimeout bounds the WebSocket handshake. A hub that accepts the TCP
// connection but never completes the upgrade would otherwise hang the bridge
// forever with no way out; the pump that follows is bounded by the caller's
// context (a signal), not by this.
const attachDialTimeout = 15 * time.Second

// attachDialClient disables proxying for the handshake. http.DefaultClient
// honors HTTP_PROXY, which would hand the hub's capability token to the proxy's
// host instead of the loopback hub.
var attachDialClient = &http.Client{Transport: &http.Transport{Proxy: nil}}

func parseAttachOptions(args []string, stderr io.Writer) (attachOptions, error) {
	opts := attachOptions{configPath: DefaultConfigPath()}
	fs := flag.NewFlagSet("evener hub attach", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.BoolVar(&opts.stdio, "stdio", false, "proxy AppWire between the hub's loopback /rpc and stdin/stdout")
	fs.StringVar(&opts.addr, "addr", "", "override hub loopback address (default: hub.toml addr, else 127.0.0.1:9180)")
	fs.StringVar(&opts.configPath, "config", opts.configPath, "path to hub.toml")
	fs.Usage = func() {
		_, _ = fmt.Fprintf(stderr, "Usage: evener hub attach --stdio [--addr host:port] [--config path]\n\n")
		_, _ = fmt.Fprintf(stderr, "Run on the hub host: connect to the local hub's loopback AppWire edge\nand proxy newline-delimited AppWire messages between it and stdin/stdout.\n\n")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return opts, err
	}
	if fs.NArg() != 0 {
		return opts, fmt.Errorf("unexpected arguments: %s", strings.Join(fs.Args(), " "))
	}
	// fs.Visit reports the flags that were actually set, so a path the operator
	// named is distinguishable from the implicit default before it is loaded.
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "config" {
			opts.configExplicit = true
		}
	})
	if !opts.stdio {
		return opts, errors.New("attach requires --stdio")
	}
	return opts, nil
}

// runAttach resolves the hub's loopback address and capability token, then
// runs the bidirectional bridge over the process streams carried in deps.
func runAttach(args []string, stderr io.Writer, deps mainDeps) error {
	opts, err := parseAttachOptions(args, stderr)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		_, _ = fmt.Fprintf(stderr, "[hub] %v\n", err)
		return err
	}
	// The address and state root come from the hub's own config machinery, not
	// a re-derived path, so a hub.toml override moves the bridge with it.
	cfg, err := deps.loadConfig(opts.configPath, opts.configExplicit)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "[hub] config: %v\n", err)
		return err
	}
	addr := opts.addr
	if addr == "" {
		addr = cfg.Addr
	}
	addr = loopbackAddr(addr)
	token, err := readAuthToken(cfg.HubStateRoot)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "[hub] %v\n", err)
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
	ctx, cancel := deps.notifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if err := proxyAppWire(ctx, addr, token, stream); err != nil {
		_, _ = fmt.Fprintf(stderr, "[hub] %v\n", err)
		return err
	}
	return nil
}

// proxyAppWire dials the hub's loopback AppWire edge and pumps Messages in both
// directions until either side closes. A failed dial is reported as "no hub at
// <addr>" so the operator sees the actionable cause rather than a bare
// connection error.
func proxyAppWire(ctx context.Context, addr, token string, stream appwire.Transport) error {
	hubURL, err := resolveHubURL(addr)
	if err != nil {
		return err
	}
	header := http.Header{}
	if token != "" {
		header.Set("Authorization", "Bearer "+token)
	}
	// Mark the connection remote-originated for the hub it dials (component 05,
	// §"The origin signal is an explicit bridge marker on the connection"): the
	// hub stamps this role into every request's context, so its host-routing
	// origin guard can refuse a remote dispatch — including an attach that would
	// make the host hub dial yet another host — instead of an honest A→B→A cycle
	// recursing past depth 1. The marker is cooperative only.
	header.Set(bridgeOriginHeader, "1")
	dialCtx, cancel := context.WithTimeout(ctx, attachDialTimeout)
	defer cancel()
	ws, err := appwire.DialWebSocketWithHeaders(dialCtx, hubURL, attachDialClient, header)
	if err != nil {
		// A cancel during the handshake is the operator stopping the bridge, not
		// a failure to reach a hub.
		if ctx.Err() != nil {
			return nil
		}
		return fmt.Errorf("no hub at %s: %w", addr, err)
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

// loopbackAddr rewrites a wildcard bind address to loopback, since a client
// running on the hub host reaches the hub over loopback even when the hub
// advertises 0.0.0.0 or ::. The IPv6 wildcard maps to ::1 rather than
// 127.0.0.1: a hub bound IPv6-only is not listening on IPv4, so forcing the
// family there would fail the dial. Non-wildcard addresses pass through.
// "localhost" is rewritten to the literal 127.0.0.1 as well: dialing the NAME
// would hand a poisoned resolver or hosts entry the hub's capability token.
func loopbackAddr(addr string) string {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return addr
	}
	switch host {
	case "", "0.0.0.0", "localhost":
		return net.JoinHostPort("127.0.0.1", port)
	case "::":
		return net.JoinHostPort("::1", port)
	}
	return addr
}

// resolveHubURL validates addr and returns the ws:// URL to dial. The hub's
// capability token crosses this connection in cleartext, so the address must
// provably stay on this host — and the assembled URL is what has to be
// validated, not the host:port pair. net.SplitHostPort folds anything after an
// "@" into the port ("localhost:9180@attacker.example" splits as host
// "localhost" and port "9180@attacker.example"), so a check that trusts its
// host portion then dials the attacker's host once url.Parse strips the
// userinfo.
func resolveHubURL(addr string) (string, error) {
	refuse := func() (string, error) {
		return "", fmt.Errorf("%w: %q", errNonLoopbackAddr, addr)
	}
	u, err := url.Parse("ws://" + addr + "/rpc")
	if err != nil {
		return refuse()
	}
	if u.User != nil || u.Path != "/rpc" || u.RawQuery != "" || u.Fragment != "" {
		return refuse()
	}
	port, err := strconv.Atoi(u.Port())
	if err != nil || port < 1 || port > 65535 {
		return refuse()
	}
	if !isLoopbackHost(u.Hostname()) {
		return refuse()
	}
	return u.String(), nil
}

// isLoopbackHost reports whether host is a loopback address LITERAL. Names are
// deliberately not resolved here: loopbackAddr rewrites "localhost" to
// 127.0.0.1 before this is consulted, so a poisoned resolver or hosts entry
// cannot aim a token-carrying dial off-host.
func isLoopbackHost(host string) bool {
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
