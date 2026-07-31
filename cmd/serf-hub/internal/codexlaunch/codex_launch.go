package codexlaunch

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"primeradiant.com/serf/appwire"
	"primeradiant.com/serf/cmd/serf-hub/internal/appsource"
	"primeradiant.com/serf/envvars"
)

type CodexLaunchConfig struct {
	ID              string            `toml:"id"`
	Binary          string            `toml:"binary"`
	WorkingDir      string            `toml:"working_dir"`
	Listen          string            `toml:"listen"`
	Args            []string          `toml:"args"`
	Env             map[string]string `toml:"env"`
	Timeout         time.Duration     `toml:"timeout"`
	BearerToken     string            `toml:"bearer_token"`
	BearerTokenFile string            `toml:"bearer_token_file"`
}

type CodexLauncher struct {
	Mu          sync.Mutex
	configs     map[string]CodexLaunchConfig
	Running     map[string]*LaunchedCodex
	Sources     map[string]appsource.Source
	client      *http.Client
	process     func(string, ...string) launchProcess
	newTicker   func(time.Duration) launchTicker
	withTimeout func(context.Context, time.Duration) (context.Context, context.CancelFunc)
}

type LaunchedCodex struct {
	Cmd      *exec.Cmd
	process  launchProcess
	endpoint string
	// Exited is closed when the launched process exits (cmd.Wait returns). It is
	// a broadcast: any number of observers may select on it, repeatedly, without
	// consuming a single-shot signal.
	Exited <-chan struct{}
}

type launchProcess interface {
	Cmd() *exec.Cmd
	SetDir(string)
	SetEnv([]string)
	StdoutPipe() (io.ReadCloser, error)
	StderrPipe() (io.ReadCloser, error)
	Start() error
	Wait() error
	Kill() error
}

type execLaunchProcess struct{ cmd *exec.Cmd }

func (p *execLaunchProcess) Cmd() *exec.Cmd                     { return p.cmd }
func (p *execLaunchProcess) SetDir(dir string)                  { p.cmd.Dir = dir }
func (p *execLaunchProcess) SetEnv(env []string)                { p.cmd.Env = env }
func (p *execLaunchProcess) StdoutPipe() (io.ReadCloser, error) { return p.cmd.StdoutPipe() }
func (p *execLaunchProcess) StderrPipe() (io.ReadCloser, error) { return p.cmd.StderrPipe() }
func (p *execLaunchProcess) Start() error                       { return p.cmd.Start() }
func (p *execLaunchProcess) Wait() error                        { return p.cmd.Wait() }
func (p *execLaunchProcess) Kill() error {
	if p.cmd.Process == nil {
		return nil
	}
	return p.cmd.Process.Kill()
}

type launchTicker interface {
	C() <-chan time.Time
	Stop()
}

type realLaunchTicker struct{ ticker *time.Ticker }

func (t *realLaunchTicker) C() <-chan time.Time { return t.ticker.C }
func (t *realLaunchTicker) Stop()               { t.ticker.Stop() }

var newRequestWithContext = http.NewRequestWithContext

func NewCodexLauncher(configs []CodexLaunchConfig) *CodexLauncher {
	byID := make(map[string]CodexLaunchConfig, len(configs))
	for _, cfg := range configs {
		id := strings.TrimSpace(cfg.ID)
		if id == "" {
			id = "codex"
		}
		cfg.ID = id
		byID[id] = cfg
	}
	return &CodexLauncher{
		configs: byID,
		Running: map[string]*LaunchedCodex{},
		Sources: map[string]appsource.Source{},
		client:  http.DefaultClient,
		process: func(name string, args ...string) launchProcess {
			return &execLaunchProcess{cmd: exec.CommandContext(context.Background(), name, args...)}
		},
		newTicker: func(d time.Duration) launchTicker {
			return &realLaunchTicker{ticker: time.NewTicker(d)}
		},
		withTimeout: context.WithTimeout,
	}
}

func (l *CodexLauncher) EnsureSource(ctx context.Context, sourceID string, sources *appsource.Registry) (appsource.Source, error) {
	l.Mu.Lock()
	defer l.Mu.Unlock()
	if source, ok := l.cachedSourceLocked(sourceID, sources); ok {
		if sources != nil {
			sources.Add(source)
		}
		return source, nil
	}
	cfg, ok := l.configs[sourceID]
	if !ok {
		return nil, appwire.HubLaunchError("codex launch not configured: " + sourceID)
	}
	launched, err := l.launchLocked(ctx, cfg)
	if err != nil {
		return nil, err
	}
	source := appsource.NewCodexSource(appsource.CodexSourceConfig{
		ID:              sourceID,
		Endpoint:        launched.endpoint,
		BearerToken:     cfg.BearerToken,
		BearerTokenFile: cfg.BearerTokenFile,
	}, l.client)
	l.Running[sourceID] = launched
	l.Sources[sourceID] = source
	if sources != nil {
		sources.Add(source)
	}
	return source, nil
}

