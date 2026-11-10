package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync/atomic"

	"primeradiant.com/evener/agent"
	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/plugin"
	"primeradiant.com/evener/agent/provider"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener/internal/rvreg"
	"primeradiant.com/evener/cmdutil"
	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/registry"
	"primeradiant.com/evener/server"
)

// bindServerCallbacks installs every session- and daemon-scoped RPC hook on the
// server, including thread/clear. Each hook resolves the live session per call,
// so a thread/clear replacement is never pinned. It returns the envelope
// source it installed, whose PendingEscalations the startup write reports.
func bindServerCallbacks(ctx context.Context, srv serveServer, deps serveDeps, live *liveSession, bridge *serveBridge, retiring *serveRetirement, rvRegistration *rvreg.Registration, sessionCfg agent.SessionConfig, client *llm.Client, profile *provider.Profile, wd, sd string, logOut io.Writer, workspaceRef string, servedBootGeneration *atomic.Uint64, cancel context.CancelFunc) liveThreadEnvelopeSource {
	srv.SetSandboxEscalationResolveFunc(func(id string, approve bool) error {
		return live.session().ResolveSandboxEscalation(id, approve)
	})
	if managerApproval, ok := srv.(interface {
		SetSandboxEscalationResolveAsFunc(func(string, bool, schema.SessionActor) error)
	}); ok {
		managerApproval.SetSandboxEscalationResolveAsFunc(func(id string, approve bool, actor schema.SessionActor) error {
			return live.session().ResolveSandboxEscalationAs(id, approve, actor)
		})
	}
	srv.SetDelegateStopFunc(func(delegateID string) (appwire.DelegateStopOutcome, error) {
		return live.session().StopDelegateRun(delegateID)
	})
	if jobsStop, ok := srv.(interface {
		SetJobStopFunc(func(context.Context, appwire.SessionStopRequest) (appwire.SessionStopResponse, error))
	}); ok {
		jobsStop.SetJobStopFunc(func(ctx context.Context, request appwire.SessionStopRequest) (appwire.SessionStopResponse, error) {
			var result appwire.SessionJobStopResponse
			var err error
			if request.Job != nil {
				result, err = live.session().StopJobForManager(ctx, *request.Job)
			} else {
				result, err = live.session().StopOwnedDelegateForManager(ctx, *request.OwnedDelegate)
			}
			if err != nil {
				return appwire.SessionStopResponse{}, err
			}
			return appwire.SessionStopResponse{Job: &result}, nil
		})
	}
	srv.SetCompactFunc(func(ctx context.Context) error { return live.session().Compact(ctx) })
	srv.SetGoalFunc(func(objective string) (bool, error) {
		if strings.TrimSpace(objective) == "" {
			return false, live.session().ClearGoal()
		}
		return live.session().SetGoal(ctx, objective)
	})
	srv.SetNotesHumanSetFunc(func(outerID, note string) (appwire.NotesHumanSetResponse, error) {
		return live.session().SetHumanNote(outerID, note)
	})
	srv.SetUrlsRemoveFunc(func(outerID, id string) (bool, error) {
		return live.session().RemoveSessionURL(outerID, id)
	})
	srv.SetRetrySafeTurnFunctions(server.RetrySafeTurnFunctions{
		Start: func(params appwire.TurnStartParams) (appwire.TurnStartResponse, error) {
			return live.session().AcceptClientMutationStart(params)
		},
		StartAs: func(params appwire.TurnStartParams, actor schema.SessionActor) (appwire.TurnStartResponse, error) {
			return live.session().AcceptClientMutationStartAs(params, actor)
		},
		Steer: func(params appwire.TurnSteerParams) (appwire.TurnSteerResponse, error) {
			return live.session().AcceptClientMutationSteer(params)
		},
		SteerAs: func(params appwire.TurnSteerParams, actor schema.SessionActor) (appwire.TurnSteerResponse, error) {
			return live.session().AcceptClientMutationSteerAs(params, actor)
		},
		Queue: func(params appwire.TurnQueueParams) (appwire.TurnQueueResponse, error) {
			return live.session().AcceptClientMutationQueue(params)
		},
		QueueAs: func(params appwire.TurnQueueParams, actor schema.SessionActor) (appwire.TurnQueueResponse, error) {
			return live.session().AcceptClientMutationQueueAs(params, actor)
		},
		Drain: func(params appwire.TurnDrainAsSteerParams) (appwire.TurnDrainAsSteerResponse, error) {
			return live.session().AcceptClientMutationDrainAsSteer(params)
		},
		DrainAs: func(params appwire.TurnDrainAsSteerParams, actor schema.SessionActor) (appwire.TurnDrainAsSteerResponse, error) {
			return live.session().AcceptClientMutationDrainAsSteerAs(params, actor)
		},
		Promote: func(params appwire.TurnPromoteQueuedAsSteerParams) (appwire.TurnPromoteQueuedAsSteerResponse, error) {
			return live.session().AcceptClientMutationPromoteQueuedAsSteer(params)
		},
		PromoteAs: func(params appwire.TurnPromoteQueuedAsSteerParams, actor schema.SessionActor) (appwire.TurnPromoteQueuedAsSteerResponse, error) {
			return live.session().AcceptClientMutationPromoteQueuedAsSteerAs(params, actor)
		},
		Cancel: func(params appwire.TurnCancelQueuedParams) (appwire.TurnCancelQueuedResponse, error) {
			return live.session().AcceptClientMutationCancelQueued(params)
		},
		Interrupt: func(waitCtx context.Context, params appwire.TurnInterruptParams) (appwire.TurnInterruptResponse, error) {
			return live.session().InterruptClientMutation(waitCtx, params, func() {
				bridge.cancelAndWaitMutationRunner()
			})
		},
	})
	// One seam replaces sixteen read-time callbacks. The daemon samples session
	// state through this at the moments it changes (server/thread_envelope.go's
	// facetsByEvent) and never on a read.
	envelopeSource := liveThreadEnvelopeSource{
		session: func() agent.EnvelopeSampling { return live.session() },
	}
	srv.SetThreadEnvelopeSource(envelopeSource)
	srv.SetModelFunc(func(model string) error {
		sess := live.session()
		// SetModel queues a pick made while a turn runs (applied at the turn
		// boundary) and applies one made at rest immediately.
		if err := sess.SetModel(model); err != nil {
			return err
		}
		// Refresh the daemon's cached session info for the RPC's own reply
		// window (G2): the bridge's EventModelChanged effect refreshes it on
		// its drain goroutine, so a switch that committed at RPC time would
		// read stale from thread/read until that drain runs. The sampled form
		// reads the profile inside the write's critical section: a queued
		// pick's boundary commit racing this refresh cannot have a stale
		// snapshot installed after the projection already installed the new
		// model.
		srv.UpdateSessionInfoFrom(sess.ID(), func() (string, string) {
			p := sess.Profile()
			return p.Model(), p.ID()
		})
		return nil
	})
	srv.SetNameFunc(func(name string) error { return live.session().Rename(name) })
	srv.SetReasoningEffortFunc(func(effort string) error { return live.session().SetReasoningEffort(effort) })
	// Resolve the session per call like the model/effort hooks above: binding one
	// session's method value here would pin the hook to the pre-thread/clear session.
	srv.SetVisionModelFunc(func(v string) error { return live.session().SetVisionModel(v) })
	srv.SetListModelsFunc(cmdutil.ListModelsFunc(client, profile.ID()))
	// Resolved per call like the model hook above: the replacement session a
	// thread/clear installs resolves on its own registry.
	srv.SetCostLookupFunc(func(ref string) *registry.Cost { return live.session().CostFor(ref) })
	srv.SetTasksFunc(func() []appwire.Task { return live.session().Tasks() })
	if sessionTools, ok := srv.(interface{ SetSessionTools(func() *agent.Session) }); ok {
		sessionTools.SetSessionTools(live.session)
	}
	localizeRef := func(sess *agent.Session, ref string) string {
		if ref != workspaceRef {
			return ref
		}
		return appwire.LocalRef(sess.ID()).String()
	}
	srv.SetThreadActivityReadFunc(func(ctx context.Context, params appwire.SessionActivityReadParams) (appwire.SessionActivitySummary, error) {
		sess := live.session()
		params.Ref = localizeRef(sess, params.Ref)
		return sess.ActivitySummary(ctx, params)
	})
	srv.SetThreadDelegatesListFunc(func(ctx context.Context, params appwire.SessionActivityListParams) (appwire.SessionDelegatesResponse, error) {
		sess := live.session()
		params.Ref = localizeRef(sess, params.Ref)
		return sess.ListActivityDelegates(ctx, params)
	})
	srv.SetThreadJobsListFunc(func(ctx context.Context, params appwire.SessionActivityListParams) (appwire.SessionJobsResponse, error) {
		sess := live.session()
		params.Ref = localizeRef(sess, params.Ref)
		return sess.ListActivityJobs(ctx, params)
	})
	srv.SetThreadWatchesListFunc(func(ctx context.Context, params appwire.SessionActivityListParams) (appwire.SessionWatchesResponse, error) {
		sess := live.session()
		params.Ref = localizeRef(sess, params.Ref)
		return sess.ListActivityWatches(ctx, params)
	})

	srv.SetJobsFunc(func(ctx context.Context, params appwire.JobsListParams) (any, error) {
		sess := live.session()
		if strings.TrimSpace(params.Ref) != "" {
			ref, err := appwire.ParseRef(params.Ref)
			if err != nil {
				return nil, appwire.InvalidParams(err.Error())
			}
			if ref.SourceID != appwire.LocalSourceID || params.Ref != workspaceRef {
				return nil, appwire.SessionUnavailable("thread not found: " + ref.ThreadID)
			}
			// The public ref names the stable workspace. JobActivityTree reads the
			// live session's activity store, so translate that stable ref to the
			// current instance after a thread/clear replacement.
			params.Ref = appwire.LocalRef(sess.ID()).String()
		}
		return sess.JobActivityTree(ctx, params)
	})
	srv.SetJobOutputFunc(func(jobID string, beforeBytes *int64, maxBytes int64) (appwire.JobOutputPage, bool, error) {
		return live.session().JobOutputPage(jobID, beforeBytes, maxBytes)
	})
	srv.SetJobGetFunc(func(jobID string) (appwire.JobActivityJob, bool, error) {
		return live.session().JobGet(jobID)
	})
	srv.SetClearFunc(func(ctx context.Context, _ appwire.ThreadClearParams) error {
		oldSess := live.session()
		// The admission lease runs from BEFORE any durable construction to
		// after the new root is published and the old one settled: retirement
		// can neither claim the tree being replaced nor start its interval
		// against the half-built replacement. Rejection here means the daemon
		// is already preparing/retiring and the clear must not begin.
		releaseAdmission, err := retiring.retirement.BeginMutation(oldSess.ID(), "admission")
		if err != nil {
			return err
		}
		defer releaseAdmission()
		oldEnv := live.environment()
		oldMeta := oldSess.Meta()
		// A session that started in, or entered, a worktree lane keeps that
		// lane across clear. The launch directory is only the pre-worktree
		// home; the live session owns the lane (#2957).
		oldLane := oldSess.WorktreeLane()
		lane := strings.TrimSpace(oldLane.Path)
		clearWd := wd
		if lane != "" {
			info, err := os.Stat(lane)
			if err != nil {
				return fmt.Errorf("clear: worktree lane %s is unavailable: %w", lane, err)
			}
			if !info.IsDir() {
				return fmt.Errorf("clear: worktree lane %s is not a directory", lane)
			}
			clearWd = lane
		}
		clearCfg := sessionCfg
		// A resumed daemon has no Manager launch flag; the session owns it.
		clearCfg.Manager = oldMeta.Manager
		// The same holds for provenance: a resumed session owns the persisted
		// mode, and clear replaces the session without re-reading launch config.
		clearCfg.CommitProvenance = oldMeta.Config.CommitProvenance
		// The launch picked the first session's ID into sessionCfg; the cleared
		// session is a new session and picks its own.
		clearCfg.SessionID = ""
		clearCfg.SessionStartKind = plugin.SessionStartKindClear
		if lane != "" {
			clearCfg.AdoptWorktreeLane = &agent.WorktreeLane{
				Path:        lane,
				Managed:     oldLane.Managed,
				RestoreRoot: oldLane.RestoreRoot,
			}
		}
		// The cleared session inherits the CURRENT session's ACTUAL sandbox (on resume
		// the persisted mode, not the launch flag), so its persisted config matches what
		// it runs under. Reconcile from the live env before building the new session.
		reconcileClearSandbox(&clearCfg, oldEnv)
		// Build and provision a FRESH env for the cleared session BEFORE any destructive
		// change. If provisioning fails, thread/clear aborts with oldSess still current rather
		// than leaving a live session running unconfined while persisting a sandbox mode
		// (a fail-open). Each session owns its own env + session tmp, disposed on Close,
		// so oldSess.Close() no longer pulls the tmp out from under the new session.
		clearEnv := execenv.NewLocalExecutionEnvironment(clearWd)
		clearEnv.LoginPATH = execenv.LoginShellPATH() // cached: free after the first probe this process
		if err := deps.provisionSandbox(clearEnv, &clearCfg, clearWd); err != nil {
			return fmt.Errorf("clear sandbox: %w", err)
		}
		newSess, err := deps.newClearSession(client, profile, clearEnv, clearCfg)
		if err != nil {
			// Cleanup stops whatever the failed construction left running; the
			// scratch it provisioned then goes, as on the fresh-session path.
			clearEnv.Cleanup()
			_ = clearEnv.DisposeSessionScratch()
			return fmt.Errorf("new session: %w", err)
		}
		// Everything that can fail happens before anything shared moves, so the
		// swap itself is infallible and needs no rollback. A rollback is not
		// merely inelegant here: undoing a published identity means emitting a
		// second thread/closed, and every subscriber has already been told the
		// thread it is watching ended.
		// The replacement serves the same workspace ref, so it serves above
		// the generation its clients hold, or they would ignore it.
		clearGeneration, err := schema.NextBootGeneration(sd, newSess.ID(), servedBootGeneration.Load())
		if err != nil {
			newSess.Close() // disposes clearEnv
			return fmt.Errorf("boot generation: %w", err)
		}
		newSess.SetBootGeneration(appwire.BootGeneration(clearGeneration))
		prepared, err := deps.prepareAppIdentity("local", newSess.ID(), workspaceRef, newSess.TranscriptPath())
		if err != nil {
			newSess.Close() // disposes clearEnv
			return fmt.Errorf("prepare app identity: %w", err)
		}
		// Hand the managed lane's occupancy marker to the replacement while the
		// predecessor is still live, so the lane is never unlocked between
		// owners. A failed rendezvous below reverses it.
		var undoWorktreeLane func()
		if clearCfg.AdoptWorktreeLane != nil && clearCfg.AdoptWorktreeLane.Managed {
			undo, err := oldSess.TransferWorktreeLaneTo(newSess.ID())
			if err != nil {
				newSess.Close() // disposes clearEnv
				return fmt.Errorf("transfer worktree lane: %w", err)
			}
			undoWorktreeLane = undo
		}
		// The rendezvous is the last fallible step and the daemon's public
		// address for this thread. Moving it before the replacement means a
		// client that discovers the new session id can always reach a daemon
		// already serving it; a failure here still names the old session, which
		// is still the live one.
		live.transitionMu.Lock()
		shutdownClaimed := live.shutdownClosed()
		if shutdownClaimed {
			closeSupersededSession(oldSess, true) // disposes oldEnv
			if !bridge.waitForSessionBridgeDrain(oldSess.ID()) {
				live.transitionMu.Unlock()
				newSess.Close() // disposes clearEnv; old identity remains current
				return errors.New("old session bridge did not drain before clear deadline")
			}
		}
		if err := deps.updateIdentity(rvRegistration, newSess.ID(), clearWd); err != nil {
			if undoWorktreeLane != nil {
				undoWorktreeLane()
			}
			live.transitionMu.Unlock()
			newSess.Close() // disposes clearEnv
			return fmt.Errorf("rendezvous update: %w", err)
		}
		// One projection commit swaps the live session, the daemon's identity,
		// and the thread's history. The stable workspace ref remains subscribed
		// while a resync tells every client to hydrate the new instance.
		srv.WireTranscriptHistory(newSess)
		srv.ReplaceAppIdentity(prepared.WithRecordedLength(newSess.TranscriptRecordedLength()).WithBootGeneration(appwire.BootGeneration(clearGeneration)), func() { live.set(newSess, clearEnv) })
		srv.SetWorkingDir(clearWd)
		servedBootGeneration.Store(clearGeneration)
		// Re-root the retirement controller at the replacement. The lease held
		// since the top of this func keeps the controller resident, so this
		// cannot fail; the idle interval restarts against the new root.
		if err := retiring.retirement.AttachRoot(newSess); err != nil {
			serveLogf(logOut, newSess.ID(), "retirement root re-attach failed: %v", err)
		}
		retiring.observe("root_published", newSess.ID())
		// The commit above zeroed the envelope with the identity it described.
		// Re-seed from the replacement session here rather than inside the
		// commit: sampling every facet reads jobs.jsonl and the task store, and
		// the commit holds the projection gate. Nothing is lost by seeding
		// after it -- the new session's own events are still queued in its
		// channel, and its bridge has not started.
		srv.RefreshThreadEnvelope()
		if !shutdownClaimed {
			closeSupersededSession(oldSess, false) // disposes oldEnv after the swap
		}
		live.transitionMu.Unlock()
		// Every session this daemon makes current gets closed by someone, and
		// shutdown covers only the one that was live when its pass ran. A
		// replacement installed after that pass has no other closer, so thread/clear
		// closes its own. The check reads AFTER the swap above, so a negative
		// answer means the pass is still to come and will find newSess itself.
		//
		// Leaving it open costs two things and neither is recoverable. Its event
		// channel is never closed, so the drain bridgeSession registers below
		// can never end -- the teardown then spends its whole budget waiting on
		// it and abandons the verbose tee. And its env's Cleanup() never runs,
		// so the scratch directory the session owns survives process exit.
		// Closing here happens BEFORE the bridge on purpose:
		// ConsumeEventsLossless reports an already-closed session drained
		// immediately, so nothing is registered that has to be waited out.
		if live.shutdownClosed() {
			newSess.Close() // disposes clearEnv
		}
		bridge.bridgeSession(newSess)
		return nil
	})

	srv.SetWorkingDir(wd)
	srv.SetShutdownFunc(func() {
		cancel()
	})

	return envelopeSource
}
