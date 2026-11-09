package hub

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"time"

	"primeradiant.com/evener/cmd/evener-hub/internal/hostlock"
	"primeradiant.com/evener/internal/hubendpoint"
)

type endpointOptions struct {
	configPath     string
	configExplicit bool
	addr           string
	addrPresent    bool
}

// runEndpoint inspects one selected hub without acquiring its lock or touching credentials.
func runEndpoint(args []string, stdout, stderr io.Writer, deps mainDeps) error {
	opts := endpointOptions{configPath: DefaultConfigPath()}
	fs := flag.NewFlagSet("evener hub endpoint", flag.ContinueOnError)
	fs.SetOutput(stderr)
	jsonOutput := fs.Bool("json", false, "write a structured endpoint report")
	fs.StringVar(&opts.configPath, "config", opts.configPath, "hub.toml path")
	fs.StringVar(&opts.addr, "addr", "", "override configured listener")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 || !*jsonOutput {
		return errors.New("endpoint requires --json and no positional arguments")
	}
	set := setFlagNames(fs)
	opts.configExplicit = set["config"]
	opts.addrPresent = set["addr"]
	report, err := resolveHubEndpoint(context.Background(), opts, deps)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "[hub] endpoint: %v\n", err)
		return err
	}
	return json.NewEncoder(stdout).Encode(report)
}

// resolveHubEndpoint selects the state-root owner, then checks lock, record,
// numeric local socket and serving process identity without reading credentials.
func resolveHubEndpoint(ctx context.Context, opts endpointOptions, deps mainDeps) (hubendpoint.Report, error) {
	if ctx.Err() != nil {
		return hubendpoint.Report{}, ctx.Err()
	}
	var cfg Config
	var endpoint EndpointConfig
	var err error
	if deps.loadConfig == nil {
		cfg, endpoint, err = loadResolvedConfig(opts.configPath, opts.configExplicit, EndpointInput{opts.addr, opts.addrPresent})
	} else {
		cfg, err = deps.loadConfig(opts.configPath, opts.configExplicit)
		if err == nil {
			cfg, endpoint, err = resolveLoadedEndpointConfig(cfg, opts.configPath, EndpointInput{opts.addr, opts.addrPresent})
		}
	}
	if err != nil {
		return hubendpoint.Report{}, err
	}
	configPath, err := endpointConfigPath(opts.configPath, endpoint.ConfigExists)
	if err != nil {
		return hubendpoint.Report{}, err
	}
	if err := hostlock.CheckStorage(cfg.HubStateRoot, deps.inspectFilesystem); err != nil {
		return hubendpoint.Report{}, err
	}
	report := hubendpoint.Report{SchemaVersion: hubendpoint.SchemaVersion, ConfigPath: configPath, ConfigExists: endpoint.ConfigExists, HubStateRoot: cfg.HubStateRoot, ConfiguredListenAddr: endpoint.ListenAddr.String(), ListenSource: endpoint.ListenSource, State: "stopped"}
	if endpoint.PublicURL != nil {
		report.PublicURL = endpoint.PublicURL.String()
	}
	lockPath := filepath.Join(cfg.HubStateRoot, "hub.lock")
	held, err := hostlock.IsHeld(lockPath)
	if errors.Is(err, hostlock.ErrNonlocalStorage) {
		return hubendpoint.Report{}, err
	}
	if err != nil {
		return unavailableEndpoint(report, "lock_unavailable")
	}
	if !held {
		return report, nil
	}
	recordPath := filepath.Join(cfg.HubStateRoot, "hub-endpoint.json")
	beforeFile, beforeFileErr := os.Lstat(recordPath)
	record, err := hubendpoint.Read(cfg.HubStateRoot)
	if errors.Is(err, os.ErrNotExist) {
		report.State = "starting"
		report.Reason = "lock_held_without_record"
		return report, nil
	}
	if err != nil || beforeFileErr != nil {
		return unavailableEndpoint(report, "invalid_record")
	}
	if record.LocalURL == "" {
		report.State = "unavailable"
		report.Reason = "no_portable_local_origin"
		return report, nil
	}
	origin, err := hubendpoint.ParseOrigin(record.LocalURL)
	if err != nil || origin.Scheme() != "http" || !hubendpoint.LocalTarget(origin) {
		return unavailableEndpoint(report, "nonlocal_target")
	}
	dialer := net.Dialer{Timeout: 2 * time.Second}
	conn, err := dialer.DialContext(ctx, "tcp", net.JoinHostPort(origin.Hostname(), strconv.Itoa(int(origin.Port()))))
	if err != nil {
		if ctx.Err() != nil {
			return hubendpoint.Report{}, ctx.Err()
		}
		report.State = "unavailable"
		report.Reason = "unreachable"
		return report, nil
	}
	health, verifyErr := hubendpoint.VerifiedLocalHealth(ctx, conn, origin, record)
	_ = conn.Close()
	if verifyErr != nil {
		if ctx.Err() != nil {
			return hubendpoint.Report{}, ctx.Err()
		}
		report.State = "unavailable"
		if errors.Is(verifyErr, hubendpoint.ErrIdentityMismatch) {
			report.Reason = "identity_mismatch"
		} else {
			report.Reason = "unreachable"
		}
		return report, nil
	}
	after, err := hubendpoint.Read(cfg.HubStateRoot)
	afterFile, afterFileErr := os.Lstat(recordPath)
	if err != nil || afterFileErr != nil || !os.SameFile(beforeFile, afterFile) || !reflect.DeepEqual(record, after) {
		return unavailableEndpoint(report, "record_changed")
	}
	held, err = hostlock.IsHeld(lockPath)
	if errors.Is(err, hostlock.ErrNonlocalStorage) {
		return hubendpoint.Report{}, err
	}
	if err != nil || !held {
		return unavailableEndpoint(report, "lock_changed")
	}
	record.Version = health.Version
	record.BackendGitSHA = health.BackendGitSha
	report.State = "running"
	report.Running = &record
	return report, nil
}

// endpointConfigPath is shared by publication and inspection. Resolve in the
// owning process's cwd, before Abs can clean symlink/.. into a different file.
func endpointConfigPath(path string, exists bool) (string, error) {
	if exists {
		var err error
		path, err = filepath.EvalSymlinks(path)
		if err != nil {
			return "", err
		}
	}
	return filepath.Abs(path)
}

func unavailableEndpoint(report hubendpoint.Report, reason string) (hubendpoint.Report, error) {
	report.State = "unavailable"
	report.Reason = reason
	return report, nil
}
