package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"primeradiant.com/serf/agent"
	"primeradiant.com/serf/cmdutil"
	"primeradiant.com/serf/internal/appwire"
	authopenai "primeradiant.com/serf/internal/auth/openai"
	"primeradiant.com/serf/internal/credentials"
	"primeradiant.com/serf/internal/launchconfig"
	"primeradiant.com/serf/internal/providerconfig"
	"primeradiant.com/serf/rendezvous"
)

const serfLaunchCheckTimeout = 30 * time.Second
const daemonLaunchStderrLimit = 64 * 1024

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
	Cfg                 Config
	SerfBinary          string // path to the serf binary; "" → "serf" on PATH
	RunDir              string
	HubToken            string
	Creds               *credentials.Store // credentials store for provider key injection
	StateRoot           string             // hub-level state root; used for resolving
	ProvidersConfigPath string             // path of the providers.toml the hub loaded
	// LaunchDefaults are ambient defaults applied to hub-spawned daemons after
	// layered launch config resolves. Explicit launch config still wins.
	LaunchDefaults launchconfig.Layer
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
		Resolved:            launchconfig.Resolved{},
		RunDir:              h.RunDir,
		StateDir:            stateDir,
		HubToken:            h.HubToken,
		Creds:               h.Creds,
		ParentEnv:           os.Environ(),
		ProvidersConfigPath: h.ProvidersConfigPath,
	})
	return listSerfLaunchModelContract(ctx, h.SerfBinary, env)
}

