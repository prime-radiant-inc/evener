package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"primeradiant.com/serf/agent"
	"primeradiant.com/serf/cmdutil"
	"primeradiant.com/serf/internal/appwire"
	authopenai "primeradiant.com/serf/internal/auth/openai"
	"primeradiant.com/serf/internal/credentials"
	"primeradiant.com/serf/internal/launchconfig"
	"primeradiant.com/serf/rendezvous"
)

const serfLaunchCheckTimeout = 30 * time.Second

// SpawnRequest carries the per-spawn knobs passed directly from the caller.
type SpawnRequest struct {
	Resolved      launchconfig.Resolved
	WorkingDir    string
	StateDir      string
	RunDir        string
	AppReplaySize int
	Env           []string // populated by ToEnv during Spawn
	Provider      string   // for credential injection
}

// ResumeRequest carries the resolved state needed to resume a saved session.
type ResumeRequest struct {
	SessionID     string
	WorkingDir    string
	StateDir      string
	Resolved      launchconfig.Resolved
	RunDir        string
	AppReplaySize int
	Env           []string // populated by ToEnv during Resume
	Provider      string   // for credential injection
}

// HubSpawner fulfills the Spawner interface using SpawnDaemon.
type HubSpawner struct {
	Cfg        Config
	SerfBinary string // path to the serf binary; "" → "serf" on PATH
	RunDir     string
	HubToken   string
	Creds      *credentials.Store // credentials store for provider key injection
	StateRoot  string             // hub-level state root; used for resolving
}

type SerfLaunchModelLister interface {
	ListLaunchModels(context.Context) ([]appwire.ModelDescriptor, error)
}

type SerfLaunchModelContractLister interface {
	ListLaunchModelContract(context.Context) (appwire.ModelListResponse, error)
}

type SerfLaunchModelContractWorkingDirLister interface {
	ListLaunchModelContractForWorkingDir(context.Context, string) (appwire.ModelListResponse, error)
}

func (h *HubSpawner) ListLaunchModels(ctx context.Context) ([]appwire.ModelDescriptor, error) {
	resp, err := h.ListLaunchModelContract(ctx)
	if err != nil {
		return nil, err
	}
	return resp.Data, nil
}

func (h *HubSpawner) ListLaunchModelContract(ctx context.Context) (appwire.ModelListResponse, error) {
	stateDir := resolveSerfLaunchStateDir("", nil)
	env := launchconfig.ToEnv(launchconfig.EnvInputs{
		Resolved:  launchconfig.Resolved{},
		RunDir:    h.RunDir,
		StateDir:  stateDir,
		HubToken:  h.HubToken,
		Creds:     h.Creds,
		ParentEnv: os.Environ(),
	})
	return listSerfLaunchModelContract(ctx, h.SerfBinary, env)
}

func (h *HubSpawner) ListLaunchModelContractForWorkingDir(ctx context.Context, workingDir string) (appwire.ModelListResponse, error) {
	stateDir := resolveSerfLaunchStateDir(workingDir, nil)
	env := launchconfig.ToEnv(launchconfig.EnvInputs{
		Resolved:  launchconfig.Resolved{},
		RunDir:    h.RunDir,
		StateDir:  stateDir,
		HubToken:  h.HubToken,
		Creds:     h.Creds,
		ParentEnv: os.Environ(),
	})
	return listSerfLaunchModelContract(ctx, h.SerfBinary, env)
}

