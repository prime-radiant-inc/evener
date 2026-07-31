package main

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"primeradiant.com/serf/agent"
	"primeradiant.com/serf/appwire"
	"primeradiant.com/serf/cmd/serf-hub/internal/appsource"
	"primeradiant.com/serf/cmd/serf-hub/internal/codexlaunch"
	"primeradiant.com/serf/cmd/serf-hub/internal/fspaths"
	"primeradiant.com/serf/cmd/serf-hub/internal/hubcore"
	"primeradiant.com/serf/cmd/serf-hub/internal/launchconfig"
	"primeradiant.com/serf/cmdutil"
	"primeradiant.com/serf/identifier"
	"primeradiant.com/serf/rendezvous"
)

var (
	hubCanonicalizeDir = fspaths.CanonicalizeDir
	hubResolveLaunch   = launchconfig.Resolve
	hubParseModelRef   = cmdutil.ParseModelRef
	hubRosterRefresh   = func(r *hubcore.Roster) { r.Refresh() }
	hubRosterList      = func(r *hubcore.Roster) []hubcore.LiveEntry { return r.List() }
	hubForkSession     = agent.ForkSession
	hubForkSessionAt   = agent.ForkSessionAtUserTurn
	hubAsideSession    = agent.AsideSession
	hubEnsureSource    = func(ctx context.Context, launcher *codexlaunch.CodexLauncher, id string, sources *appsource.Registry) (appsource.Source, error) {
		return launcher.EnsureSource(ctx, id, sources)
	}
)

