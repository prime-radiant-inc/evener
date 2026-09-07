package hub

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"primeradiant.com/evener/agent"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/appsource"
	"primeradiant.com/evener/cmd/evener-hub/internal/fspaths"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/cmd/evener-hub/internal/launchconfig"
	"primeradiant.com/evener/cmdutil"
	"primeradiant.com/evener/identifier"
	"primeradiant.com/evener/internal/plugins"
	"primeradiant.com/evener/rendezvous"
)

// threadStartDetachedTimeout bounds thread/start's admitted sequence once it
// detaches from the connection context: it must comfortably cover the spawn's
// rendezvous wait (30s default) plus the ReadThread and initial StartTurn
// RPCs, while guaranteeing a wedged daemon cannot park the worker forever.
// Var, not const, so tests can shrink the bound.
var threadStartDetachedTimeout = 2 * time.Minute

var (
	hubCanonicalizeDir = fspaths.CanonicalizeDir
	hubResolveLaunch   = launchconfig.Resolve
	hubParseModelRef   = cmdutil.ParseModelRef
	hubRosterRefresh   = func(ctx context.Context, r *hubcore.Roster) error { return r.RefreshAndWait(ctx) }
	hubRosterList      = func(r *hubcore.Roster) []hubcore.LiveEntry { return r.List() }
	hubForkSession     = agent.ForkSession
	hubForkSessionAt   = agent.ForkSessionAtUserTurn
	hubAsideSession    = agent.AsideSession
	hubResolvePlugins  = func(ctx context.Context, pluginRoot string, dirs []string, enabled *[]string) (plugins.LaunchPluginResolution, error) {
		return plugins.NewManager(pluginRoot).ResolveForLaunch(ctx, dirs, enabled)
	}
)

