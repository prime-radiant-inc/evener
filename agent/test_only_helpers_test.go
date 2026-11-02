// Helpers that only tests call. They are state-machine and projection seams the
// behavior tests drive directly; production reaches the same state through
// other entry points, so they live here instead of in the shipped package.

package agent

import (
	"context"
	"errors"
	"strings"

	"primeradiant.com/evener/agent/internal/delegatestore"
	"primeradiant.com/evener/agent/internal/jobstore"
	"primeradiant.com/evener/agent/sandbox"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
)

// recordWatchSendPending retains the pure runtime-state transition used by the
// state-machine harnesses. Production persistence uses planWatchSendPending,
// fsyncs its event, and calls commitWatchSendPendingRecord only afterward.
func (jm *jobManager) recordWatchSendPending(state jobstore.WatchSendState, d watchSendDelivery) watchSendPendingRecord {
	record := jm.planWatchSendPending(state, d)
	jm.commitWatchSendPendingRecord(record, d.allowAfterTerminalExpiry)
	return record
}

func (m *subagentManager) beginReconstructionSideEffects(childID string, sub *subagent) (func(), error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closing {
		if m.subs[childID] == sub {
			delete(m.subs, childID)
		}
		return nil, errSubagentManagerClosing
	}
	m.activeRestoreSideEffects++
	return func() {
		m.mu.Lock()
		m.activeRestoreSideEffects--
		if m.activeRestoreSideEffects == 0 {
			m.restoreSideEffectsCond.Broadcast()
		}
		m.mu.Unlock()
	}, nil
}

func (jm *jobManager) noteJobActivity(jobID, phase string) {
	if jm == nil || strings.TrimSpace(jobID) == "" {
		return
	}
	now := jm.now()
	jm.mu.Lock()
	defer jm.mu.Unlock()
	run := jm.running[jobID]
	if run == nil || run.rec == nil || run.rec.Status.IsTerminal() {
		return
	}
	run.rec.LastActivity = &now
	if phase != "" {
		run.rec.Phase = phase
	}
}

func (jm *jobManager) abandonRunningJob(jobID string) {
	jm.mu.Lock()
	run := jm.running[jobID]
	var targets []watchConfigTerminalSnapshot
	if run != nil {
		delete(jm.running, jobID)
		targets = jm.pruneWatchedTargetWatchesLocked(jobID, "watched target pruned", jm.now())
	}
	jm.mu.Unlock()
	if run == nil {
		return
	}
	dropped := terminalSnapshots(targets)
	applied, err := jm.appendAbandonSnapshots(dropped)
	if err != nil {
		jm.removeWatchSendTerminalSnapshots(applied)
		jm.rollbackWatchConfigSnapshotsRejecting(targets)
		jm.enqueueWatchNotifications([]jobNotification{
			watchNotification(jobID, "watch send prune cleanup failed: "+limitWatchText(err.Error(), watchReadErrorMaxChars)),
		})
	} else {
		jm.detachWatchConfigSnapshots(targets)
		jm.removeWatchSendTerminalSnapshots(applied)
	}
	if run.output != nil {
		_ = run.output.Close()
	}
	run.delegateShell.finish()
	run.closeDoneAbandoned()
}

func (jm *jobManager) readOutputHead(jobID string, headBytes int) (content string, total int64, truncated bool, err error) {
	jm.mu.Lock()
	run := jm.running[jobID]
	jm.mu.Unlock()
	if run != nil {
		return headOutput(run.output, headBytes)
	}

	recs, err := jm.store.Load()
	if err != nil {
		return "", 0, false, err
	}
	rec := recs[jobID]
	if rec == nil {
		return "", 0, false, errJobNotFound(jobID)
	}
	return readClosedJobOutput(jm.outputPathForJob(rec, jobID), rec, headBytes, true)
}

