package hub

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"primeradiant.com/evener/agent"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/cmd/evener-hub/internal/launchconfig"
	"primeradiant.com/evener/envvars"
	"primeradiant.com/evener/execsupport/orphanpipe"
	"primeradiant.com/evener/identifier"
	"primeradiant.com/evener/llm/registry"
	"primeradiant.com/evener/rendezvous"
)

const evenerLaunchCheckTimeout = 30 * time.Second

// evenerLaunchCheckWaitDelay bounds how long a launch-check's output pipe may
// stay open after the check exits or its context ends. Killing the check does
// not close a pipe a grandchild inherited (a wrapper script around evener is
// enough), and without this the call would wait for that grandchild however
// long it lives, turning evenerLaunchCheckTimeout into no bound at all. A
// check with no stray children reaches EOF at exit and never waits on it.
const evenerLaunchCheckWaitDelay = time.Second

// daemonLaunchOutputLimit bounds how much of a failed launch's daemon log is
// quoted back to the operator as the reason it would not start.
const daemonLaunchOutputLimit = 64 * 1024

var (
	spawnMkdirAll                   = os.MkdirAll
	spawnMkdirTemp                  = os.MkdirTemp
	spawnWriteFile                  = os.WriteFile
	spawnRemoveAll                  = os.RemoveAll
	listEvenerLaunchModelContractFn = listEvenerLaunchModelContract
	listRendezvousForWait           = rendezvous.List
	startResumeChild                = startResumeCommand
)

// daemonProcessCommand builds the subprocess that runs an `evener serve`
// daemon from its already-resolved binary, argv and environment. It is the
// external process-launch seam: production builds a plain exec.Cmd, and the
// rendezvous wait, listeners, serve admission/release and every hub-side
// algorithm stay real on both paths. A test replaces it to run the real serve
// lifecycle from a cmd/evener test-binary entry while the resolved argv and
// env are passed through unchanged.
//
// The env belongs to the command because the two daemon launch sites assign it
// exactly once, here; a caller-side assignment after this returns would
// silently drop anything the seam added.
var daemonProcessCommand = func(binary string, args, env []string) *exec.Cmd {
	cmd := exec.Command(binary, args...) //nolint:noctx // detached daemon must outlive ctx (see spawnDaemon)
	cmd.Env = env
	return cmd
}

// HubSpawner fulfills the hubcore.Spawner interface using SpawnDaemon.
type HubSpawner struct {
	Cfg                 Config
	EvenerBinary        string // path to the evener binary; "" → "evener" on PATH
	RunDir              string
	HubToken            string
	Registry            *hubcore.ProviderRegistry // live registry the credential gate reads
	ProvidersConfigPath string                    // providers.toml the child reads as its user layer
	CredentialsPath     string                    // credentials.toml the child resolves keys from
	NoUserLayer         bool                      // the tri-state from EVENER_PROVIDERS_CONFIG: present and empty means no user layer (spec §10)
}

// childNoUserLayer is spec §10's third state as a child must see it: the hub's
// own tri-state, or a providers.toml the registry cannot read right now. It is
// asked per call rather than frozen at startup, because every credential
// action reloads the registry and can change the answer in either direction —
// and a child pointed at a file the hub is not reading (or denied one it is)
// fails at launch with none of the hub's diagnostics attached.
func childNoUserLayer(configured bool, reg *hubcore.ProviderRegistry) bool {
	return configured || (reg != nil && reg.WritesRefused())
}

type EvenerLaunchModelLister interface {
	ListLaunchModels(context.Context) ([]appwire.ModelDescriptor, error)
}

type EvenerLaunchModelContractLister interface {
	ListLaunchModelContract(context.Context) (appwire.ModelListResponse, error)
}

type EvenerLaunchModelContractWorkingDirLister interface {
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
	stateDir, err := resolveEvenerLaunchStateDir("", nil)
	if err != nil {
		return appwire.ModelListResponse{}, err
	}
	env := launchconfig.ToEnv(launchconfig.EnvInputs{
		Resolved:            launchconfig.Resolved{},
		RunDir:              h.RunDir,
		StateDir:            stateDir,
		HubToken:            h.HubToken,
		ParentEnv:           os.Environ(),
		ProvidersConfigPath: h.ProvidersConfigPath,
		NoUserLayer:         childNoUserLayer(h.NoUserLayer, h.Registry),
		CredentialsPath:     h.CredentialsPath,
	})
	return listEvenerLaunchModelContractFn(ctx, h.EvenerBinary, env)
}

func (h *HubSpawner) ListLaunchModelContractForWorkingDir(ctx context.Context, workingDir string) (appwire.ModelListResponse, error) {
	stateDir, err := resolveEvenerLaunchStateDir(workingDir, nil)
	if err != nil {
		// The spawn form lets a user type a working directory that does not
		// exist yet (preflight offers to create it on submit), and the model
		// picker loads its launchable set scoped by that cwd. A not-yet-created
		// directory fails project resolution at EvalSymlinks with fs.ErrNotExist;
		// failing the whole model list here empties the picker before the
		// directory is ever created. Fall back to the unscoped state dir — the
		// same resolveEvenerLaunchStateDir("", nil) ListLaunchModelContract
		// uses — so the launchable set loads against the default project
		// identity. A non-NotExist error (permissions, etc.) still propagates.
		if !errors.Is(err, fs.ErrNotExist) {
			return appwire.ModelListResponse{}, err
		}
		stateDir, err = resolveEvenerLaunchStateDir("", nil)
		if err != nil {
			return appwire.ModelListResponse{}, err
		}
	}
	env := launchconfig.ToEnv(launchconfig.EnvInputs{
		Resolved:            launchconfig.Resolved{},
		RunDir:              h.RunDir,
		StateDir:            stateDir,
		HubToken:            h.HubToken,
		ParentEnv:           os.Environ(),
		ProvidersConfigPath: h.ProvidersConfigPath,
		NoUserLayer:         childNoUserLayer(h.NoUserLayer, h.Registry),
		CredentialsPath:     h.CredentialsPath,
	})
	return listEvenerLaunchModelContractFn(ctx, h.EvenerBinary, env)
}