func hubThreadStart(ctx context.Context, cfg hubcore.WebConfig, sources *appsource.Registry, params appwire.ThreadStartParams) (appwire.ThreadStartResponse, error) {
	if err := validateAppWireInputItems(params.Input); err != nil {
		return appwire.ThreadStartResponse{}, appwire.InvalidParams(err.Error())
	}
	sourceID := launchSourceID(params)
	if sourceID != "" && sourceID != "local" {
		source, ok := sources.Source(sourceID)
		if !ok || source == nil {
			return appwire.ThreadStartResponse{}, appwire.Unavailable("spawn source is not available: " + sourceID)
		}
		return source.StartThread(ctx, params)
	}
	if cfg.Spawner == nil {
		return appwire.ThreadStartResponse{}, appwire.Unavailable("spawner not configured")
	}
	workingDir := params.CWD
	if workingDir != "" {
		resolved, err := hubCanonicalizeDir(workingDir)
		if err != nil {
			return appwire.ThreadStartResponse{}, appwire.InvalidParams("cwd: " + err.Error())
		}
		workingDir = resolved
	}
	var overrides launchconfig.Layer
	if params.LaunchOverrides != nil {
		overrides = launchconfig.FromWire(*params.LaunchOverrides)
	}
	// Legacy scalar fields win over launchOverrides (per spec §5.4).
	if params.Model != "" {
		model := params.Model
		if params.ModelProvider != "" && !strings.HasPrefix(params.Model, params.ModelProvider+"/") {
			model = params.ModelProvider + "/" + params.Model
		}
		modelRef, err := hubParseModelRef(model)
		if err != nil {
			return appwire.ThreadStartResponse{}, appwire.InvalidParams(err.Error())
		}
		overrides.Model = modelRef.Qualified()
	}
	if params.Profile != "" {
		overrides.Agent = params.Profile
	}
	if params.ReasoningEffort != "" {
		overrides.ReasoningEffort = params.ReasoningEffort
	}
	if params.NonInteractive != nil {
		v := *params.NonInteractive
		overrides.NonInteractive = &v
	}
	// launch.toml is user-editable configuration (Hub UI's Launch settings
	// tab, or hand-edited), so its root is the config root, not
	// cfg.HubStateRoot (machine-generated state: auth-token, index.db,
	// deletions/).
	spawnResolved, resolveErr := hubResolveLaunch(hubLaunchConfigRoot(cfg), workingDir, overrides)
	if resolveErr != nil {
		return appwire.ThreadStartResponse{}, resolveErr
	}
	// The env floor (EVENER_MODEL etc.) applies to the spawn decision too,
	// matching the agent's own flag > env fallback: a session started now
	// would run with the env model, so the required-model gate must accept
	// it and the spawned child must receive it. Layers and per-launch
	// overrides still win — the floor only fills what nothing else set.
	// Env only, deliberately NOT the builtin floor: the agent applies its
	// own builtins, and pinning them in the hub's argv would skew across
	// versions (ApplyEnvDefaults' doc comment).
	spawnResolved = launchconfig.ApplyEnvDefaults(spawnResolved, os.Getenv, launchconfig.LaunchOptionSchema())
	resolvedModel := strings.TrimSpace(spawnResolved.Effective.Model)
	if resolvedModel == "" {
		return appwire.ThreadStartResponse{}, appwire.InvalidParams("model is required")
	}
	modelRef, err := hubParseModelRef(resolvedModel)
	if err != nil {
		return appwire.ThreadStartResponse{}, appwire.InvalidParams(err.Error())
	}
	if err := validateEvenerLaunchModel(ctx, cfg, modelRef, workingDir); err != nil {
		return appwire.ThreadStartResponse{}, err
	}
	pluginResolution, pluginErr := hubResolvePlugins(ctx, cfg.PluginRoot, spawnResolved.Effective.PluginDirs, spawnResolved.Effective.EnabledPlugins)
	if pluginErr != nil {
		// A resolver failure is fatal when a selection has to be honoured, and
		// always when the failure IS the caller leaving: the next thing this
		// handler does is detach from the request context and spawn, so a
		// cancellation walked past here becomes a session started for a client
		// that has gone. Everything else falls through to a launch with
		// whatever the resolver could list.
		if spawnResolved.Effective.EnabledPlugins != nil ||
			errors.Is(pluginErr, context.Canceled) || errors.Is(pluginErr, context.DeadlineExceeded) {
			return appwire.ThreadStartResponse{}, appwire.HubLaunchError(pluginErr.Error())
		}
	} else if err := pluginResolution.ValidateSelection(); err != nil {
		return appwire.ThreadStartResponse{}, appwire.InvalidParams(err.Error())
	}
	// One last look at the connection before the handler stops listening to
	// it. Everything above is validation, and a request the caller abandoned
	// while it ran must not become a session: past the detach below, nothing
	// asks about the caller again.
	if err := ctx.Err(); err != nil {
		return appwire.ThreadStartResponse{}, appwire.HubLaunchError(err.Error())
	}
	// The mutation is admitted here: every validation has passed and the spawn
	// is about to happen. From this point the outcome must not depend on the
	// connection's fate — a disconnecting client still gets a fully-formed
	// thread (spawn + read + optional initial turn) that reconnect resync
	// discovers via thread/list — so shed PEER-lifetime cancellation, but pair
	// it with an explicit deadline: a wedged sequence may not park the worker
	// with no cancel path (threadStartDetachedTimeout's doc covers sizing).
	// The spawned child is already detached (spawnDaemon uses exec.Command);
	// this shields only the handler's own awaits.
	ctx, cancelDetached := context.WithTimeout(context.WithoutCancel(ctx), threadStartDetachedTimeout)
	defer cancelDetached()
	entry, err := cfg.Spawner.Spawn(ctx, hubcore.SpawnRequest{
		Project:    spawnResolved.Project,
		Resolved:   spawnResolved,
		WorkingDir: workingDir,
		PluginRoot: cfg.PluginRoot,
		Provider:   modelRef.Provider,
	})
	if err != nil {
		return appwire.ThreadStartResponse{}, appwire.HubLaunchError(err.Error())
	}
	canUseSpawnEntry := entry.Protocol == appwire.ProtocolVersion && entry.Endpoint != "" && entry.ThreadID != ""
	if cfg.Roster != nil {
		if err := hubRosterRefresh(ctx, cfg.Roster); err != nil {
			if !canUseSpawnEntry {
				return appwire.ThreadStartResponse{}, appwire.Unavailable(err.Error())
			}
			// Spawning already established this daemon's identity. An unrelated
			// discovery failure must not hide its identity or discard initial input.
			fmt.Fprintf(os.Stderr, "[hub] spawned session %s; roster refresh failed: %v\n", entry.ThreadID, err)
		}
		if entry.ThreadID == "" || entry.SessionID == "" {
			for _, live := range hubRosterList(cfg.Roster) {
				if live.PID == entry.PID {
					if entry.ThreadID == "" {
						entry.ThreadID = live.SessionID
					}
					if entry.SessionID == "" {
						entry.SessionID = live.SessionID
					}
					break
				}
			}
		}
	}
	ref := localSpawnWorkspaceRef(entry)
	var source appsource.Source
	if canUseSpawnEntry {
		// SpawnDaemon already returned this exact, freshly published rendezvous
		// entry. Route the initial read and turn through it directly instead of
		// depending on a concurrent roster status probe to admit the new daemon.
		source = appsource.NewLocalDaemonSource("local", func() []rendezvous.Entry {
			return []rendezvous.Entry{entry}
		}, nil)
	} else {
		source, err = sourceForThread(sources, ref, "")
	}
	if err != nil {
		if entry.ThreadID == "" {
			return appwire.ThreadStartResponse{}, err
		}
		thread := appwire.Thread{
			ID:            entry.ThreadID,
			SessionID:     entry.SessionID,
			Preview:       entry.SessionID,
			ModelProvider: modelRef.Provider,
			CWD:           workingDir,
			Source:        "local",
			Status:        appwire.ThreadStatus{Type: appwire.ThreadStatusIdle},
			Evener:        appwire.EvenerThread{Ref: ref, InstanceID: localSpawnInstanceID(entry, appwire.Thread{})},
		}
		annotateThreadProjects([]appwire.Thread{thread})
		return appwire.ThreadStartResponse{Thread: thread}, nil
	}
	threadResp, err := source.ReadThread(ctx, appwire.ThreadReadParams{Ref: ref})
	if err != nil {
		threadResp.Thread = appwire.Thread{
			ID: entry.ThreadID, SessionID: entry.SessionID, CWD: workingDir,
			Source: "local", Evener: appwire.EvenerThread{Ref: ref, InstanceID: localSpawnInstanceID(entry, appwire.Thread{})},
		}
	}
	expectedInstanceID := localSpawnInstanceID(entry, threadResp.Thread)
	annotateThreadProjects([]appwire.Thread{threadResp.Thread})
	turn := appwire.Turn{}
	if len(params.Input) > 0 {
		clientMutationID, err := identifier.NewClientMutationID()
		if err != nil {
			return appwire.ThreadStartResponse{}, appwire.InternalError("create initial turn mutation id: " + err.Error())
		}
		turnResp, err := source.StartTurn(ctx, appwire.TurnStartParams{
			Ref:                ref,
			ClientMutationID:   clientMutationID,
			ExpectedInstanceID: expectedInstanceID,
			Input:              params.Input,
		})
		if err != nil {
			return appwire.ThreadStartResponse{}, err
		}
		turn = turnResp.Turn
	}
	return appwire.ThreadStartResponse{Thread: threadResp.Thread, Turn: turn}, nil
}