func (h *HubSpawner) Spawn(ctx context.Context, req SpawnRequest) (rendezvous.Entry, error) {
	timeout := h.Cfg.SpawnTimeout
	if timeout == 0 {
		timeout = 30 * time.Second
	}
	if req.StateDir == "" {
		req.StateDir = resolveSerfLaunchStateDir(req.WorkingDir, req.Resolved.Effective.Env)
	}
	req.RunDir = h.RunDir
	if req.Resolved.Effective.AppReplaySize != nil {
		req.AppReplaySize = *req.Resolved.Effective.AppReplaySize
	}
	req.Env = launchconfig.ToEnv(launchconfig.EnvInputs{
		Resolved:  req.Resolved,
		Provider:  req.Provider,
		Creds:     h.Creds,
		ParentEnv: os.Environ(),
		RunDir:    h.RunDir,
		StateDir:  req.StateDir,
		HubToken:  h.HubToken,
	})
	if err := validateProviderCredentials(req.Provider, h.Creds); err != nil {
		return rendezvous.Entry{}, err
	}
	if err := validateSerfLaunchContract(ctx, h.SerfBinary, req.Resolved.Effective.Model, req.Env); err != nil {
		return rendezvous.Entry{}, err
	}
	return SpawnDaemon(ctx, h.SerfBinary, h.RunDir, req, timeout)
}

func (h *HubSpawner) Resume(ctx context.Context, req ResumeRequest) (rendezvous.Entry, error) {
	timeout := h.Cfg.SpawnTimeout
	if timeout == 0 {
		timeout = 30 * time.Second
	}
	if req.StateDir == "" {
		req.StateDir = resolveSerfLaunchStateDir(req.WorkingDir, req.Resolved.Effective.Env)
	}
	req.RunDir = h.RunDir
	if req.Resolved.Effective.AppReplaySize != nil {
		req.AppReplaySize = *req.Resolved.Effective.AppReplaySize
	}
	req.Env = launchconfig.ToEnv(launchconfig.EnvInputs{
		Resolved:  req.Resolved,
		Provider:  req.Provider,
		Creds:     h.Creds,
		ParentEnv: os.Environ(),
		RunDir:    h.RunDir,
		StateDir:  req.StateDir,
		HubToken:  h.HubToken,
	})
	if req.Provider != "" {
		if err := validateProviderCredentials(req.Provider, h.Creds); err != nil {
			return rendezvous.Entry{}, err
		}
	}
	if err := validateSerfLaunchContract(ctx, h.SerfBinary, req.Resolved.Effective.Model, req.Env); err != nil {
		return rendezvous.Entry{}, err
	}
	return ResumeDaemon(ctx, h.SerfBinary, h.RunDir, req, timeout)
}

// buildSpawnArgs assembles the arg slice for `serf serve` from a SpawnRequest.
//
// Always passes --addr 127.0.0.1:0 so the daemon binds an ephemeral port,
// which it reports via its rendezvous file.
func buildSpawnArgs(req SpawnRequest) []string {
	args := []string{"--addr", "127.0.0.1:0"}
	if req.WorkingDir != "" {
		args = append(args, "--dir", req.WorkingDir)
	}
	if req.StateDir != "" {
		args = append(args, "--state-dir", req.StateDir)
	}
	if req.RunDir != "" {
		args = append(args, "--run-dir", req.RunDir)
	}
	if req.AppReplaySize > 0 {
		args = append(args, "--app-replay-size", fmt.Sprintf("%d", req.AppReplaySize))
	}
	args = append(args, launchconfig.ToArgs(req.Resolved)...)
	return args
}

// SpawnDaemon launches a `serf serve` subprocess from the given SpawnRequest,
// then waits up to timeout for its rendezvous file to appear.
//
// Returns the rendezvous Entry on success, or error on timeout / spawn failure.
// Caller does NOT manage the subprocess lifecycle — the spawned daemon
// runs independently and lives until killed or sent /shutdown.
func SpawnDaemon(ctx context.Context, serfBinary string, runDir string, req SpawnRequest, timeout time.Duration) (rendezvous.Entry, error) {
	if serfBinary == "" {
		serfBinary = "serf"
	}
	args := append([]string{"serve"}, buildSpawnArgs(req)...)

	cmd := exec.Command(serfBinary, args...)
	cmd.Env = req.Env
	cmd.Stdout = os.Stderr // forward to hub stderr for now
	cmd.Stderr = os.Stderr

	startedAt := time.Now()
	if err := cmd.Start(); err != nil {
		return rendezvous.Entry{}, fmt.Errorf("start daemon: %w", err)
	}
	exited := make(chan error, 1)
	go func() {
		exited <- cmd.Wait()
	}()

	waitCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	entry, err := waitForRendezvousOrExit(waitCtx, runDir, cmd.Process.Pid, exited, WithStartedAfter(startedAt))
	if err != nil {
		_ = cmd.Process.Kill()
		return rendezvous.Entry{}, fmt.Errorf("daemon spawn timed out: %w", err)
	}
	return entry, nil
}