func hubThreadStart(ctx context.Context, cfg hubcore.WebConfig, sources *appsource.Registry, params appwire.ThreadStartParams) (appwire.ThreadStartResponse, error) {
	if err := validateAppWireInputItems(params.Input); err != nil {
		return appwire.ThreadStartResponse{}, appwire.InvalidParams(err.Error())
	}
	sourceID := launchSourceID(params)
	if sourceID != "" && sourceID != "local" {
		var source appsource.Source
		if cfg.CodexLauncher != nil && cfg.CodexLauncher.Manages(sourceID) {
			launched, err := hubEnsureSource(ctx, cfg.CodexLauncher, sourceID, sources)
			if err != nil {
				return appwire.ThreadStartResponse{}, err
			}
			source = launched
		} else {
			var ok bool
			source, ok = sources.Source(sourceID)
			if !ok {
				if cfg.CodexLauncher == nil {
					return appwire.ThreadStartResponse{}, appwire.Unavailable("spawn source is not available: " + sourceID)
				}
				launched, err := hubEnsureSource(ctx, cfg.CodexLauncher, sourceID, sources)
				if err != nil {
					return appwire.ThreadStartResponse{}, err
				}
				source = launched
			}
		}
		if source == nil {
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
	spawnResolved, resolveErr := hubResolveLaunch(cfg.HubStateRoot, workingDir, overrides)
	if resolveErr != nil {
		return appwire.ThreadStartResponse{}, resolveErr
	}
	resolvedModel := strings.TrimSpace(spawnResolved.Effective.Model)
	if resolvedModel == "" {
		return appwire.ThreadStartResponse{}, appwire.InvalidParams("model is required")
	}
	modelRef, err := hubParseModelRef(resolvedModel)
	if err != nil {
		return appwire.ThreadStartResponse{}, appwire.InvalidParams(err.Error())
	}
	if err := validateSerfLaunchModel(ctx, cfg, modelRef, workingDir); err != nil {
		return appwire.ThreadStartResponse{}, err
	}
	entry, err := cfg.Spawner.Spawn(ctx, hubcore.SpawnRequest{
		Project:    spawnResolved.Project,
		Resolved:   spawnResolved,
		WorkingDir: workingDir,
		Provider:   modelRef.Provider,
	})
	if err != nil {
		return appwire.ThreadStartResponse{}, appwire.HubLaunchError(err.Error())
	}
	if cfg.Roster != nil {
		hubRosterRefresh(cfg.Roster)
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
	ref := appwire.Ref{SourceID: "local", ThreadID: entry.ThreadID}.String()
	var source appsource.Source
	if entry.Protocol == appwire.ProtocolVersion && entry.Endpoint != "" && entry.ThreadID != "" {
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
			Serf:          appwire.SerfThread{Ref: ref},
		}
		annotateThreadProjects([]appwire.Thread{thread})
		return appwire.ThreadStartResponse{Thread: thread}, nil
	}
	threadResp, err := source.ReadThread(ctx, appwire.ThreadReadParams{Ref: ref})
	if err != nil {
		threadResp.Thread = appwire.Thread{
			ID: entry.ThreadID, SessionID: entry.SessionID, CWD: workingDir,
			Source: "local", Serf: appwire.SerfThread{Ref: ref},
		}
	}
	annotateThreadProjects([]appwire.Thread{threadResp.Thread})
	turn := appwire.Turn{}
	if len(params.Input) > 0 {
		clientMutationID, err := identifier.NewClientMutationID()
		if err != nil {
			return appwire.ThreadStartResponse{}, appwire.InternalError("create initial turn mutation id: " + err.Error())
		}
		turnResp, err := source.StartTurn(ctx, appwire.TurnStartParams{
			Ref:              ref,
			ClientMutationID: clientMutationID,
			Input:            params.Input,
		})
		if err != nil {
			return appwire.ThreadStartResponse{}, err
		}
		turn = turnResp.Turn
	}
	return appwire.ThreadStartResponse{Thread: threadResp.Thread, Turn: turn}, nil
}

func launchSourceID(params appwire.ThreadStartParams) string {
	harness := strings.TrimSpace(params.Harness)
	if harness != "" {
		if harness == "serf" {
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
			var source appsource.Source
			if cfg.CodexLauncher != nil && cfg.CodexLauncher.Manages(ref.SourceID) {
				launched, err := hubEnsureSource(ctx, cfg.CodexLauncher, ref.SourceID, sources)
				if err != nil {
					return appwire.ThreadResumeResponse{}, err
				}
				source = launched
			} else {
				var err error
				source, err = sourceForThread(sources, params.Ref, "")
				if err != nil {
					if cfg.CodexLauncher == nil {
						return appwire.ThreadResumeResponse{}, err
					}
					launched, launchErr := hubEnsureSource(ctx, cfg.CodexLauncher, ref.SourceID, sources)
					if launchErr != nil {
						return appwire.ThreadResumeResponse{}, launchErr
					}
					source = launched
				}
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
		// through the current local source. Refresh also preserves a dead daemon
		// as a crash marker for diagnostics. Both cases must fall through
		// to spawning.
		if cfg.Roster != nil {
			hubRosterRefresh(cfg.Roster)
			if le, ok := cfg.Roster.Find(sessionID); ok &&
				!le.Crashed &&
				le.Protocol == appwire.ProtocolVersion {
				return hubResumedThreadResponse(ctx, sources, le.SessionID, le.ThreadID)
			}
		}
	}
	entry, err := cfg.Spawner.Resume(ctx, resumeReq)
	if err != nil {
		return appwire.ThreadResumeResponse{}, appwire.HubLaunchError(resumeFailureMessage(cfg, sessionID, err))
	}
	if cfg.Roster != nil {
		hubRosterRefresh(cfg.Roster)
	}
	return hubResumedThreadResponse(ctx, sources, entry.SessionID, entry.ThreadID)
}

// resumeFailureMessage explains a failed replacement spawn when the daemon the
// ownership predicate above refused to reuse is STILL running. That daemon
// holds the session's exclusive API-log reservation, so no replacement can
// start until it stops, and the spawn failure that comes back names only the
// locked file — it is raised inside the child process, which has no idea a hub
// is replacing an incompatible daemon, and its stock advice ("send work to the
// live session") is the one thing that cannot work here. The hub is the only
// party holding the blocking daemon's pid and address, so it owes the operator
// both plus the command that releases the session (kata ew86).
//
// The roster is re-read rather than reused from the pre-spawn check: the spawn
// attempt takes seconds, and naming a pid that has since exited would send the
// operator after a process that is not there.
func resumeFailureMessage(cfg hubcore.WebConfig, sessionID string, err error) string {
	if cfg.Roster == nil {
		return err.Error()
	}
	hubRosterRefresh(cfg.Roster)
	blocker, ok := cfg.Roster.Find(sessionID)
	if !ok || blocker.Crashed || blocker.Protocol == appwire.ProtocolVersion {
		return err.Error()
	}
	remedy := fmt.Sprintf("kill %d", blocker.PID)
	if blocker.Address != "" {
		remedy = fmt.Sprintf("curl -X POST http://%s/shutdown (or kill %d)", blocker.Address, blocker.PID)
	}
	return fmt.Sprintf(
		"session %s is still held by live daemon pid %d (AppWire protocol %q; this hub speaks %q), which the hub can neither route to nor replace. Stop it and resume again: %s. Replacement spawn failed: %v",
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
			return appwire.ThreadForkResponse{}, appwire.Unavailable("aside is only supported for local serf threads")
		}
		return withDeletionTargetOwnership(cfg, params.Ref, "", "", func() (appwire.ThreadForkResponse, error) {
			source, err := sourceForThreadWithManagedLaunchUnlocked(ctx, cfg, sources, params.Ref, "")
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
			Serf:      appwire.SerfThread{Ref: childRef},
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
			Serf:      appwire.SerfThread{Ref: childRef},
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