func localSpawnWorkspaceRef(entry rendezvous.Entry) string {
	if ref, err := appwire.ParseRef(strings.TrimSpace(entry.WorkspaceRef)); err == nil && ref.SourceID == "local" {
		return ref.String()
	}
	threadID := strings.TrimSpace(entry.ThreadID)
	if threadID == "" {
		threadID = strings.TrimSpace(entry.SessionID)
	}
	return appwire.Ref{SourceID: "local", ThreadID: threadID}.String()
}

func localSpawnInstanceID(entry rendezvous.Entry, thread appwire.Thread) string {
	for _, candidate := range []string{
		entry.InstanceID,
		entry.SessionID,
		thread.Evener.InstanceID,
		entry.ThreadID,
	} {
		if candidate = strings.TrimSpace(candidate); candidate != "" {
			return candidate
		}
	}
	return ""
}

func launchSourceID(params appwire.ThreadStartParams) string {
	harness := strings.TrimSpace(params.Harness)
	if harness != "" {
		if harness == "evener" {
			return "local"
		}
		return harness
	}
	return ""
}

func hubThreadResume(ctx context.Context, cfg hubcore.WebConfig, sources *appsource.Registry, params appwire.ThreadResumeParams) (appwire.ThreadResumeResponse, error) {
	if params.Ref != "" {
		ref, err := appwire.ParseRef(params.Ref)
		if err != nil {
			return appwire.ThreadResumeResponse{}, err
		}
		if ref.SourceID != "local" {
			source, err := sourceForThread(sources, params.Ref, "")
			if err != nil {
				return appwire.ThreadResumeResponse{}, err
			}
			return source.ResumeThread(ctx, params)
		}
	}
	sessionID := strings.TrimSpace(params.Session)
	if sessionID == "" && params.Ref != "" {
		// A non-empty ref was parsed at function entry, so this cannot fail.
		ref, _ := appwire.ParseRef(params.Ref)
		sessionID = ref.ThreadID
	}
	if sessionID == "" {
		return appwire.ThreadResumeResponse{}, appwire.InvalidParams("sessionId or ref is required")
	}
	if cfg.ResumeLocks != nil {
		lock := cfg.ResumeLocks.For(sessionID)
		lock.Lock()
		defer lock.Unlock()
	}
	if err := deletionFenceError(cfg, params.Ref, sessionID, ""); err != nil {
		return appwire.ThreadResumeResponse{}, err
	}
	if cfg.Roster != nil {
		if err := hubRosterRefresh(ctx, cfg.Roster); err != nil {
			return appwire.ThreadResumeResponse{}, appwire.Unavailable(err.Error())
		}
	}
	if err := daemonRestartRequiredError(ctx, cfg, params.Ref, sessionID, ""); err != nil {
		return appwire.ThreadResumeResponse{}, err
	}
	if cfg.Spawner == nil {
		return appwire.ThreadResumeResponse{}, appwire.Unavailable("spawner not configured")
	}
	resumeReq, err := resumeRequestForConfig(cfg, sessionID)
	if err != nil {
		return appwire.ThreadResumeResponse{}, appwire.HubLaunchError(err.Error())
	}
	// Serialize concurrent resumes of the same session behind a per-session
	// lock shared with the REST send path (kata sm1a). While one resume holds
	// the lock, another RPC mutation that also decided to resume waits here
	// rather than spawning a second daemon for the same exited session.
	if cfg.ResumeLocks != nil {
		// Double-check under the lock: a resume that completed while we waited
		// has already put the session in the roster, so reuse it instead of
		// spawning again. Only this Hub's exact flag-day protocol establishes
		// ownership; an older daemon can be healthy while remaining unroutable
		// through the current local source. A dead daemon may remain as a
		// crash marker and must fall through to spawning.
		if cfg.Roster != nil {
			if le, ok := liveDaemonForThread(cfg.Roster, sessionID); ok &&
				le.Protocol == appwire.ProtocolVersion {
				return hubResumedThreadResponse(ctx, sources, le.SessionID, le.ThreadID)
			}
		}
	}
	entry, err := cfg.Spawner.Resume(ctx, resumeReq)
	if err != nil {
		return appwire.ThreadResumeResponse{}, appwire.HubLaunchError(resumeFailureError(ctx, cfg, sessionID, err).Error())
	}
	if cfg.Roster != nil {
		if err := hubRosterRefresh(ctx, cfg.Roster); err != nil {
			if entry.Protocol != appwire.ProtocolVersion || entry.Endpoint == "" || entry.ThreadID == "" {
				return appwire.ThreadResumeResponse{}, appwire.Unavailable(err.Error())
			}
			if confirmErr := cfg.Roster.RefreshEntry(ctx, entry); confirmErr != nil {
				return appwire.ThreadResumeResponse{}, appwire.Unavailable(errors.Join(err, confirmErr).Error())
			}
			fmt.Fprintf(os.Stderr, "[hub] resumed session %s; roster refresh failed: %v\n", entry.ThreadID, err)
		}
	}
	return hubResumedThreadResponse(ctx, sources, entry.SessionID, entry.ThreadID)
}