func (l *CodexLauncher) Manages(sourceID string) bool {
	l.Mu.Lock()
	defer l.Mu.Unlock()
	_, ok := l.configs[sourceID]
	return ok
}

func (l *CodexLauncher) cachedSourceLocked(sourceID string, sources *appsource.Registry) (appsource.Source, bool) {
	source, hasSource := l.Sources[sourceID]
	launched := l.Running[sourceID]
	if !hasSource {
		return nil, false
	}
	if launched != nil && !launchedCodexExited(launched) {
		return source, true
	}
	delete(l.Running, sourceID)
	delete(l.Sources, sourceID)
	if sources != nil {
		sources.Remove(sourceID)
	}
	return nil, false
}

func launchedCodexExited(launched *LaunchedCodex) bool {
	select {
	case <-launched.Exited:
		return true
	default:
		return false
	}
}

func (l *CodexLauncher) Shutdown(ctx context.Context) error {
	l.Mu.Lock()
	running := make([]*LaunchedCodex, 0, len(l.Running))
	for _, launched := range l.Running {
		running = append(running, launched)
	}
	l.Running = map[string]*LaunchedCodex{}
	l.Sources = map[string]appsource.Source{}
	l.Mu.Unlock()

	for _, launched := range running {
		if launched.process != nil {
			_ = launched.process.Kill()
		} else if launched.Cmd.Process != nil {
			_ = launched.Cmd.Process.Kill()
		}
		select {
		case <-launched.Exited:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

func (l *CodexLauncher) launchLocked(ctx context.Context, cfg CodexLaunchConfig) (*LaunchedCodex, error) {
	binary := strings.TrimSpace(cfg.Binary)
	if binary == "" {
		binary = "codex"
	}
	listen := strings.TrimSpace(cfg.Listen)
	if listen == "" {
		listen = "ws://127.0.0.1:0"
	}
	if !strings.HasPrefix(listen, "ws://") {
		return nil, appwire.HubLaunchError("hub-launched codex app-server requires websocket listen URL")
	}
	timeout := cfg.Timeout
	if timeout == 0 {
		timeout = 30 * time.Second
	}
	args := buildCodexLaunchArgs(binary, cfg.Args, listen)
	// NOT CommandContext: the launched codex app-server must outlive this
	// call's ctx (the caller owns it via LaunchedCodex). ctx scopes only the
	// readiness wait below; on timeout we kill the process explicitly.
	process := l.process(binary, args...) //nolint:noctx // detached app-server must outlive ctx (see comment)
	process.SetDir(cfg.WorkingDir)
	process.SetEnv(codexLaunchEnv(cfg.Env))
	stdout, err := process.StdoutPipe()
	if err != nil {
		return nil, appwire.HubLaunchError("prepare codex app-server stdout: " + err.Error())
	}
	stderr, err := process.StderrPipe()
	if err != nil {
		return nil, appwire.HubLaunchError("prepare codex app-server stderr: " + err.Error())
	}
	endpoints := make(chan string, 4)
	if err := process.Start(); err != nil {
		return nil, appwire.HubLaunchError("start codex app-server: " + err.Error())
	}
	go scanCodexEndpoint(stdout, endpoints)
	go scanCodexEndpoint(stderr, endpoints)
	// exitErr is published before close(exited); a receive on exited
	// happens-after the close, so reading exitErr after the receive is race-free.
	exited := make(chan struct{})
	var exitErr error
	go func() {
		exitErr = process.Wait()
		close(exited)
	}()

	waitCtx, cancel := l.withTimeout(ctx, timeout)
	defer cancel()
	endpoint := configuredCodexEndpoint(listen)
	ticker := l.newTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		if endpoint != "" && CodexReady(waitCtx, l.client, endpoint) {
			return &LaunchedCodex{Cmd: process.Cmd(), process: process, endpoint: endpoint, Exited: exited}, nil
		}
		select {
		case next := <-endpoints:
			if next != "" {
				endpoint = next
			}
		case <-exited:
			if exitErr != nil {
				return nil, appwire.HubLaunchError("codex app-server exited before ready: " + exitErr.Error())
			}
			return nil, appwire.HubLaunchError("codex app-server exited before ready")
		case <-ticker.C():
		case <-waitCtx.Done():
			_ = process.Kill()
			return nil, codexReadyWaitError(waitCtx)
		}
	}
}

// codexReadyWaitError says which way a ready-wait that never saw the
// app-server come up was stopped. Its context is done for two unrelated
// reasons — the launch's readiness budget elapsed, or the caller went away —
// and only the first is a timeout. Calling the second one sends an operator
// triaging it after a slow machine or a too-short launch timeout, when nothing
// was slow and nobody is waiting for the app-server any more (kata f9hr).
//
// The wait runs under the caller's context on every hub path that reaches it:
// EnsureSource is called from thread lifecycle handlers carrying a live
// request context — r.Context() on the REST spawn, the websocket connection's
// ctx (which the keepalive cancels) on the RPC one — so a client that drops
// mid-launch lands here.
//
// ctx.Err() separates the two outright, the same way the daemon path's
// launchCheckWaitError does: Canceled is the caller walking away,
// DeadlineExceeded is time genuinely running out — the launch's own budget, or
// a deadline the caller brought with it.
//
// Both stay an appwire.HubLaunchError, the discriminator clients read to
// headline the failure as a session that would not start. The label changes;
// the family of failure does not.
func codexReadyWaitError(ctx context.Context) error {
	if errors.Is(ctx.Err(), context.Canceled) {
		return appwire.HubLaunchError("codex app-server launch canceled waiting for ready")
	}
	return appwire.HubLaunchError("codex app-server timed out waiting for ready")
}

func buildCodexLaunchArgs(binary string, configured []string, listen string) []string {
	var args []string
	if configured != nil {
		args = append(args, configured...)
	} else if !strings.Contains(filepath.Base(binary), "codex-app-server") {
		args = append(args, "app-server")
	}
	if !argsContainFlag(args, "--listen") {
		args = append(args, "--listen", listen)
	}
	return args
}

func argsContainFlag(args []string, flag string) bool {
	for _, arg := range args {
		if arg == flag || strings.HasPrefix(arg, flag+"=") {
			return true
		}
	}
	return false
}

func codexLaunchEnv(overrides map[string]string) []string {
	env := append([]string{}, os.Environ()...)
	env = append(env, envvars.SERFHubSpawnedCodex.Assignment("1"))
	for key, value := range overrides {
		env = append(env, key+"="+value)
	}
	return env
}

func scanCodexEndpoint(r io.Reader, endpoints chan<- string) {
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		if endpoint, ok := ParseCodexEndpoint(scanner.Text()); ok {
			endpoints <- endpoint
		}
	}
}

func ParseCodexEndpoint(line string) (string, bool) {
	if endpoint, ok := parseCodexEndpointJSON(line); ok {
		return endpoint, true
	}
	idx := strings.Index(line, "ws://")
	if idx < 0 {
		return "", false
	}
	raw := line[idx:]
	if fields := strings.Fields(raw); len(fields) > 0 {
		raw = fields[0]
	}
	raw = strings.TrimRight(raw, ".,)")
	return validCodexEndpoint(raw)
}

func parseCodexEndpointJSON(line string) (string, bool) {
	var payload struct {
		Endpoint string `json:"endpoint"`
	}
	if err := json.Unmarshal([]byte(line), &payload); err != nil {
		return "", false
	}
	return validCodexEndpoint(payload.Endpoint)
}

func validCodexEndpoint(raw string) (string, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", false
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "ws" || u.Host == "" {
		return "", false
	}
	return raw, true
}

func configuredCodexEndpoint(listen string) string {
	u, err := url.Parse(listen)
	if err != nil || u.Scheme != "ws" || strings.HasSuffix(u.Host, ":0") {
		return ""
	}
	return listen
}

func CodexReady(ctx context.Context, client *http.Client, endpoint string) bool {
	readyURL, err := codexReadyURL(endpoint)
	if err != nil {
		return false
	}
	req, err := newRequestWithContext(ctx, http.MethodGet, readyURL, nil)
	if err != nil {
		return false
	}
	resp, err := client.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close() //nolint:errcheck // response body close on read path; error is not actionable
	return resp.StatusCode == http.StatusOK
}

func codexReadyURL(endpoint string) (string, error) {
	u, err := url.Parse(endpoint)
	if err != nil {
		return "", err
	}
	if u.Scheme != "ws" {
		return "", fmt.Errorf("unsupported codex endpoint scheme: %s", u.Scheme)
	}
	u.Scheme = "http"
	u.Path = "/readyz"
	u.RawQuery = ""
	u.Fragment = ""
	return u.String(), nil
}
