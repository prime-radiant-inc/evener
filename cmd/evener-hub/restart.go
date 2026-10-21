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
	token, err := readAuthToken(cfg.HubStateRoot)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "[hub] %v\n", err)
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), opts.timeout)
	defer cancel()
	baseline, err := readHubStartedAt(ctx, addr, token)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "[hub] health: %v\n", err)
		return err
	}

	client, err := dialHubControl(ctx, addr, token)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "[hub] %v\n", err)
		return err
	}
	defer client.Close()

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
	}

	if err := waitForNewIncarnation(ctx, addr, token, baseline); err != nil {
		if requestErr != nil {
			return fmt.Errorf("%w; %v", requestErr, err)
		}
		return err
	}
	_, _ = fmt.Fprintln(stdout, "hub restarted")
	return nil
}

// isAmbiguousRestartFailure reports a transport loss: the request may have
// reached the hub and the response may have been lost while the process
// exec'd. Server refusals are not ambiguous.
func isAmbiguousRestartFailure(err error) bool {
	var transport appwire.TransportFailureError
	return errors.As(err, &transport)
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
		return nil, fmt.Errorf("%w %s: %v", errNoHubAt, addr, err)
	}
	client := appwire.NewClient(ws)
	client.Start(ctx)
	if _, err := client.Initialize(ctx, appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		client.Close()
		return nil, err
	}
	return client, nil
}

func readHubStartedAt(ctx context.Context, addr, token string) (time.Time, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+addr+"/api/health", nil)
	if err != nil {
		return time.Time{}, err
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
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

func waitForNewIncarnation(ctx context.Context, addr, token string, previous time.Time) error {
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("hub did not restart: %w", ctx.Err())
		case <-ticker.C:
			next, err := readHubStartedAt(ctx, addr, token)
			if err == nil && !next.Equal(previous) {
				return nil
			}
		}
	}
}