// resumeFailureError explains a failed replacement spawn when the daemon this
// hub refused to reuse is STILL running. That daemon holds the session's
// exclusive API-log reservation, so no replacement can start until it stops,
// and the spawn failure that comes back names only the locked file — it is
// raised inside the child process, which has no idea a hub is replacing an
// incompatible daemon, and its stock advice ("send work to the live session")
// is the one thing that cannot work here. The hub is the only party holding
// the blocking daemon's pid and address, so it owes the operator both plus the
// command that releases the session (kata ew86).
//
// Every hub path that resumes a local session runs into the same wedge, so
// they all report it through here: hubThreadResume above, which serves both
// /rpc thread/resume and the turn/start auto-resume. The original failure stays wrapped so a
// caller that inspects the error, rather than its text, still sees what the
// spawner returned.
//
// The roster is re-read rather than reused from the pre-spawn check: the spawn
// attempt takes seconds, and naming a pid that has since exited would send the
// operator after a process that is not there.
func resumeFailureError(ctx context.Context, cfg hubcore.WebConfig, sessionID string, err error) error {
	if cfg.Roster == nil {
		return err
	}
	if refreshErr := hubRosterRefresh(ctx, cfg.Roster); refreshErr != nil {
		return errors.Join(err, refreshErr)
	}
	blocker, ok := cfg.Roster.Find(sessionID)
	if !ok || blocker.Crashed || blocker.Protocol == appwire.ProtocolVersion {
		return err
	}
	remedy := fmt.Sprintf("kill %d", blocker.PID)
	return fmt.Errorf(
		"session %s is still held by live daemon pid %d (AppWire protocol %q; this hub speaks %q), which the hub can neither route to nor replace. Stop it and resume again: %s. Replacement spawn failed: %w",
		sessionID, blocker.PID, blocker.Protocol, appwire.ProtocolVersion, remedy, err)
}