func (h *HubSpawner) Spawn(ctx context.Context, req hubcore.SpawnRequest) (rendezvous.Entry, error) {
	timeout := h.Cfg.SpawnTimeout
	if timeout == 0 {
		timeout = 30 * time.Second
	}
	if req.StateDir == "" {
		var err error
		if req.Project.ID != "" {
			req.StateDir, err = resolveStateDirForProject(req.Project, req.WorkingDir, req.Resolved.Effective.Env)
		} else {
			req.Project, req.StateDir, err = resolveEvenerLaunchProjectStateDir(req.WorkingDir, req.Resolved.Effective.Env)
		}
		if err != nil {
			return rendezvous.Entry{}, err
		}
	}
	resolved, cleanup, err := prepareResolvedForSpawn(req.StateDir, req.Resolved)
	if err != nil {
		return rendezvous.Entry{}, err
	}
	defer cleanup()
	req.Resolved = resolved
	// The idle-retirement deadline is a runtime launch parameter owned by the
	// Hub's config, assigned AFTER layer resolution/materialization on both
	// launch paths (resume's resolved is a historical reconstruction — the
	// timeout must reflect the Hub's CURRENT config, not the past launch's).
	req.Resolved.DaemonIdleTimeout = h.Cfg.DaemonIdleTimeout
	applyHubAPILogDefault(&req.Resolved, h.Cfg.APILog)
	req.RunDir = h.RunDir
	if req.Resolved.Effective.AppReplaySize != nil {
		req.AppReplaySize = *req.Resolved.Effective.AppReplaySize
	}
	req.Env = launchconfig.ToEnv(launchconfig.EnvInputs{
		Resolved:            req.Resolved,
		ParentEnv:           os.Environ(),
		RunDir:              h.RunDir,
		StateDir:            req.StateDir,
		HubToken:            h.HubToken,
		ProvidersConfigPath: h.ProvidersConfigPath,
		NoUserLayer:         childNoUserLayer(h.NoUserLayer, h.Registry),
		CredentialsPath:     h.CredentialsPath,
	})
	if err := validateProviderCredentials(req.Provider, req.Resolved.Effective.Model, h.Registry); err != nil {
		return rendezvous.Entry{}, err
	}
	if err := validateEvenerLaunchContract(ctx, h.EvenerBinary, req.Resolved.Effective.Model, req.Env); err != nil {
		return rendezvous.Entry{}, err
	}
	return SpawnDaemon(ctx, h.EvenerBinary, h.RunDir, req, timeout)
}

func (h *HubSpawner) Resume(ctx context.Context, req hubcore.ResumeRequest) (rendezvous.Entry, error) {
	ctx, trace := withThreadLifecycleLog(ctx, "resume", req.SessionID, nil)
	prepareDone := trace.stage(ctx, "launch_preparation")
	timeout := h.Cfg.SpawnTimeout
	if timeout == 0 {
		timeout = 30 * time.Second
	}
	if req.StateDir == "" {
		var err error
		if req.Project.ID != "" {
			req.StateDir, err = resolveStateDirForProject(req.Project, req.WorkingDir, req.Resolved.Effective.Env)
		} else {
			req.Project, req.StateDir, err = resolveEvenerLaunchProjectStateDir(req.WorkingDir, req.Resolved.Effective.Env)
		}
		if err != nil {
			prepareDone(err)
			return rendezvous.Entry{}, err
		}
	}
	resolved, cleanup, err := prepareResolvedForSpawn(req.StateDir, req.Resolved)
	if err != nil {
		prepareDone(err)
		return rendezvous.Entry{}, err
	}
	defer cleanup()
	req.Resolved = resolved
	// The idle-retirement deadline is a runtime launch parameter owned by the
	// Hub's config, assigned AFTER layer resolution/materialization on both
	// launch paths (resume's resolved is a historical reconstruction — the
	// timeout must reflect the Hub's CURRENT config, not the past launch's).
	req.Resolved.DaemonIdleTimeout = h.Cfg.DaemonIdleTimeout
	applyHubAPILogDefault(&req.Resolved, h.Cfg.APILog)
	req.RunDir = h.RunDir
	if req.Resolved.Effective.AppReplaySize != nil {
		req.AppReplaySize = *req.Resolved.Effective.AppReplaySize
	}
	req.Env = launchconfig.ToEnv(launchconfig.EnvInputs{
		Resolved:            req.Resolved,
		ParentEnv:           os.Environ(),
		RunDir:              h.RunDir,
		StateDir:            req.StateDir,
		HubToken:            h.HubToken,
		ProvidersConfigPath: h.ProvidersConfigPath,
		NoUserLayer:         childNoUserLayer(h.NoUserLayer, h.Registry),
		CredentialsPath:     h.CredentialsPath,
	})
	if req.Provider != "" {
		// The resume request carries the model the session persisted
		// (resumeRequestForConfig builds it from the session meta), so the
		// gate judges the very model the resumed session runs — a row may
		// override the auth scheme, and the instance view can vouch for a
		// launch that model cannot authenticate. The child's arguments stay
		// model-stripped (buildResumeArgs) and the launch-contract check
		// below stays model-blind; only the credential gate is model-aware.
		if err := validateProviderCredentials(req.Provider, req.Resolved.Effective.Model, h.Registry); err != nil {
			prepareDone(err)
			return rendezvous.Entry{}, err
		}
	}
	// Resume must validate only the daemon/app-wire contract. The resumed
	// session's persisted metadata, not ambient launch config, selects the model;
	// passing req.Resolved.Effective.Model here can reject an otherwise-valid
	// resume because of a stale launch-config model.
	contractDone := trace.stage(ctx, "launch_contract")
	if err := validateEvenerLaunchContract(ctx, h.EvenerBinary, "", req.Env); err != nil {
		contractDone(err)
		prepareDone(err)
		return rendezvous.Entry{}, err
	}
	contractDone(nil)
	prepareDone(nil)
	return ResumeDaemon(ctx, h.EvenerBinary, h.RunDir, req, timeout)
}