// WaitOption configures WaitForRendezvous.
type WaitOption func(*waitConfig)

type waitConfig struct {
	startedAfter time.Time
}

// WithStartedAfter rejects rendezvous entries whose StartedAt is on or
// before t. Use this to defend against a recycled PID matching a stale
// entry from a previously-crashed daemon.
func WithStartedAfter(t time.Time) WaitOption {
	return func(c *waitConfig) { c.startedAfter = t }
}

// WaitForRendezvous polls runDir for a rendezvous Entry whose PID matches.
// Returns when found, or when ctx is canceled.
func WaitForRendezvous(ctx context.Context, runDir string, pid int, opts ...WaitOption) (rendezvous.Entry, error) {
	cfg := waitConfig{}
	for _, o := range opts {
		o(&cfg)
	}
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		entries, _ := rendezvous.List(runDir)
		for _, e := range entries {
			if e.PID != pid {
				continue
			}
			if !cfg.startedAfter.IsZero() && !e.StartedAt.After(cfg.startedAfter) {
				continue
			}
			return e, nil
		}
		select {
		case <-ctx.Done():
			return rendezvous.Entry{}, errors.New("timeout waiting for rendezvous")
		case <-ticker.C:
		}
	}
}

// ResumeDaemon launches `serf serve --resume <sessionID>` and waits for
// rendezvous. Returns the new daemon's rendezvous Entry.
//
// Note: resume always creates a NEW session_id (the daemon mints a fresh
// one). Caller resolves it via roster lookup after rendezvous appears.
func ResumeDaemon(ctx context.Context, serfBinary, runDir string, req ResumeRequest, timeout time.Duration) (rendezvous.Entry, error) {
	if serfBinary == "" {
		serfBinary = "serf"
	}
	args := []string{"serve", "--addr", "127.0.0.1:0", "--resume", req.SessionID}
	if req.WorkingDir != "" {
		args = append(args, "--dir", req.WorkingDir)
	}
	if req.StateDir != "" {
		args = append(args, "--state-dir", req.StateDir)
	}
	if req.RunDir != "" {
		args = append(args, "--run-dir", req.RunDir)
	}
	if req.AppReplaySize > 0 {
		args = append(args, "--app-replay-size", fmt.Sprintf("%d", req.AppReplaySize))
	}
	args = append(args, launchconfig.ToArgs(req.Resolved)...)
	cmd := exec.Command(serfBinary, args...)
	cmd.Env = req.Env
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	startedAt := time.Now()
	if err := cmd.Start(); err != nil {
		return rendezvous.Entry{}, fmt.Errorf("start daemon: %w", err)
	}
	exited := make(chan error, 1)
	go func() {
		exited <- cmd.Wait()
	}()
	waitCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	entry, err := waitForRendezvousOrExit(waitCtx, runDir, cmd.Process.Pid, exited, WithStartedAfter(startedAt))
	if err != nil {
		_ = cmd.Process.Kill()
		return rendezvous.Entry{}, fmt.Errorf("resume timed out: %w", err)
	}
	return entry, nil
}

func resolveSerfStateDir(workDir, override string) string {
	return resolveSerfStateDirWithStateHome(workDir, override, "")
}

func resolveSerfLaunchStateDir(workDir string, env map[string]string) string {
	if env == nil {
		return resolveSerfStateDir(workDir, "")
	}
	return resolveSerfStateDirWithStateHome(workDir, env["SERF_STATE_DIR"], env["XDG_STATE_HOME"])
}