// hubResumedThreadResponse reads the freshly-resumed local thread back and
// wraps it in a ThreadResumeResponse. It is the shared tail of hubThreadResume:
// both a fresh spawn and the double-check reuse of an already-resumed daemon
// resolve the thread the same way. threadID falls back to sessionID when the
// rendezvous entry omitted it.
func hubResumedThreadResponse(ctx context.Context, sources *appsource.Registry, sessionID, threadID string) (appwire.ThreadResumeResponse, error) {
	if threadID == "" {
		threadID = sessionID
	}
	ref := appwire.Ref{SourceID: "local", ThreadID: threadID}.String()
	source, err := sourceForThread(sources, ref, "")
	if err != nil {
		return appwire.ThreadResumeResponse{}, err
	}
	threadResp, err := source.ReadThread(ctx, appwire.ThreadReadParams{Ref: ref})
	if err != nil {
		return appwire.ThreadResumeResponse{}, err
	}
	annotateThreadProjects([]appwire.Thread{threadResp.Thread})
	return appwire.ThreadResumeResponse{Thread: threadResp.Thread}, nil
}

func resumeRequestForConfig(cfg hubcore.WebConfig, id string) (hubcore.ResumeRequest, error) {
	req := hubcore.ResumeRequest{SessionID: id}
	if cfg.Past != nil {
		if pe, ok := cfg.Past.Find(id); ok {
			// Restore root, not the live working dir: a session actively
			// inside a worktree must resume at its pre-worktree home so
			// Task 18's resume re-entry (not this `--dir`) takes it back
			// into the worktree, honoring the lock/validation rules there
			// (native worktree tools spec §7 "Hub consumers").
			req.WorkingDir = hubcore.EffectiveWorkingDir(pe.Meta)
			req.StateDir = pe.StateDir
			provider := strings.TrimSpace(pe.Meta.ProfileID)
			if provider == "" {
				return hubcore.ResumeRequest{}, fmt.Errorf("session %s has no provider profile: cannot resume", id)
			}
			project, projectErr := identifier.ResolveProject(req.WorkingDir)
			if projectErr != nil {
				return hubcore.ResumeRequest{}, fmt.Errorf("resolve resume project: %w", projectErr)
			}
			req.Project = project
			if pe.Meta.Model != "" {
				req.Provider = provider
				req.Resolved = launchconfig.Resolved{Effective: launchconfig.Layer{
					Model: provider + "/" + pe.Meta.Model,
				}}
			}
		}
	}
	return req, nil
}