func (h *HubSpawner) ListLaunchModelContractForWorkingDir(ctx context.Context, workingDir string) (appwire.ModelListResponse, error) {
	stateDir := resolveSerfLaunchStateDir(workingDir, nil)
	env := launchconfig.ToEnv(launchconfig.EnvInputs{
		Resolved:            launchconfig.Resolved{},
		RunDir:              h.RunDir,
		StateDir:            stateDir,
		HubToken:            h.HubToken,
		Creds:               h.Creds,
		ParentEnv:           os.Environ(),
		ProvidersConfigPath: h.ProvidersConfigPath,
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
	req.Resolved = applyLaunchDefaultsForSpawn(req.Resolved, h.LaunchDefaults)
	resolved, cleanup, err := prepareResolvedForSpawn(req.StateDir, req.Resolved)
	if err != nil {
		return rendezvous.Entry{}, err
	}
	defer cleanup()
	req.Resolved = resolved
	req.RunDir = h.RunDir
	if req.Resolved.Effective.AppReplaySize != nil {
		req.AppReplaySize = *req.Resolved.Effective.AppReplaySize
	}
	req.Env = launchconfig.ToEnv(launchconfig.EnvInputs{
		Resolved:            req.Resolved,
		Provider:            req.Provider,
		Creds:               h.Creds,
		ParentEnv:           os.Environ(),
		RunDir:              h.RunDir,
		StateDir:            req.StateDir,
		HubToken:            h.HubToken,
		ProvidersConfigPath: h.ProvidersConfigPath,
	})
	if err := validateProviderCredentials(req.Provider, h.Creds, req.Env, h.ProvidersConfigPath); err != nil {
		return rendezvous.Entry{}, err
	}
	if err := validateSerfLaunchContract(ctx, h.SerfBinary, req.Resolved.Effective.Model, req.Env); err != nil {
		return rendezvous.Entry{}, err
	}
	return SpawnDaemon(ctx, h.SerfBinary, h.RunDir, req, timeout)
}

func applyLaunchDefaultsForSpawn(resolved launchconfig.Resolved, defaults launchconfig.Layer) launchconfig.Resolved {
	if len(resolved.Effective.PluginDirs) == 0 && len(defaults.PluginDirs) > 0 {
		resolved.Effective.PluginDirs = append([]string(nil), defaults.PluginDirs...)
	}
	return resolved
}

func (h *HubSpawner) Resume(ctx context.Context, req ResumeRequest) (rendezvous.Entry, error) {
	timeout := h.Cfg.SpawnTimeout
	if timeout == 0 {
		timeout = 30 * time.Second
	}
	if req.StateDir == "" {
		req.StateDir = resolveSerfLaunchStateDir(req.WorkingDir, req.Resolved.Effective.Env)
	}
	resolved, cleanup, err := prepareResolvedForSpawn(req.StateDir, req.Resolved)
	if err != nil {
		return rendezvous.Entry{}, err
	}
	defer cleanup()
	req.Resolved = resolved
	req.RunDir = h.RunDir
	if req.Resolved.Effective.AppReplaySize != nil {
		req.AppReplaySize = *req.Resolved.Effective.AppReplaySize
	}
	req.Env = launchconfig.ToEnv(launchconfig.EnvInputs{
		Resolved:            req.Resolved,
		Provider:            req.Provider,
		Creds:               h.Creds,
		ParentEnv:           os.Environ(),
		RunDir:              h.RunDir,
		StateDir:            req.StateDir,
		HubToken:            h.HubToken,
		ProvidersConfigPath: h.ProvidersConfigPath,
	})
	if req.Provider != "" {
		if err := validateProviderCredentials(req.Provider, h.Creds, req.Env, h.ProvidersConfigPath); err != nil {
			return rendezvous.Entry{}, err
		}
	}
	if err := validateSerfLaunchContract(ctx, h.SerfBinary, req.Resolved.Effective.Model, req.Env); err != nil {
		return rendezvous.Entry{}, err
	}
	return ResumeDaemon(ctx, h.SerfBinary, h.RunDir, req, timeout)
}

func prepareResolvedForSpawn(stateDir string, resolved launchconfig.Resolved) (launchconfig.Resolved, func(), error) {
	effective := &resolved.Effective
	if effective.SystemPromptMode != "inline" && effective.SystemPromptAppendMode != "inline" {
		return resolved, func() {}, nil
	}
	if stateDir == "" {
		return launchconfig.Resolved{}, nil, errors.New("state dir is required for inline system prompts")
	}
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return launchconfig.Resolved{}, nil, fmt.Errorf("create state dir for inline prompts: %w", err)
	}
	tempDir, err := os.MkdirTemp(stateDir, "inline-prompts-")
	if err != nil {
		return launchconfig.Resolved{}, nil, fmt.Errorf("create inline prompt dir: %w", err)
	}
	cleanupPartial := func() { _ = os.RemoveAll(tempDir) }
	writePrompt := func(name, text string) (string, error) {
		path := filepath.Join(tempDir, name)
		if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
			return "", err
		}
		return path, nil
	}

	if effective.SystemPromptMode == "inline" {
		path, err := writePrompt("system-prompt.md", effective.SystemPromptText)
		if err != nil {
			cleanupPartial()
			return launchconfig.Resolved{}, nil, fmt.Errorf("write inline system prompt: %w", err)
		}
		effective.SystemPromptMode = "file"
		effective.SystemPromptFile = path
		effective.SystemPromptText = ""
	}
	if effective.SystemPromptAppendMode == "inline" {
		path, err := writePrompt("system-prompt-append.md", effective.SystemPromptAppendText)
		if err != nil {
			cleanupPartial()
			return launchconfig.Resolved{}, nil, fmt.Errorf("write inline system prompt append: %w", err)
		}
		effective.SystemPromptAppendMode = "file"
		effective.SystemPromptAppendFile = path
		effective.SystemPromptAppendText = ""
	}
	// Once preparation succeeds, the daemon/session state directory owns these
	// files. They must remain available after the hub RPC returns because the
	// daemon can reuse the resolved session config later.
	return resolved, func() {}, nil
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
	var stderr tailBuffer
	stderr.limit = daemonLaunchStderrLimit
	cmd.Stdout = os.Stderr // forward to hub stderr for now
	cmd.Stderr = io.MultiWriter(os.Stderr, &stderr)

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
		return rendezvous.Entry{}, launchFailureError("daemon spawn timed out", err, stderr.String())
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
	var stderr tailBuffer
	stderr.limit = daemonLaunchStderrLimit
	cmd.Stdout = os.Stderr
	cmd.Stderr = io.MultiWriter(os.Stderr, &stderr)
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
		return rendezvous.Entry{}, launchFailureError("resume timed out", err, stderr.String())
	}
	return entry, nil
}

type tailBuffer struct {
	buf   []byte
	limit int
}

func (b *tailBuffer) Write(p []byte) (int, error) {
	if b.limit <= 0 {
		return len(p), nil
	}
	if len(p) >= b.limit {
		b.buf = append(b.buf[:0], p[len(p)-b.limit:]...)
		return len(p), nil
	}
	if extra := len(b.buf) + len(p) - b.limit; extra > 0 {
		copy(b.buf, b.buf[extra:])
		b.buf = b.buf[:len(b.buf)-extra]
	}
	b.buf = append(b.buf, p...)
	return len(p), nil
}

func (b *tailBuffer) String() string {
	return string(b.buf)
}

