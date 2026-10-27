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
	"primeradiant.com/evener/internal/hubendpoint"
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
	fs.StringVar(&opts.addr, "addr", "", "hub loopback address (default: verified running endpoint)")
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
	opts.configExplicit = setFlagNames(fs)["config"]
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
	ctx, cancel := context.WithTimeout(context.Background(), opts.timeout)
	defer cancel()
	// Validate explicit destinations before endpoint discovery or credentials.
	if opts.addr != "" {
		if _, err := attachOrigin("http://" + opts.addr); err != nil {
			_, _ = fmt.Fprintf(stderr, "[hub] %v\n", err)
			return err
		}
	}
	endpointOpts := endpointOptions{configPath: opts.configPath, configExplicit: opts.configExplicit}
	report, err := resolveHubEndpoint(ctx, endpointOpts, deps)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "[hub] endpoint: %v\n", err)
		return err
	}
	if report.Running == nil {
		err := fmt.Errorf("%w: selected hub is %s (%s)", errNoHubAt, report.State, report.Reason)
		_, _ = fmt.Fprintf(stderr, "[hub] %v\n", err)
		return err
	}
	origin, err := attachOrigin(report.Running.LoopbackURL)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "[hub] %v\n", err)
		return err
	}
	if opts.addr != "" {
		selected, _ := attachOrigin("http://" + opts.addr)
		if selected.String() != origin.String() {
			_, _ = fmt.Fprintf(stderr, "[hub] %v\n", hubendpoint.ErrIdentityMismatch)
			return hubendpoint.ErrIdentityMismatch
		}
		origin = selected
	}
	configIdentity, err := selectRestartConfigIdentity(endpointOpts, report)
	if err != nil {
		err = fmt.Errorf("%w: selected config: %w", hubendpoint.ErrIdentityMismatch, err)
		_, _ = fmt.Fprintf(stderr, "[hub] %v\n", err)
		return err
	}
	token, err := readAuthToken(report.Running.HubStateRoot)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "[hub] %v\n", err)
		return err
	}
	baseline, err := readHubStartedAt(ctx, origin)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "[hub] health: %v\n", err)
		return err
	}

	client, err := dialHubControl(ctx, origin, token, *report.Running)
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

	if err := waitForNewIncarnation(ctx, endpointOpts, deps, report, configIdentity, baseline); err != nil {
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

func dialHubControl(ctx context.Context, origin hubendpoint.Origin, token string, record hubendpoint.Record) (*appwire.Client, error) {
	httpClient, err := hubendpoint.HTTPClientWithBearer(nil, token, origin, &record)
	if err != nil {
		return nil, err
	}
	dialCtx, cancel := context.WithTimeout(ctx, attachDialTimeout)
	defer cancel()
	ws, err := appwire.DialWebSocketWithHeaders(dialCtx, origin.RPCURL(), httpClient, nil)
	if err != nil {
		return nil, fmt.Errorf("%w %s: %w", errNoHubAt, origin.String(), err)
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
var restartHealthClient = &http.Client{
	Transport: &http.Transport{Proxy: nil},
	CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	},
}

func readHubStartedAt(ctx context.Context, origin hubendpoint.Origin) (time.Time, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, origin.String()+"/api/health", nil)
	if err != nil {
		return time.Time{}, err
	}
	resp, err := restartHealthClient.Do(req)
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

func waitForNewIncarnation(ctx context.Context, opts endpointOptions, deps mainDeps, selected hubendpoint.Report, configIdentity restartConfigIdentity, previous time.Time) error {
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("hub did not restart: %w", ctx.Err())
		case <-ticker.C:
			// The replacement may bind a different configured listener. Pin the
			// selected config/root, not the predecessor's address.
			report, err := resolveHubEndpoint(ctx, opts, deps)
			if err != nil {
				continue
			}
			if !configIdentity.matchesConfirmation(report.ConfigPath) || (configIdentity.file == nil && report.ConfigExists) || report.HubStateRoot != selected.HubStateRoot {
				return hubendpoint.ErrIdentityMismatch
			}
			if report.Running == nil {
				continue
			}
			if !configIdentity.matchesConfirmation(report.Running.ConfigPath) || report.Running.HubStateRoot != selected.HubStateRoot {
				return hubendpoint.ErrIdentityMismatch
			}
			origin, err := attachOrigin(report.Running.LoopbackURL)
			if err != nil {
				return err
			}
			next, err := readHubStartedAt(ctx, origin)
			if err == nil && !next.IsZero() && next.Equal(report.Running.StartedAt) && !next.Equal(previous) {
				return nil
			}
		}
	}
}

type restartConfigIdentity struct {
	file          os.FileInfo
	canonicalPath string
	absentPath    string
}

func selectRestartConfigIdentity(opts endpointOptions, report hubendpoint.Report) (restartConfigIdentity, error) {
	file, err := os.Stat(report.ConfigPath)
	if err == nil {
		identity := restartConfigIdentity{file: file, canonicalPath: report.ConfigPath}
		if !report.ConfigExists || report.Running == nil || !identity.matches(report.Running.ConfigPath) || report.Running.HubStateRoot != report.HubStateRoot {
			return restartConfigIdentity{}, hubendpoint.ErrIdentityMismatch
		}
		return identity, nil
	}
	if !errors.Is(err, os.ErrNotExist) || opts.configExplicit || report.ConfigExists {
		return restartConfigIdentity{}, err
	}
	defaultPath, defaultErr := endpointConfigPath(DefaultConfigPath(), false)
	selectedPath, selectedErr := endpointConfigPath(opts.configPath, false)
	identity := restartConfigIdentity{absentPath: defaultPath}
	if defaultErr != nil || selectedErr != nil || selectedPath != defaultPath || report.ConfigPath != defaultPath || !identity.matches(report.ConfigPath) || (report.Running != nil && !identity.matches(report.Running.ConfigPath)) {
		return restartConfigIdentity{}, hubendpoint.ErrIdentityMismatch
	}
	return identity, nil
}

func (selected restartConfigIdentity) matchesConfirmation(path string) bool {
	if selected.file != nil && path == selected.canonicalPath {
		// Startup may atomically prune host metadata at the same canonical
		// pathname, but must leave a regular file rather than a symlink or other
		// type. Another pathname still needs the frozen file identity.
		current, err := os.Lstat(path)
		return err == nil && current.Mode().IsRegular()
	}
	return selected.matches(path)
}

func (selected restartConfigIdentity) matches(path string) bool {
	if selected.file != nil {
		current, err := os.Stat(path)
		return err == nil && os.SameFile(selected.file, current)
	}
	// A file-less selection has no inode. Pin its default pathname and require
	// absence, including no dangling symlink, for the whole confirmation.
	defaultPath, err := endpointConfigPath(DefaultConfigPath(), false)
	if err != nil || selected.absentPath == "" || path != selected.absentPath || defaultPath != selected.absentPath {
		return false
	}
	_, err = os.Lstat(path)
	return errors.Is(err, os.ErrNotExist)
}