// outputDropped returns the number of bytes before the first visible byte of
// jobID's output, which the retention cap keeps from readers (0 when nothing has
// been pruned), for both the live store and the closed-file fallback.
func (jm *jobManager) outputDropped(jobID string) (int64, error) {
	jm.mu.Lock()
	run := jm.running[jobID]
	jm.mu.Unlock()
	if run != nil {
		return run.output.RetainedStart(), nil
	}
	recs, err := jm.store.Load()
	if err != nil {
		return 0, err
	}
	rec := recs[jobID]
	if rec == nil {
		return 0, errJobNotFound(jobID)
	}
	snapshot, err := readLocalJobOutputSnapshot(jm.outputPathForJob(rec, jobID), 0, true)
	if err != nil {
		return 0, err
	}
	if err := checkOutputTotalForRecord(rec, snapshot.TotalBytes); err != nil {
		return 0, err
	}
	return snapshot.RetainedStart, nil
}

func (jm *jobManager) finalizeWithRunNoNotification(jobID string, prepare func(*runningJob) (jobstore.Status, string, *int, error)) error {
	return jm.finalizeWithRunMode(jobID, prepare, false)
}

func headOutput(output *jobstore.OutputStore, headBytes int) (string, int64, bool, error) {
	b, total, truncated, err := output.Head(headBytes)
	return stringOutputResult(b, total, truncated, err)
}

// cloneSandboxSnapshot deep-copies a persisted snapshot so a resumed-turn
// descriptor does not alias the previous descriptor's slices (mirroring the other
// resume-path clones in resumedDelegateRestoreDescriptor). nil stays nil.
func cloneSandboxSnapshot(in *delegatestore.SandboxSnapshot) *delegatestore.SandboxSnapshot {
	if in == nil {
		return nil
	}
	out := &delegatestore.SandboxSnapshot{
		Mode:               in.Mode,
		WriteBlocked:       in.WriteBlocked,
		DenylistAdd:        append([]string(nil), in.DenylistAdd...),
		DenylistRemove:     append([]string(nil), in.DenylistRemove...),
		ExtraWritableRoots: append([]string(nil), in.ExtraWritableRoots...),
		ExtraReadRoots:     append([]string(nil), in.ExtraReadRoots...),
	}
	if in.Network != nil {
		n := *in.Network
		out.Network = &n
	}
	return out
}

// sandboxPolicyFromSnapshot rebuilds the backend-independent policy REQUEST from a
// persisted snapshot for re-resolution on restore. ok is false when the mode name
// is unparseable (a corrupt/hand-edited descriptor), which the restore path treats
// as not-resumable rather than resuming with a guessed policy.
func sandboxPolicyFromSnapshot(snap *delegatestore.SandboxSnapshot) (sandbox.SandboxPolicy, bool) {
	if snap == nil {
		return sandbox.SandboxPolicy{}, false
	}
	mode, err := sandbox.ParseMode(snap.Mode)
	if err != nil {
		return sandbox.SandboxPolicy{}, false
	}
	pol := sandbox.SandboxPolicy{
		Mode:               mode,
		WriteBlocked:       snap.WriteBlocked,
		DenylistAdd:        append([]string(nil), snap.DenylistAdd...),
		DenylistRemove:     append([]string(nil), snap.DenylistRemove...),
		ExtraWritableRoots: append([]string(nil), snap.ExtraWritableRoots...),
		ExtraReadRoots:     append([]string(nil), snap.ExtraReadRoots...),
	}
	if snap.Network != nil {
		n := *snap.Network
		pol.Network = &n
	}
	return pol, true
}

func (s *Session) clearDescendantReceiverWatchByID(watchID string) (watchResult, bool, error) {
	return s.clearStableReceiverWatchByID(watchID)
}

func watchSendArg(args map[string]any) (*watchSendArgs, error) {
	raw, ok := args["send"]
	if !ok {
		return nil, nil
	}
	values, ok := raw.(map[string]any)
	if !ok {
		return nil, errors.New("send must be an object")
	}
	if isEmptyWatchSend(values) {
		return nil, nil
	}
	to := strings.TrimSpace(stringArg(values, "to"))
	if to == "" {
		return nil, errors.New("invalid_request: send.to is required")
	}
	return &watchSendArgs{
		To:             to,
		Message:        stringArg(values, "message"),
		IncludeExcerpt: shellBoolArg(values, "include_excerpt"),
	}, nil
}