func hubThreadFork(ctx context.Context, cfg hubcore.WebConfig, sources *appsource.Registry, params appwire.ThreadForkParams) (appwire.ThreadForkResponse, error) {
	ref, err := appwire.ParseRef(params.Ref)
	if err != nil {
		return appwire.ThreadForkResponse{}, err
	}
	if ref.SourceID != "local" {
		if params.Aside {
			return appwire.ThreadForkResponse{}, appwire.Unavailable("aside is only supported for local evener threads")
		}
		return withDeletionTargetOwnership(ctx, cfg, params.Ref, "", "", func() (appwire.ThreadForkResponse, error) {
			source, err := sourceForThread(sources, params.Ref, "")
			if err != nil {
				return appwire.ThreadForkResponse{}, err
			}
			if threadForkRequiresTurnCapability(params) {
				if err := ensureThreadActionAvailable(ctx, source, params.Ref, "", "fork"); err != nil {
					return appwire.ThreadForkResponse{}, err
				}
			}
			return source.ForkThread(ctx, params)
		})
	}
	unlockDeletionTarget := lockDeletionTarget(cfg, params.Ref, ref.ThreadID)
	defer unlockDeletionTarget()
	if err := deletionFenceError(cfg, params.Ref, ref.ThreadID, ""); err != nil {
		return appwire.ThreadForkResponse{}, err
	}
	if params.Aside {
		if strings.TrimSpace(params.SourceTurnID) != "" || strings.TrimSpace(params.EditedInput) != "" || strings.TrimSpace(params.Label) != "" || params.DeferInput {
			return appwire.ThreadForkResponse{}, appwire.InvalidParams("aside does not accept sourceTurnId, editedInput, deferInput, or label")
		}
		stateDir := cfg.StateDir
		if cfg.Past != nil {
			if pe, ok := cfg.Past.Find(ref.ThreadID); ok {
				stateDir = pe.StateDir
			}
		}
		if stateDir == "" {
			return appwire.ThreadForkResponse{}, appwire.Unavailable("state dir not resolvable for parent thread")
		}
		if err := refreshDaemonRestartRequiredError(ctx, cfg, params.Ref, ref.ThreadID, ""); err != nil {
			return appwire.ThreadForkResponse{}, err
		}
		childID, err := hubAsideSession(stateDir, ref.ThreadID)
		if err != nil {
			return appwire.ThreadForkResponse{}, err
		}
		if cfg.Past != nil {
			_, _ = cfg.Past.Rebuild()
		}
		childRef := appwire.Ref{SourceID: "local", ThreadID: childID}.String()
		return appwire.ThreadForkResponse{Thread: appwire.Thread{
			ID:        childID,
			SessionID: childID,
			Source:    "local",
			Evener:    appwire.EvenerThread{Ref: childRef},
		}}, nil
	}
	turn, err := parseSourceTurnID(params.SourceTurnID)
	if err != nil {
		return appwire.ThreadForkResponse{}, appwire.InvalidParams(err.Error())
	}
	if params.DeferInput && strings.TrimSpace(params.EditedInput) != "" {
		return appwire.ThreadForkResponse{}, appwire.InvalidParams("editedInput and deferInput are mutually exclusive")
	}
	if !params.DeferInput && strings.TrimSpace(params.EditedInput) == "" {
		return appwire.ThreadForkResponse{}, appwire.InvalidParams("editedInput is required")
	}
	stateDir := cfg.StateDir
	if cfg.Past != nil {
		if pe, ok := cfg.Past.Find(ref.ThreadID); ok {
			stateDir = pe.StateDir
		}
	}
	if stateDir == "" {
		return appwire.ThreadForkResponse{}, appwire.Unavailable("state dir not resolvable for parent thread")
	}
	if err := refreshDaemonRestartRequiredError(ctx, cfg, params.Ref, ref.ThreadID, ""); err != nil {
		return appwire.ThreadForkResponse{}, err
	}
	var childID, originalInput string
	if params.DeferInput {
		childID, originalInput, err = hubForkSessionAt(stateDir, ref.ThreadID, turn, params.Label)
	} else {
		childID, err = hubForkSession(stateDir, ref.ThreadID, turn, params.EditedInput, params.Label)
	}
	if err != nil {
		return appwire.ThreadForkResponse{}, err
	}
	if cfg.Past != nil {
		_, _ = cfg.Past.Rebuild()
	}
	childRef := appwire.Ref{SourceID: "local", ThreadID: childID}.String()
	return appwire.ThreadForkResponse{
		Thread: appwire.Thread{
			ID:        childID,
			SessionID: childID,
			Source:    "local",
			Evener:    appwire.EvenerThread{Ref: childRef},
		},
		OriginalInput: originalInput,
	}, nil
}

func threadForkRequiresTurnCapability(params appwire.ThreadForkParams) bool {
	return strings.TrimSpace(params.SourceTurnID) != "" ||
		strings.TrimSpace(params.EditedInput) != "" ||
		strings.TrimSpace(params.Label) != "" ||
		params.DeferInput
}

func parseSourceTurnID(raw string) (int, error) {
	raw = strings.TrimSpace(strings.TrimPrefix(raw, "turn_"))
	if raw == "" {
		return 0, errors.New("sourceTurnId is required")
	}
	turn, err := strconv.Atoi(raw)
	if err != nil || turn < 1 {
		return 0, errors.New("sourceTurnId must be a positive turn number")
	}
	return turn, nil
}