func launchFailureError(prefix string, err error, stderr string) error {
	detail := strings.TrimSpace(stderr)
	if detail == "" || strings.Contains(err.Error(), detail) {
		return fmt.Errorf("%s: %w", prefix, err)
	}
	return fmt.Errorf("%s: %w: %s", prefix, err, detail)
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
//
// When providersConfigPath is non-empty, the file is loaded fresh from disk
// and the check is instance-aware: inline api_key on the instance counts as a
// credential, and for openai-type instances the per-instance OAuth file
// (auth/<name>.json) is checked using the instance name, not "openai".
// When the file cannot be loaded or the path is empty, the original type-map
// behavior is used unchanged.
func validateProviderCredentials(provider string, store *credentials.Store, env []string, providersConfigPath string) error {
	if provider == "" || store == nil {
		return nil
	}

	// Config-path: instance-aware credential check.
	if providersConfigPath != "" {
		pcfg, exists, err := providerconfig.LoadFile(providersConfigPath)
		if err != nil || !exists {
			// Fall through to the no-config path below.
			goto noConfig
		}
		cfg := &pcfg
		for _, inst := range cfg.Instances {
			if inst.Name != strings.ToLower(strings.TrimSpace(provider)) {
				continue
			}
			// Inline api_key on the instance is always sufficient.
			if strings.TrimSpace(inst.APIKey) != "" {
				return nil
			}
			// For openai-type instances, check per-instance OAuth at auth/<name>.json.
			if inst.Type == "openai" {
				stateDir := openAIStateDirFromLaunchEnv(env)
				if openAIInstanceOAuthUsable(stateDir, inst.Name) {
					return nil
				}
			}
			// Fall through to env-var check using the instance's behavior tag.
			tag := providerconfig.BehaviorTag(string(inst.Type), string(inst.APIStyle))
			if tag == "openai-compatible" {
				// A base_url set in providers.toml is sufficient: the openaicompat
				// adapter reads it from config and does not require
				// OPENAI_COMPATIBLE_BASE_URL in the environment.
				if strings.TrimSpace(inst.BaseURL) != "" {
					return nil
				}
				if openAICompatibleBaseURLInEnv(env) {
					return nil
				}
			}
			if providerCredentialInEnv(tag, env) {
				return nil
			}
			return appwire.HubLaunchError(fmt.Sprintf("provider credentials missing for %s: set via serf/auth/apiKey/set or set the matching env var", provider))
		}
		// Instance name not found in config — don't block launch.
		return nil
	}

noConfig:
	// No-config path: original type-map behavior, unchanged.
	if strings.EqualFold(strings.TrimSpace(provider), "openai") && openAIStoredOAuthUsable(env) {
		return nil
	}
	if strings.EqualFold(strings.TrimSpace(provider), "openai-compatible") && openAICompatibleBaseURLInEnv(env) {
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
		if providerCredentialInEnv(provider, env) {
			return nil
		}
		return appwire.HubLaunchError(fmt.Sprintf("provider credentials missing for %s: set via serf/auth/apiKey/set or set the matching env var", provider))
	}
	// Unknown provider — don't block launch.
	return nil
}

// openAIInstanceOAuthUsable reports whether a usable OAuth record exists for
// the given instance name in stateDir (at auth/<instanceName>.json).
func openAIInstanceOAuthUsable(stateDir, instanceName string) bool {
	record, err := authopenai.LoadAuth(stateDir, instanceName)
	if err != nil {
		return false
	}
	if record.Source != authopenai.AuthSourceOAuth {
		return false
	}
	if record.Expiry.IsZero() || record.Expiry.After(time.Now()) {
		return true
	}
	return strings.TrimSpace(record.RefreshToken) != ""
}

func providerCredentialInEnv(provider string, env []string) bool {
	for _, key := range credentials.EnvVars(provider) {
		value, ok := envLookup(env, key)
		if env == nil {
			value, ok = os.Getenv(key), true
		}
		if ok && strings.TrimSpace(value) != "" {
			return true
		}
	}
	return false
}

func openAICompatibleBaseURLInEnv(env []string) bool {
	value, ok := envLookup(env, "OPENAI_COMPATIBLE_BASE_URL")
	if env == nil {
		value, ok = os.Getenv("OPENAI_COMPATIBLE_BASE_URL"), true
	}
	return ok && strings.TrimSpace(value) != ""
}

func openAIStoredOAuthUsable(env []string) bool {
	return openAIInstanceOAuthUsable(openAIStateDirFromLaunchEnv(env), "openai")
}

func openAIStateDirFromLaunchEnv(env []string) string {
	if env == nil {
		return authopenai.DefaultStateDirWithStateHome("")
	}
	return openAIStateDirFromEnvList(env)
}

func envLookup(env []string, key string) (string, bool) {
	prefix := key + "="
	for i := len(env) - 1; i >= 0; i-- {
		if strings.HasPrefix(env[i], prefix) {
			return strings.TrimPrefix(env[i], prefix), true
		}
	}
	return "", false
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