func isEmptyWatchSend(values map[string]any) bool {
	return strings.TrimSpace(stringArg(values, "to")) == "" &&
		stringArg(values, "message") == "" &&
		!shellBoolArg(values, "include_excerpt")
}

func projectJobRecord(s *Session, rec *jobstore.JobRecord) jobListEntry {
	return projectJobRecordForViewer(s, s, rec)
}

// liveWorkHandles returns the launch-time working directory of every shell job
// still in jm.running. Stable delegate lanes are enumerated by the controller.
func (jm *jobManager) liveWorkHandles() []liveWorkHandle {
	jm.mu.Lock()
	defer jm.mu.Unlock()
	var out []liveWorkHandle
	for _, run := range jm.running {
		if run == nil || run.rec == nil {
			continue
		}
		if run.rec.Type == jobstore.JobShell && run.rec.WorkingDir != "" {
			out = append(out, liveWorkHandle{dir: run.rec.WorkingDir, handle: run.rec.JobID + " (shell, running)"})
		}
	}
	return out
}

func (jm *jobManager) watchListToolResultForReceiver(receiverSessionID, receiverDelegateID string) jobWatchListToolResult {
	receiverSessionID = strings.TrimSpace(receiverSessionID)
	receiverDelegateID = strings.TrimSpace(receiverDelegateID)
	if receiverSessionID == "" {
		return jobWatchListToolResult{}
	}
	pending, history := jm.watchInspectSnapshots(
		func(cfg *watchConfig) bool {
			return watchConfigMatchesReceiver(cfg, receiverSessionID, receiverDelegateID)
		},
		func(h watchHistoryEntry) bool {
			return watchHistoryMatchesReceiver(h, receiverSessionID, receiverDelegateID)
		},
	)
	return formatWatchListInspectResult(pending, history)
}

// aggregateWatchStatuses projects the watches every supplied manager holds that
// are visible to receiverSessionID. Managers are deduped so two live sessions
// sharing one manager do not double every row, and the result is ordered like
// the single-manager projection. Superseded as a production entry point by the
// page walk's per-row aggregation (appendWatchStatusesByRow, inside
// LiveWatchRowsForSessions); it is reached only from tests, which keep the
// receiver-rollup contract pinned through it.
func aggregateWatchStatuses(receiverSessionID string, managers []*jobManager) []WatchStatusInfo {
	var statuses []WatchStatusInfo
	seen := make(map[*jobManager]struct{}, len(managers))
	for _, jm := range managers {
		if jm == nil {
			continue
		}
		if _, ok := seen[jm]; ok {
			continue
		}
		seen[jm] = struct{}{}
		statuses = append(statuses, jm.liveWatchStatusesForSession(receiverSessionID)...)
	}
	sortWatchStatuses(statuses)
	return statuses
}

// LiveWatchesForDescendant returns the live watch rows visible to the descendant
// session sessionID, so its own thread row can carry them. Today a session's
// watches are projected only onto its own row by the daemon's status sampler;
// a subagent session's watches reached no row at all, because the root's status
// is the only one the daemon projects. This resolves the descendant through the
// manager that owns children and delegates to that child's own liveWatchStatuses,
// so isolation matches the root's: a receiver watch the child holds for an
// ancestor is filtered out here (it belongs on the ancestor's row), and a
// keyless watch the child owns never surfaces on an ancestor's projection. The
// root is not its own descendant, so a root (or unknown) session ID returns nil.
// A KNOWN descendant with no watches is a third case: it returns a NON-nil empty
// slice, because the list path must be able to tell "this descendant has no
// watches now" from "this ID is not a descendant I can see". nil therefore means
// only the root, an empty ID, or an unknown ID.
//
// This is sampled on read, not cached: watch state changes without a per-child
// app event on this daemon, so the caller re-derives it on every list/read. The
// walk itself is LiveWatchRowsForSessions', so a single-ID caller and a page
// caller can never disagree about which sessions are answerable. The page walk is
// the production entry point; this single-ID form and LiveWatchesForSession below
// are reached only from tests, which pin the page's contracts through these
// narrower seams.
func (s *Session) LiveWatchesForDescendant(sessionID string) []WatchStatusInfo {
	if s == nil || sessionID == "" || sessionID == s.ID() {
		return nil
	}
	return s.LiveWatchRowsForSessions([]string{sessionID})[sessionID]
}

