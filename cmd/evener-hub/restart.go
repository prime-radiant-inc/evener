package hub

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/hubapi"
)

type restartOptions struct {
	addr           string
	configPath     string
	configExplicit bool
	timeout        time.Duration
}

func parseRestartOptions(args []string, stderr io.Writer) (restartOptions, error) {
	opts := restartOptions{configPath: DefaultConfigPath(), timeout: 30 * time.Second}
	fs := flag.NewFlagSet("evener hub restart", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.StringVar(&opts.addr, "addr", "", "override hub loopback address (default: hub.toml addr, else 127.0.0.1:9180)")
	fs.StringVar(&opts.configPath, "config", opts.configPath, "path to hub.toml")
	fs.DurationVar(&opts.timeout, "timeout", opts.timeout, "how long to wait for the new hub process")
	fs.Usage = func() {
		_, _ = fmt.Fprintf(stderr, "Usage: evener hub restart [--addr host:port] [--config path] [--timeout duration]\n\n")
		_, _ = fmt.Fprintf(stderr, "Restart the local hub into the binary at its launch path. The hub verifies\nand execs that binary in place; this command waits until the new process answers.\n\n")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return opts, err
	}
	if fs.NArg() != 0 {
		return opts, fmt.Errorf("unexpected arguments: %s", strings.Join(fs.Args(), " "))
	}
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "config" {
			opts.configExplicit = true
		}
	})
	return opts, nil
}

// runHubRestart is the client half of evener/hub/restart. It captures the
// current process incarnation, asks the hub to exec the on-disk binary, and
// waits for a different incarnation to answer. A lost response is ambiguous:
// the same wait decides whether the new process came up.
func runHubRestart(args []string, stderr io.Writer, deps mainDeps) error {
	opts, err := parseRestartOptions(args, stderr)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		_, _ = fmt.Fprintf(stderr, "[hub] %v\n", err)
		return err
	}
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
	// Validate the dialable address before reading the credential or sending
	// the health probe: resolveHubURL is what rejects userinfo and non-loopback
	// hosts, and the health probe carries the bearer token.
	if _, err := resolveHubURL(addr); err != nil {
		_, _ = fmt.Fprintf(stderr, "[hub] %v\n", err)
		return err
	}
	token, err := readAuthToken(cfg.HubStateRoot)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "[hub] %v\n", err)
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), opts.timeout)
	defer cancel()
	baseline, err := readHubStartedAt(ctx, addr)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "[hub] health: %v\n", err)
		return err
	}

	client, err := dialHubControl(ctx, addr, token)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "[hub] %v\n", err)
		return err
	}
	defer func() { _ = client.Close() }()

	var resp appwire.HubRestartResponse
	requestErr := client.Request(ctx, appwire.MethodEvenerHubRestart, appwire.HubRestartParams{}, &resp)
	if requestErr != nil && !isAmbiguousRestartFailure(requestErr) {
		_, _ = fmt.Fprintf(stderr, "[hub] restart: %v\n", requestErr)
		return requestErr
	}

	stdout := deps.stdout
	if stdout == nil {
		stdout = os.Stdout
	}
	if requestErr == nil {
		_, _ = fmt.Fprintf(stdout, "restarting hub into %s\n", resp.TargetVersion)
		if !resp.Incarnation.IsZero() {
			baseline = resp.Incarnation
		}
	}

	if err := waitForNewIncarnation(ctx, addr, baseline); err != nil {
		confirmErr := err
		if requestErr != nil {
			confirmErr = fmt.Errorf("%w; %w", requestErr, err)
		}
		_, _ = fmt.Fprintf(stderr, "[hub] restart confirmation: %v\n", confirmErr)
		return confirmErr
	}
	_, _ = fmt.Fprintln(stdout, "hub restarted")
	return nil
}

// isAmbiguousRestartFailure reports a transport loss: the request may have
// reached the hub and the response may have been lost while the process
// exec'd. Server refusals and requests that never left are not ambiguous;
// every other error is, because the client cannot prove the frame stayed put.
func isAmbiguousRestartFailure(err error) bool {
	if _, ok := errors.AsType[appwire.TransportFailureError](err); ok {
		return true
	}
	if _, ok := errors.AsType[appwire.RequestNotSentError](err); ok {
		return false
	}
	if _, ok := errors.AsType[appwire.WireError](err); ok {
		return false
	}
	return true
}

func dialHubControl(ctx context.Context, addr, token string) (*appwire.Client, error) {
	hubURL, err := resolveHubURL(addr)
	if err != nil {
		return nil, err
	}
	header := http.Header{}
	if token != "" {
		header.Set("Authorization", "Bearer "+token)
	}
	dialCtx, cancel := context.WithTimeout(ctx, attachDialTimeout)
	defer cancel()
	ws, err := appwire.DialWebSocketWithHeaders(dialCtx, hubURL, attachDialClient, header)
	if err != nil {
		return nil, fmt.Errorf("%w %s: %w", errNoHubAt, addr, err)
	}
	client := appwire.NewClient(ws)
	client.Start(ctx)
	if _, err := client.Initialize(ctx, appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		_ = client.Close()
		return nil, err
	}
	return client, nil
}

// readHubStartedAt probes the auth-exempt health endpoint. It deliberately
// sends no credential: there is nothing to authenticate, and a misconfigured
// address must never receive the hub token.
func readHubStartedAt(ctx context.Context, addr string) (time.Time, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+addr+"/api/health", nil)
	if err != nil {
		return time.Time{}, err
	}
	resp, err := attachDialClient.Do(req)
	if err != nil {
		return time.Time{}, err
	}
	defer resp.Body.Close() //nolint:errcheck // read-only health probe
	if resp.StatusCode != http.StatusOK {
		return time.Time{}, fmt.Errorf("health: %s", resp.Status)
	}
	var health hubapi.HealthResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&health); err != nil {
		return time.Time{}, err
	}
	return health.StartedAt, nil
}

func waitForNewIncarnation(ctx context.Context, addr string, previous time.Time) error {
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("hub did not restart: %w", ctx.Err())
		case <-ticker.C:
			next, err := readHubStartedAt(ctx, addr)
			if err == nil && !next.Equal(previous) {
				return nil
			}
		}
	}
}