func resolveSerfStateDirWithStateHome(workDir, override, stateHome string) string {
	if strings.TrimSpace(override) != "" {
		return override
	}
	wd := strings.TrimSpace(workDir)
	if wd == "" {
		if got, err := os.Getwd(); err == nil {
			wd = got
		}
	}
	return agent.RuntimeDirWithStateHome(cmdutil.GitOriginURLFromDir(wd), wd, "", strings.TrimSpace(stateHome))
}

func setEnvValue(env []string, key, value string) []string {
	prefix := key + "="
	for i, kv := range env {
		if strings.HasPrefix(kv, prefix) {
			env[i] = prefix + value
			return env
		}
	}
	return append(env, prefix+value)
}

// validateProviderCredentials checks that the credentials store has a value
// for the given provider. Providers listed with auth mode "none" (e.g. ollama)
// are always accepted. A configured provider with no resolvable credential
// returns a structured launch error.
//
// If store is nil, credential validation is skipped (the spawned process
// inherits env credentials or will fail at the LLM provider level instead).
func validateProviderCredentials(provider string, store *credentials.Store) error {
	if provider == "" || store == nil {
		return nil
	}
	if strings.EqualFold(strings.TrimSpace(provider), "openai") && openAIStoredOAuthActive() {
		return nil
	}
	// Use List() so providers that need no credentials (e.g. ollama) are
	// correctly identified via their SourceNone status.
	for _, p := range store.List() {
		if p.Name != strings.ToLower(provider) {
			continue
		}
		if p.Source == credentials.SourceNone {
			return nil
		}
		if p.Source == credentials.SourceAbsent {
			return appwire.HubLaunchError(fmt.Sprintf("provider credentials missing for %s: set via serf/auth/apiKey/set or set the matching env var", provider))
		}
		return nil
	}
	// Unknown provider — don't block launch.
	return nil
}

func openAIStoredOAuthActive() bool {
	status, err := authopenai.NewService(authopenai.DefaultConfig(), nil).Status(authopenai.DefaultStateDirWithStateHome(""))
	if err != nil {
		return false
	}
	return status.SignedIn && status.Source == authopenai.AuthSourceOAuth && !status.NeedsLogin
}

func envToMap(env []string) map[string]string {
	out := make(map[string]string, len(env))
	for _, kv := range env {
		key, value, ok := strings.Cut(kv, "=")
		if !ok {
			continue
		}
		out[key] = value
	}
	return out
}

func validateSerfLaunchContract(ctx context.Context, serfBinary, model string, env []string) error {
	if serfBinary == "" {
		serfBinary = "serf"
	}
	args := []string{"launch-check", "--protocol", appwire.ProtocolVersion, "--json"}
	if strings.TrimSpace(model) != "" {
		args = append(args, "--model", model)
	}
	checkCtx, cancel := context.WithTimeout(ctx, serfLaunchCheckTimeout)
	defer cancel()
	cmd := exec.CommandContext(checkCtx, serfBinary, args...)
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	if checkCtx.Err() != nil {
		return appwire.HubLaunchError("serf launch-check timed out")
	}
	if err != nil {
		msg := strings.TrimSpace(redactEnvSecrets(string(out), env))
		if msg == "" {
			msg = err.Error()
		}
		return appwire.HubLaunchError("serf launch-check failed: " + msg)
	}
	var resp struct {
		Protocol string `json:"protocol"`
	}
	if err := json.NewDecoder(bytes.NewReader(out)).Decode(&resp); err != nil {
		return appwire.HubLaunchError("serf launch-check returned invalid response")
	}
	if resp.Protocol != appwire.ProtocolVersion {
		return appwire.HubLaunchError(fmt.Sprintf("serf launch-check protocol %q does not match Hub protocol %q", resp.Protocol, appwire.ProtocolVersion))
	}
	return nil
}

func listSerfLaunchModels(ctx context.Context, serfBinary string, env []string) ([]appwire.ModelDescriptor, error) {
	resp, err := listSerfLaunchModelContract(ctx, serfBinary, env)
	if err != nil {
		return nil, err
	}
	return resp.Data, nil
}