// LiveWatchesForSession resolves a row's thread ID to the live watch rows that
// belong on it, so a thread LIST read can refresh watches that changed with no
// per-thread app event. It is the broader sibling of LiveWatchesForDescendant:
// that one keeps its narrower contract (the root is not its own descendant, so
// it returns nil for the root), while this one also answers for the root's own
// row, carrying every manager that holds a watch for that session -- including a
// receiver watch a descendant session holds for it.
//
// This is NOT the set Session.DetailedStatus().Watches carries, and must not be
// replaced by it: that facet is the session's own manager only, by the
// envelope-sampling contract (a sampled method may take this session's mu and
// nothing else, so it cannot reach a descendant's manager). The receiver rollup
// therefore reaches a row from the LIST path (cmd/evener/serve.go), which samples
// here. Dropping that sampling makes "watch my child's long-running job"
// invisible on both sessions' rows.
//
// An empty answer for the root is non-nil: the caller merges only a nil answer
// as "no fresh sample, leave the cached projection alone", so a non-nil empty
// slice is how a watch cleared since the last diagnostics refresh leaves the
// row. A known descendant with no watches is likewise non-nil empty, exactly as
// the root's is, so a descendant cleared since the last refresh leaves its row
// too. An unknown or empty ID stays nil (LiveWatchesForDescendant's own
// behavior).
func (s *Session) LiveWatchesForSession(sessionID string) []WatchStatusInfo {
	if s == nil || sessionID == "" {
		return nil
	}
	return s.LiveWatchRowsForSessions([]string{sessionID})[sessionID]
}

// liveWatchStatuses aggregates every live manager that can hold a config whose
// receiver is THIS session: its own manager plus the stable watch-source
// sessions (stableWatchSourceSessions), the same set the #655 stop inventory
// scans. A receiver watch on a descendant's job lives in that descendant's job
// manager with this session recorded as the receiver, so reading only
// s.jobManager made it invisible to both sessions - the exact "watch my child's
// long-running job" case this feature exists for.
func (s *Session) liveWatchStatuses() []WatchStatusInfo {
	if s == nil {
		return nil
	}
	// Answered from the same page walk the list path uses, so the two cannot
	// disagree about which rows are this session's. A known session with no
	// watches keeps the non-nil empty answer its siblings document
	// (TestLiveWatchesForSessionEmptyRootIsNotEmptyAnswer pins it for the public
	// seams): nil means "this session cannot be answered for", never "it has no
	// watches now".
	rows, ok := s.LiveWatchRowsForSessions([]string{s.ID()})[s.ID()]
	if !ok {
		return nil
	}
	return rows
}
func (s *Session) launchCompactionNamer(ctx context.Context, turn schema.Turn) {
	s.launchCompactionNamerGated(ctx, turn, ungatedFoldRevision)
}

func (s *Session) nameSessionFromCompactionTurn(ctx context.Context, turn schema.Turn) error {
	return s.nameSessionFromCompactionTurnGated(ctx, turn, ungatedFoldRevision)
}

func appendColdAttentionResolution(path, expectedSessionID string, ids []string, disposition delegateAttentionResolution) (err error) {
	return appendColdAttentionResolutionWithOpen(path, expectedSessionID, ids, disposition, transcript.OpenWriterForSession)
}

func (s *Session) readMemoryIndex(scope string) memoryProjection {
	p, _ := s.readMemoryScope(context.Background(), scope, nil)
	return p
}
