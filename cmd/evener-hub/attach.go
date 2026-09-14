package hub

import (
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
	stdio      bool
	addr       string
	configPath string
}

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
	cfg, err := deps.loadConfig(opts.configPath)
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
	if err := proxyAppWire(context.Background(), addr, token, stream); err != nil {
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
	header := http.Header{}
	if token != "" {
		header.Set("Authorization", "Bearer "+token)
	}
	hubURL := "ws://" + addr + "/rpc"
	ws, err := appwire.DialWebSocketWithHeaders(ctx, hubURL, http.DefaultClient, header)
	if err != nil {
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
	err := <-errs
	_ = a.Close()
	_ = b.Close()
	if err == nil || errors.Is(err, io.EOF) || websocket.CloseStatus(err) == websocket.StatusNormalClosure {
		return nil
	}
	return err
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
// advertises 0.0.0.0 or ::. Non-wildcard addresses pass through unchanged.
func loopbackAddr(addr string) string {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return addr
	}
	switch host {
	case "", "0.0.0.0", "::":
		return net.JoinHostPort("127.0.0.1", port)
	}
	return addr
}