func listSerfLaunchModelContract(ctx context.Context, serfBinary string, env []string) (appwire.ModelListResponse, error) {
	if serfBinary == "" {
		serfBinary = "serf"
	}
	checkCtx, cancel := context.WithTimeout(ctx, serfLaunchCheckTimeout)
	defer cancel()
	cmd := exec.CommandContext(checkCtx, serfBinary, "launch-check", "--protocol", appwire.ProtocolVersion, "--json", "--models")
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	if checkCtx.Err() != nil {
		return appwire.ModelListResponse{}, appwire.HubLaunchError("serf launch-check timed out")
	}
	if err != nil {
		msg := strings.TrimSpace(redactEnvSecrets(string(out), env))
		if msg == "" {
			msg = err.Error()
		}
		return appwire.ModelListResponse{}, appwire.HubLaunchError("serf launch-check failed: " + msg)
	}
	var resp struct {
		Protocol    string                        `json:"protocol"`
		Models      []appwire.ModelDescriptor     `json:"models"`
		Diagnostics []appwire.ModelListDiagnostic `json:"diagnostics"`
	}
	if err := json.NewDecoder(bytes.NewReader(out)).Decode(&resp); err != nil {
		return appwire.ModelListResponse{}, appwire.HubLaunchError("serf launch-check returned invalid response")
	}
	if resp.Protocol != appwire.ProtocolVersion {
		return appwire.ModelListResponse{}, appwire.HubLaunchError(fmt.Sprintf("serf launch-check protocol %q does not match Hub protocol %q", resp.Protocol, appwire.ProtocolVersion))
	}
	models := make([]appwire.ModelDescriptor, 0, len(resp.Models))
	for _, model := range resp.Models {
		provider := strings.TrimSpace(model.Provider)
		name := strings.TrimSpace(model.Model)
		if provider == "" || name == "" {
			continue
		}
		models = append(models, appwire.ModelDescriptor{Provider: provider, Model: name})
	}
	diagnostics := make([]appwire.ModelListDiagnostic, 0, len(resp.Diagnostics))
	for _, diag := range resp.Diagnostics {
		diag.Provider = strings.TrimSpace(diag.Provider)
		diag.Source = strings.TrimSpace(diag.Source)
		diag.Title = strings.TrimSpace(diag.Title)
		diag.Message = strings.TrimSpace(diag.Message)
		diag.Hint = strings.TrimSpace(diag.Hint)
		if diag.Message == "" {
			continue
		}
		diagnostics = append(diagnostics, diag)
	}
	return appwire.ModelListResponse{Data: models, Diagnostics: diagnostics}, nil
}

func redactEnvSecrets(text string, env []string) string {
	for key, value := range envToMap(env) {
		if !isSensitiveEnvKey(key) || len(value) < 8 {
			continue
		}
		text = strings.ReplaceAll(text, value, "[redacted]")
	}
	return text
}

func isSensitiveEnvKey(key string) bool {
	key = strings.ToUpper(key)
	return strings.Contains(key, "KEY") ||
		strings.Contains(key, "TOKEN") ||
		strings.Contains(key, "SECRET") ||
		strings.Contains(key, "PASSWORD") ||
		strings.Contains(key, "CREDENTIAL")
}

func waitForRendezvousOrExit(ctx context.Context, runDir string, pid int, exited <-chan error, opts ...WaitOption) (rendezvous.Entry, error) {
	cfg := waitConfig{}
	for _, o := range opts {
		o(&cfg)
	}
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		entries, _ := rendezvous.List(runDir)
		for _, e := range entries {
			if e.PID != pid {
				continue
			}
			if !cfg.startedAfter.IsZero() && !e.StartedAt.After(cfg.startedAfter) {
				continue
			}
			return e, nil
		}
		select {
		case <-ctx.Done():
			return rendezvous.Entry{}, errors.New("timeout waiting for rendezvous")
		case err := <-exited:
			if err != nil {
				return rendezvous.Entry{}, fmt.Errorf("process exited before rendezvous: %w", err)
			}
			return rendezvous.Entry{}, errors.New("process exited before rendezvous")
		case <-ticker.C:
		}
	}
}