func prepareResolvedForSpawn(stateDir string, resolved launchconfig.Resolved) (launchconfig.Resolved, func(), error) {
	effective := &resolved.Effective
	if effective.SystemPromptMode != "inline" && effective.SystemPromptAppendMode != "inline" {
		return resolved, func() {}, nil
	}
	if stateDir == "" {
		return launchconfig.Resolved{}, nil, errors.New("state dir is required for inline system prompts")
	}
	if err := spawnMkdirAll(stateDir, 0o700); err != nil {
		return launchconfig.Resolved{}, nil, fmt.Errorf("create state dir for inline prompts: %w", err)
	}
	tempDir, err := spawnMkdirTemp(stateDir, "inline-prompts-")
	if err != nil {
		return launchconfig.Resolved{}, nil, fmt.Errorf("create inline prompt dir: %w", err)
	}
	cleanupPartial := func() { _ = spawnRemoveAll(tempDir) }
	writePrompt := func(name, text string) (string, error) {
		path := filepath.Join(tempDir, name)
		if err := spawnWriteFile(path, []byte(text), 0o600); err != nil {
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

// applyHubAPILogDefault fills the hub.toml api_log setting into a resolved
// launch config as the floor default: it applies only when every launch layer
// left api_log unset, so an explicit per-session choice (either direction)
// always wins over the hub-wide default. The floor pins BOTH directions into
// the child argv rather than deferring to the child binary's own default:
// an evener predating the api_log flag still records by default, which would
// silently defeat the hub's opt-out, so the hub-wide policy must be explicit
// even when it matches what a current binary would do anyway. Mutating the
// copy in req.Resolved is safe because Resolved was copied by value out of
// the resolver.
func applyHubAPILogDefault(resolved *launchconfig.Resolved, apiLog bool) {
	if resolved == nil || resolved.Effective.APILog != nil {
		return
	}
	value := apiLog
	resolved.Effective.APILog = &value
	if resolved.Provenance == nil {
		resolved.Provenance = map[string]launchconfig.LayerName{}
	}
	resolved.Provenance["api_log"] = launchconfig.LayerHub
}

// buildSpawnArgs assembles the arg slice for `evener serve` from a hubcore.SpawnRequest.
//
// Always passes --addr 127.0.0.1:0 so the daemon binds an ephemeral port,
// which it reports via its rendezvous file.
func buildSpawnArgs(req hubcore.SpawnRequest) []string {
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
	if req.PluginRoot != "" {
		args = append(args, "--plugin-root", req.PluginRoot)
	}
	if req.AgentsDocPath != "" {
		args = append(args, "--agents-doc", req.AgentsDocPath)
	}
	if req.AppReplaySize > 0 {
		args = append(args, "--app-replay-size", strconv.Itoa(req.AppReplaySize))
	}
	args = append(args, launchconfig.ToArgs(req.Resolved)...)
	return args
}

func buildResumeArgs(req hubcore.ResumeRequest) []string {
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
	if req.AgentsDocPath != "" {
		args = append(args, "--agents-doc", req.AgentsDocPath)
	}
	if req.AppReplaySize > 0 {
		args = append(args, "--app-replay-size", strconv.Itoa(req.AppReplaySize))
	}
	resumeResolved := req.Resolved
	resumeResolved.Effective.Model = ""
	resumeResolved.Effective.FastCheapModel = ""
	resumeResolved.Effective.ModelFallbacks = nil
	args = append(args, launchconfig.ToArgs(resumeResolved)...)
	return args
}

// SpawnDaemon launches a `evener serve` subprocess from the given hubcore.SpawnRequest,
// then waits up to timeout for its rendezvous file to appear.
//
// Returns the rendezvous Entry on success, or error on timeout / spawn failure.
// Caller does NOT manage the subprocess lifecycle — the spawned daemon
// runs independently and lives until killed or sent /shutdown.
func SpawnDaemon(ctx context.Context, evenerBinary string, runDir string, req hubcore.SpawnRequest, timeout time.Duration) (rendezvous.Entry, error) {
	return spawnDaemon(ctx, evenerBinary, runDir, req, timeout, os.Stderr)
}

// spawnDaemon is SpawnDaemon against a caller-supplied hub log, which is the
// hub's own stderr in production.
func spawnDaemon(ctx context.Context, evenerBinary string, runDir string, req hubcore.SpawnRequest, timeout time.Duration, hubLog io.Writer) (entry rendezvous.Entry, launchErr error) {
	ctx, trace := withThreadLifecycleLog(ctx, "spawn", "", hubLog)
	daemonStarted := time.Now()
	trace.record(ctx, "daemon", "begin", daemonStarted, nil, 0, 0)
	defer func() { trace.record(ctx, "daemon", "complete", daemonStarted, launchErr, entry.PID, 0) }()
	launchDone := trace.stage(ctx, "launch")
	if evenerBinary == "" {
		evenerBinary = "evener"
	}
	args := append([]string{"serve"}, buildSpawnArgs(req)...)

	// NOT CommandContext: the spawned daemon must outlive this call's ctx (it
	// runs independently until killed or sent /shutdown). ctx scopes only the
	// rendezvous wait below; on timeout we kill the process explicitly.
	cmd := daemonProcessCommand(evenerBinary, args, req.Env)
	cmd.SysProcAttr = daemonSysProcAttr()
	// A fresh spawn cannot name the log after its session yet: the daemon mints
	// the id and reports it through rendezvous, so the file is adopted below.
	dlog, err := openDaemonLog(runDir, "")
	if err != nil {
		launchDone(err)
		return rendezvous.Entry{}, err
	}
	dlog.attach(cmd)

	startedAt := time.Now()
	if err := cmd.Start(); err != nil {
		launchDone(err)
		dlog.close()
		// Nothing was ever written to it and no session will ever claim it.
		dlog.removeIfPending()
		return rendezvous.Entry{}, fmt.Errorf("start daemon: %w", err)
	}
	launchDone(nil)
	// The child holds its own descriptor from here on.
	dlog.close()
	exited := make(chan error, 1)
	go func() {
		exited <- cmd.Wait()
	}()

	waitCtx, cancel := withRendezvousTimeout(ctx, timeout)
	defer cancel()
	waitStarted := time.Now()
	trace.record(waitCtx, "rendezvous", "begin", waitStarted, nil, cmd.Process.Pid, 0)
	entry, err = waitForRendezvousOrExit(waitCtx, runDir, cmd.Process.Pid, exited, WithStartedAfter(startedAt))
	trace.record(waitCtx, "rendezvous", "complete", waitStarted, err, cmd.Process.Pid, 0)
	if err != nil {
		_ = cmd.Process.Kill()
		// Take the tail FIRST: it is the only account of this failure anyone
		// gets. Then drop the file, because the session id that would have
		// named it only ever arrives with the rendezvous entry this launch did
		// not get, so nothing will ever read it again (kata dd8d).
		tail := dlog.tail(daemonLaunchOutputLimit)
		trace.record(waitCtx, "failed_start", "complete", waitStarted, err, cmd.Process.Pid, len(tail))
		failure := launchFailureError(launchFailurePrefix("daemon spawn", err), err, tail)
		dlog.removeIfPending()
		return rendezvous.Entry{}, failure
	}
	dlog.adopt(entry.SessionID)
	// A fresh daemon only reveals its session through rendezvous. Bind that
	// identity to the final record without mutating a caller's context trace.
	completedTrace := *trace
	completedTrace.sessionID = entry.SessionID
	trace = &completedTrace
	_, _ = io.WriteString(hubLog, daemonSpawnBanner(entry.SessionID, entry.PID, dlog.path))
	return entry, nil
}

// WaitOption configures waitForRendezvousOrExit.
type WaitOption func(*waitConfig)

type waitConfig struct {
	startedAfter time.Time
}

// completionOwnedResumeTimeout is the hub-side bound on a completion-owned
// resume's rendezvous wait, mirroring the client's own RESUME_REQUEST_TIMEOUT_MS
// (10m): no client waits longer, so the bound only fires for callers with no
// deadline of their own. A var so tests can shrink it.
var completionOwnedResumeTimeout = 10 * time.Minute

// withRendezvousTimeout applies the production startup budget when it is
// positive. A non-positive timeout leaves the caller's context as the only
// bound, which lets deterministic host-process tests await the rendezvous edge
// itself instead of turning an arbitrary duration into their behavior oracle.
func withRendezvousTimeout(ctx context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	if timeout <= 0 {
		return context.WithCancel(ctx)
	}
	return context.WithTimeout(ctx, timeout)
}

// WithStartedAfter rejects rendezvous entries whose StartedAt is on or
// before t. Use this to defend against a recycled PID matching a stale
// entry from a previously-crashed daemon.
func WithStartedAfter(t time.Time) WaitOption {
	return func(c *waitConfig) { c.startedAfter = t }
}

// ResumeDaemon launches `evener serve --resume <sessionID>` and waits for
// rendezvous. Returns the resumed daemon's rendezvous Entry.
//
// Note: resume PRESERVES the existing session_id. The daemon restores via
// RestoreSessionFromMetaWithConfig, which keeps the persisted meta.ID
// (immutable across restart), so the returned Entry.SessionID is the same
// id the session had before it exited. (A fresh session_id is minted only
// by /clear, which is a distinct operation.)
func ResumeDaemon(ctx context.Context, evenerBinary, runDir string, req hubcore.ResumeRequest, timeout time.Duration) (rendezvous.Entry, error) {
	return resumeDaemon(ctx, evenerBinary, runDir, req, timeout, os.Stderr)
}

// resumeChild is the process-launch boundary. The launcher retains the exact
// child's kill/wait handles; cancellation never rediscovers a PID to signal.
type resumeChild struct {
	pid  int
	kill func() error
	wait func() error
}

func startResumeCommand(cmd *exec.Cmd) (resumeChild, error) {
	if err := cmd.Start(); err != nil {
		return resumeChild{}, err
	}
	return resumeChild{pid: cmd.Process.Pid, kill: cmd.Process.Kill, wait: cmd.Wait}, nil
}

// resumeCleanupError is separate from the original launch failure so a Stop
// waiting on Resume can refuse to claim cleanup while preserving both causes.
type resumeCleanupError struct{ cause error }

func (e *resumeCleanupError) Error() string {
	return "resume child cleanup is unconfirmed: " + e.cause.Error()
}
func (e *resumeCleanupError) Unwrap() error { return e.cause }

func reapResumeChild(child resumeChild, reaped <-chan struct{}, active *hubcore.ActiveResume) error {
	if err := child.kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
		if active != nil {
			// The kill handle must outlive the launcher, which is about to
			// return: retaining it on the active lifetime lets the stop that
			// drains this Resume retry the kill until the child is confirmed
			// reaped, instead of stranding a live child no later action can
			// address behind the retained cleanup error.
			active.RetainChildCleanup(child.kill)
		}
		select {
		case <-reaped:
			return nil
		default:
			return &resumeCleanupError{cause: err}
		}
	}
	// Only the original waiter reads Wait. Even when rendezvous waiting already
	// consumed its exit result, this closed channel still proves reaping ended.
	<-reaped
	return nil
}

// resumeDaemon is ResumeDaemon against a caller-supplied hub log, which is the
// hub's own stderr in production.
func resumeDaemon(ctx context.Context, evenerBinary, runDir string, req hubcore.ResumeRequest, timeout time.Duration, hubLog io.Writer) (entry rendezvous.Entry, launchErr error) {
	ctx, trace := withThreadLifecycleLog(ctx, "resume", req.SessionID, hubLog)
	done := trace.stage(ctx, "daemon")
	defer func() { done(launchErr) }()
	launchDone := trace.stage(ctx, "launch")
	if evenerBinary == "" {
		evenerBinary = "evener"
	}
	args := buildResumeArgs(req)
	// NOT CommandContext: the resumed daemon must outlive this call's ctx (it
	// runs independently until killed or sent /shutdown). ctx scopes only the
	// rendezvous wait below; on timeout we kill the process explicitly.
	cmd := daemonProcessCommand(evenerBinary, args, req.Env)
	cmd.SysProcAttr = daemonSysProcAttr()
	// A resume keeps its session's id, so it keeps — and appends to — that
	// session's own log.
	dlog, err := openDaemonLog(runDir, req.SessionID)
	if err != nil {
		launchDone(err)
		return rendezvous.Entry{}, err
	}
	dlog.attach(cmd)
	startedAt := time.Now()
	if err := ctx.Err(); err != nil {
		launchDone(err)
		dlog.close()
		dlog.removeIfUncommitted()
		failure := errors.Join(rendezvousWaitError(ctx), err)
		return rendezvous.Entry{}, launchFailureError(launchFailurePrefix("resume", failure), failure, "")
	}
	if req.ActiveResume != nil {
		if err := req.ActiveResume.BeforeLaunch(); err != nil {
			launchDone(err)
			dlog.close()
			dlog.removeIfUncommitted()
			return rendezvous.Entry{}, fmt.Errorf("prepare resume ownership: %w", err)
		}
	}
	child, err := startResumeChild(cmd)
	if err != nil {
		dlog.close()
		dlog.removeIfUncommitted()
		if req.ActiveResume != nil {
			req.ActiveResume.ChildReaped()
			if cleanupErr := req.ActiveResume.LaunchFinished(true, nil); cleanupErr != nil {
				err = errors.Join(err, &resumeCleanupError{cause: cleanupErr})
			}
		}
		launchDone(err)
		return rendezvous.Entry{}, fmt.Errorf("start daemon: %w", err)
	}
	launchDone(nil)
	// The child holds its own descriptor from here on.
	dlog.close()
	exited := make(chan error, 1)
	reaped := make(chan struct{})
	activeResume := req.ActiveResume
	go func() {
		exited <- child.wait()
		if activeResume != nil {
			activeResume.ChildReaped()
		}
		close(reaped)
	}()
	finishLaunch := func(failed bool, cleanupErr error) error {
		if req.ActiveResume != nil {
			if err := req.ActiveResume.LaunchFinished(failed, cleanupErr); err != nil {
				// LaunchFinished already classifies a retained cleanup
				// failure; wrap only an unclassified one so the "cleanup is
				// unconfirmed" text is not duplicated.
				if cleanup, ok := errors.AsType[*resumeCleanupError](err); ok {
					return cleanup
				}
				return &resumeCleanupError{cause: err}
			}
			return nil
		}
		return cleanupErr
	}
	if req.CompletionOwned {
		// A completion-owned resume answers to its caller's lifecycle, but the
		// rendezvous wait still gets a hub-side bound: the web client caps its
		// own wait at RESUME_REQUEST_TIMEOUT_MS (10m), and a client without a
		// deadline must not hold the session's ownership aliases forever while
		// each relay/isLive/workspace read degrades against the lock.
		timeout = completionOwnedResumeTimeout
	}
	waitCtx, cancel := withRendezvousTimeout(ctx, timeout)
	defer cancel()
	waitStarted := time.Now()
	trace.record(waitCtx, "rendezvous", "begin", waitStarted, nil, child.pid, 0)
	entry, err = waitForRendezvousOrExit(waitCtx, runDir, child.pid, exited, WithStartedAfter(startedAt))
	trace.record(waitCtx, "rendezvous", "complete", waitStarted, err, child.pid, 0)
	if err != nil {
		err = errors.Join(err, finishLaunch(true, reapResumeChild(child, reaped, activeResume)))
		tail := dlog.tail(daemonLaunchOutputLimit)
		trace.record(waitCtx, "failed_start", "complete", waitStarted, err, child.pid, len(tail))
		failure := launchFailureError(launchFailurePrefix("resume", err), err, tail)
		dlog.removeIfUncommitted()
		return rendezvous.Entry{}, failure
	}
	if err := dlog.promote(); err != nil {
		promotionErr := errors.Join(fmt.Errorf("promote daemon log: %w", err), finishLaunch(true, reapResumeChild(child, reaped, activeResume)))
		tail := dlog.tail(daemonLaunchOutputLimit)
		trace.record(ctx, "failed_start", "complete", waitStarted, promotionErr, child.pid, len(tail))
		failure := launchFailureError(launchFailurePrefix("resume", promotionErr), promotionErr, tail)
		dlog.removeIfUncommitted()
		return rendezvous.Entry{}, failure
	}
	_ = finishLaunch(false, nil)
	_, _ = io.WriteString(hubLog, daemonSpawnBanner(entry.SessionID, entry.PID, dlog.path))
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

// errRendezvousTimeout is the rendezvous wait running out of time, as opposed
// to the child dying first or the caller walking away. Those are the only
// three ways the wait fails, the operator's next move differs for each, and a
// caller that wants to say which one happened must not have to re-read the
// message to find out.
var errRendezvousTimeout = errors.New("timeout waiting for rendezvous")

// errRendezvousCanceled is the caller abandoning the launch before the daemon
// registered. The wait runs under the caller's context on both hub paths — the
// REST resume passes r.Context(), the RPC one the websocket connection's ctx —
// so a browser that navigates away, a dropped connection, or a keepalive that
// gives up ends the wait without the spawn timeout having elapsed (kata 0c3g).
//
// The text keeps "rendezvous" so a canceled launch stays inside
// diagnostic.HubFailureKeywords: it is still a hub failure whose honest
// recovery is to reconnect and re-issue, not a Evener fault with a session log
// to go read.
var errRendezvousCanceled = errors.New("request canceled before rendezvous")

// rendezvousWaitError says which way a done rendezvous-wait context ended.
// ctx.Err() separates the two outright, so nothing has to be inferred from
// timing: Canceled is the caller walking away, DeadlineExceeded is time
// genuinely running out — the spawn timeout a launch layers on, or a deadline
// the caller brought with it.
func rendezvousWaitError(ctx context.Context) error {
	if errors.Is(ctx.Err(), context.Canceled) {
		return errRendezvousCanceled
	}
	return errRendezvousTimeout
}

// launchFailurePrefix labels a launch failure by what actually stopped it. A
// daemon that fails validation and exits in milliseconds is not a timeout, and
// neither is a caller who stopped waiting; calling either one a timeout sends
// an operator triaging it after a slow machine, a hung provider, or a
// too-short SpawnTimeout — none of which are involved (katas 42ck, 0c3g).
// Only the wait genuinely running out of time keeps the timeout label.
func launchFailurePrefix(action string, err error) string {
	switch {
	case errors.Is(err, errRendezvousTimeout):
		return action + " timed out"
	case errors.Is(err, errRendezvousCanceled):
		return action + " canceled"
	default:
		return action + " failed"
	}
}

func launchFailureError(prefix string, err error, stderr string) error {
	detail := strings.TrimSpace(stderr)
	if detail == "" || strings.Contains(err.Error(), detail) {
		return fmt.Errorf("%s: %w", prefix, err)
	}
	return fmt.Errorf("%s: %w: %s", prefix, err, detail)
}

func resolveEvenerStateDir(workDir, override string) (string, error) {
	return resolveEvenerStateDirWithStateHome(workDir, override, "")
}

func resolveEvenerLaunchStateDir(workDir string, env map[string]string) (string, error) {
	_, stateDir, err := resolveEvenerLaunchProjectStateDir(workDir, env)
	return stateDir, err
}

func resolveEvenerLaunchProjectStateDir(workDir string, env map[string]string) (identifier.Project, string, error) {
	if env == nil {
		return resolveEvenerStateDirWithProject(workDir, "", "")
	}
	return resolveEvenerStateDirWithProject(workDir, env[envvars.EVENERStateDir.Name], env[envvars.XDGStateHome.Name])
}

// resolveStateDirForProject derives state storage from an identity that was
// already resolved by the launch entry point. An explicit EVENER_STATE_DIR
// remains authoritative; the active working directory is intentionally unused
// in that case and is retained only for the direct-call fallback contract.
func resolveStateDirForProject(project identifier.Project, workDir string, env map[string]string) (string, error) {
	override := ""
	stateHome := ""
	if env != nil {
		override = env[envvars.EVENERStateDir.Name]
		stateHome = env[envvars.XDGStateHome.Name]
	}
	if strings.TrimSpace(override) != "" {
		return override, nil
	}
	if project.ID == "" {
		_, stateDir, err := resolveEvenerStateDirWithProject(workDir, override, stateHome)
		return stateDir, err
	}
	return agent.RuntimeDirForProjectWithStateHome(project, strings.TrimSpace(stateHome)), nil
}

func resolveEvenerStateDirWithStateHome(workDir, override, stateHome string) (string, error) {
	_, stateDir, err := resolveEvenerStateDirWithProject(workDir, override, stateHome)
	return stateDir, err
}

func resolveEvenerStateDirWithProject(workDir, override, stateHome string) (identifier.Project, string, error) {
	if strings.TrimSpace(override) != "" {
		return identifier.Project{}, override, nil
	}
	wd := strings.TrimSpace(workDir)
	if wd == "" {
		if got, err := os.Getwd(); err == nil {
			wd = got
		}
	}
	// Key off the resolved main repo root, not the raw wd: for an origin-less
	// repo, spawning from a linked worktree must compute the same session
	// state dir as spawning from the main checkout. See
	// cmdutil.ResolveStateKeyDir and
	// docs/superpowers/specs/2026-07-02-native-worktree-tools-design.md §1.
	project, stateDir, err := agent.RuntimeDirWithStateHome(wd, "", strings.TrimSpace(stateHome))
	if err != nil {
		return identifier.Project{}, "", fmt.Errorf("resolve project state: %w", err)
	}
	return project, stateDir, nil
}

// validateProviderCredentials refuses a launch whose target has no
// credential the child could resolve, so the failure is a launch error the
// user can read rather than a 401 mid-session. A launch names a model, and
// the child resolves instance/model through that model's own row-merged
// transport — a row may override the auth scheme or header — so the gate
// judges that transport, not the per-instance listing (which describes the
// bare-name launch alone); with no model it judges the listing view. The
// judgment is structural for command expressions: the gate counts a
// command-bearing credential slot as present and never executes it — the
// child alone runs credential commands (the evaluation contract), so the
// preflight neither mints a second token nor blocks a launch on a
// transient command failure the child's retry would survive. The registry
// answers every part of it (spec §11.3): auth = none and optional-bearer
// need nothing, oauth-openai-codex is satisfied by the instance's OAuth
// record, gcp-adc by the ADC variable or file, and everything else by a
// resolved key or credential header — with the endpoint stop of §10
// already applied, so a gateway that inherits no vendor key is refused here.
//
// A nil registry or an empty provider name means there is nothing to check.
func validateProviderCredentials(provider, model string, reg *hubcore.ProviderRegistry) error {
	name := strings.ToLower(strings.TrimSpace(provider))
	if name == "" || reg == nil || reg.Get() == nil {
		return nil
	}
	r := reg.Get()
	// The launch's model arrives as launchconfig materialized it:
	// provider-qualified (cmdutil.ModelRef.Qualified()). Strip this
	// instance's own prefix once, for the refusal message and the judgment
	// alike — ResolveGateCredential strips again, harmlessly.
	if ref := registry.ParseRef(model); strings.EqualFold(ref.Instance, name) {
		model = ref.Model
	}
	if r.HasInstance(name) {
		// The listing says the instance exists; the judgment is the gate's
		// own view of the launch — ResolveGateCredential handles the
		// provider-qualified model form, judges the model's transport, and
		// executes no command expression. A named model the child refuses
		// — disabled, or one that does not resolve — is refused here too,
		// before the spawn, with the model's own verdict rather than a
		// credential message for a launch that never happens.
		res, err := r.ResolveGateCredential(name, model)
		if err != nil {
			return appwire.HubLaunchError(err.Error())
		}
		switch res.Transport.Auth {
		case registry.AuthNone, registry.AuthOptionalBearer:
			return nil
		}
		if res.Credential.Source != "none" {
			return nil
		}
		target := name
		if model != "" {
			target = name + "/" + model
		}
		return appwire.HubLaunchError(fmt.Sprintf("provider credentials missing for %s: %s", target, strings.Join(res.Warnings, "; ")))
	}
	// Not an instance: a curated implicit provider whose credential does not
	// resolve in this environment (spec §5.1), or a name nothing declares.
	if p, ok := r.Provider(name); ok && registry.BoolValue(p.Implicit) {
		// The judgment is the launch's own — the named model's row, or
		// the default row when the caller named none — resolved the way
		// the instance branch's gate resolves it, never executing a
		// command. Not the provider's model-less shape: a row override
		// that flips the scheme flips the remedy with it, or the gate
		// points at a credential the launch never reads. A named model
		// the child refuses is the preflight's own refusal, before the
		// spawn — not a bare-launch scheme to offer a credential remedy
		// against.
		res, err := r.ResolveGateCredential(name, model)
		if err != nil {
			return appwire.HubLaunchError(err.Error())
		}
		// The named launch's own judgment: its row's scheme and
		// credential. A row pinned to none or optional-bearer
		// launches with nothing to configure; a resolved credential
		// is satisfied; anything else takes the remediation for the
		// row's own scheme below.
		switch res.Transport.Auth {
		case registry.AuthNone, registry.AuthOptionalBearer:
			return nil
		}
		if res.Credential.Source != "none" {
			return nil
		}
		scheme := res.Transport.Auth
		// The Codex transport reads no key at all (spec §5.1), so its
		// api_key_env list is empty and the key advice below would name
		// nothing. Point at the flow that does configure it.
		if scheme == registry.AuthOAuthOpenAICodex {
			return appwire.HubLaunchError(fmt.Sprintf("provider credentials missing for %s: run `evener openai login --instance %s`", name, name))
		}
		// gcp-adc reads no key either, and it can be unconfigured two ways
		// (spec §5.1). Hidden means the base URL did not resolve — the
		// registry says which variables this environment still owes it and
		// what is wrong with the ones it has. Otherwise the URL resolves and
		// it is the credential that did not.
		if scheme == registry.AuthGCPADC {
			if p.Hidden {
				unset, problems := r.UnresolvedBaseURL(name)
				reasons := append([]string(nil), problems...)
				if len(unset) > 0 {
					reasons = append(reasons, "set "+strings.Join(unset, ", "))
				}
				return appwire.HubLaunchError(fmt.Sprintf("provider %s is not configured: %s", name, strings.Join(reasons, "; ")))
			}
			return appwire.HubLaunchError(fmt.Sprintf("provider credentials missing for %s: it needs application-default credentials (run `gcloud auth application-default login` or set GOOGLE_APPLICATION_CREDENTIALS) or a stored credential JSON (evener/auth/credentialJson/set)", name))
		}
		return appwire.HubLaunchError(fmt.Sprintf("provider credentials missing for %s: set a key via evener/auth/apiKey/set or export one of %s", name, strings.Join(p.APIKeyEnv, ", ")))
	}
	return appwire.HubLaunchError(fmt.Sprintf("unknown instance %q: add a [providers.%s] entry to providers.toml", name, name))
}

func envLookup(env []string, key string) (string, bool) {
	prefix := key + "="
	for _, entry := range slices.Backward(env) {
		if rest, ok := strings.CutPrefix(entry, prefix); ok {
			return rest, true
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

// launchCheckWaitError says which way a launch-check that never produced a
// verdict was stopped. Its context is done for two unrelated reasons — the
// evenerLaunchCheckTimeout budget elapsed, or the caller went away — and only the
// first is a timeout. Calling the second one sends an operator triaging it
// after a slow machine or a hung `evener launch-check`, when nothing was slow and
// nobody is waiting for the answer any more (kata zg02).
//
// The launch-check runs ahead of the rendezvous wait and carries its own
// budget, so this is the first place a mid-launch cancellation lands: the hub
// runs it under the caller's context on every path that reaches it, and both
// hub paths hand it a live request context — r.Context() on the REST resume,
// the websocket connection's ctx on the RPC one.
//
// ctx.Err() separates the two outright, the same way rendezvousWaitError does
// for the wait that follows: Canceled is the caller walking away,
// DeadlineExceeded is time genuinely running out — the hub's own budget, or a
// deadline the caller brought with it.
//
// Both stay an appwire.HubLaunchError, the discriminator the web client and the
// TUI notice panel read to headline the failure as a session that would not
// start. The label changes; the family of failure does not.
func launchCheckWaitError(ctx context.Context) error {
	if errors.Is(ctx.Err(), context.Canceled) {
		return appwire.HubLaunchError("evener launch-check canceled")
	}
	return appwire.HubLaunchError("evener launch-check timed out")
}

// runLaunchCheck runs a launch-check and returns its combined output, bounding
// the wait for its output pipe with evenerLaunchCheckWaitDelay. When that bound
// is what ended the wait but the check itself exited 0, the check answered: a
// child it left behind merely held the pipe open after the complete response
// was written, so it is not a failure of the check (see orphanpipe).
func runLaunchCheck(cmd *exec.Cmd) ([]byte, error) {
	cmd.WaitDelay = evenerLaunchCheckWaitDelay
	out, err := cmd.CombinedOutput()
	return out, orphanpipe.ChildErr(cmd, err)
}

// requiredLaunchFlag is the serve flag the hub passes on every spawn and
// resume (the api_log floor pins both directions), so every child binary
// must advertise it in launch-check's launch_flags before the hub will
// launch it: a pre-change evener would otherwise accept the protocol, then
// die on the unknown flag before writing its rendezvous record, surfacing
// as a misleading spawn timeout.
const requiredLaunchFlag = "api-log"

func validateEvenerLaunchContract(ctx context.Context, evenerBinary, model string, env []string) error {
	if evenerBinary == "" {
		evenerBinary = "evener"
	}
	args := []string{"launch-check", "--protocol", appwire.ProtocolVersion, "--json"}
	if strings.TrimSpace(model) != "" {
		args = append(args, "--model", model)
	}
	checkCtx, cancel := context.WithTimeout(ctx, evenerLaunchCheckTimeout)
	defer cancel()
	cmd := exec.CommandContext(checkCtx, evenerBinary, args...)
	cmd.Env = env
	out, err := runLaunchCheck(cmd)
	if checkCtx.Err() != nil {
		return launchCheckWaitError(checkCtx)
	}
	if err != nil {
		msg := strings.TrimSpace(redactEnvSecrets(string(out), env))
		if msg == "" {
			msg = err.Error()
		}
		return appwire.HubLaunchError("evener launch-check failed: " + msg)
	}
	var resp struct {
		Protocol    string   `json:"protocol"`
		LaunchFlags []string `json:"launch_flags"`
	}
	if err := json.NewDecoder(bytes.NewReader(out)).Decode(&resp); err != nil {
		return appwire.HubLaunchError("evener launch-check returned invalid response")
	}
	if resp.Protocol != appwire.ProtocolVersion {
		return appwire.HubLaunchError(fmt.Sprintf("evener launch-check protocol %q does not match Hub protocol %q", resp.Protocol, appwire.ProtocolVersion))
	}
	if !slices.Contains(resp.LaunchFlags, requiredLaunchFlag) {
		return appwire.HubLaunchError(fmt.Sprintf("evener launch-check did not advertise the --%s flag: upgrade the evener binary the hub spawns", requiredLaunchFlag))
	}
	return nil
}

func listEvenerLaunchModelContract(ctx context.Context, evenerBinary string, env []string) (appwire.ModelListResponse, error) {
	if evenerBinary == "" {
		evenerBinary = "evener"
	}
	checkCtx, cancel := context.WithTimeout(ctx, evenerLaunchCheckTimeout)
	defer cancel()
	cmd := exec.CommandContext(checkCtx, evenerBinary, "launch-check", "--protocol", appwire.ProtocolVersion, "--json", "--models")
	cmd.Env = env
	out, err := runLaunchCheck(cmd)
	if checkCtx.Err() != nil {
		return appwire.ModelListResponse{}, launchCheckWaitError(checkCtx)
	}
	if err != nil {
		msg := strings.TrimSpace(redactEnvSecrets(string(out), env))
		if msg == "" {
			msg = err.Error()
		}
		return appwire.ModelListResponse{}, appwire.HubLaunchError("evener launch-check failed: " + msg)
	}
	var resp struct {
		Protocol    string                        `json:"protocol"`
		Models      []appwire.ModelDescriptor     `json:"models"`
		Diagnostics []appwire.ModelListDiagnostic `json:"diagnostics"`
	}
	if err := json.NewDecoder(bytes.NewReader(out)).Decode(&resp); err != nil {
		return appwire.ModelListResponse{}, appwire.HubLaunchError("evener launch-check returned invalid response")
	}
	if resp.Protocol != appwire.ProtocolVersion {
		return appwire.ModelListResponse{}, appwire.HubLaunchError(fmt.Sprintf("evener launch-check protocol %q does not match Hub protocol %q", resp.Protocol, appwire.ProtocolVersion))
	}
	models := make([]appwire.ModelDescriptor, 0, len(resp.Models))
	for _, model := range resp.Models {
		provider := strings.TrimSpace(model.Provider)
		name := strings.TrimSpace(model.Model)
		if provider == "" || name == "" {
			continue
		}
		models = append(models, appwire.ModelDescriptor{Provider: provider, Model: name, Warnings: append([]string(nil), model.Warnings...)})
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

// waitForRendezvousOrExit polls runDir for a rendezvous Entry whose PID
// matches, returning when one appears, when the launched child exits first, or
// when ctx ends. It is the only rendezvous wait: a second, exported copy of
// this loop with no exited arm and no possible production caller was deleted,
// having twice drifted from this one (kata waf1).
//
// A nil exited channel never fires in the select below, which is how a caller
// with no child process to watch waits on the rendezvous file alone.
func waitForRendezvousOrExit(ctx context.Context, runDir string, pid int, exited <-chan error, opts ...WaitOption) (rendezvous.Entry, error) {
	cfg := waitConfig{}
	for _, o := range opts {
		o(&cfg)
	}
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		entries, _ := listRendezvousForWait(runDir)
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
			return rendezvous.Entry{}, rendezvousWaitError(ctx)
		case err := <-exited:
			if err != nil {
				return rendezvous.Entry{}, fmt.Errorf("process exited before rendezvous: %w", err)
			}
			return rendezvous.Entry{}, errors.New("process exited before rendezvous")
		case <-ticker.C:
		}
	}
}
