package agent

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/afero"

	"primeradiant.com/serf/agent/events"
)

// queuePersistSubdir is the state-dir subdirectory holding one queue-snapshot
// file per session, mirroring agent/task.NewTaskStore's <stateDir>/tasks/<id>.json
// convention (a dedicated per-concern file, not a SessionMeta field): queue
// mutations happen far more often than the coarse-grained meta.json autosave,
// so folding them into SessionMeta would rewrite the whole ~30-field snapshot
// on every Enqueue/Steer/Promote/Cancel call. A small, separately-written file
// keeps each write proportional to the queue itself.
const queuePersistSubdir = "queues"

// persistedQueues is the on-disk representation of a session's pending
// steering and input queues. Steering and Input are kept as distinct fields
// (rather than merged into one list) so the steer-vs-input classification of
// each entry survives a restart alongside its order and content.
type persistedQueues struct {
	Steering []steeringMessage `json:"steering,omitempty"`
	Input    []queuedInput     `json:"input,omitempty"`
}

// queuesFilePath returns the path a session's queue snapshot lives at.
func queuesFilePath(stateDir, id string) string {
	return filepath.Join(stateDir, queuePersistSubdir, id+".json")
}

// saveQueues writes the given queues to <stateDir>/queues/<id>.json
// atomically (write-temp, rename), matching TaskStore.save's pattern. An
// empty snapshot (both slices empty) removes the file instead of writing an
// empty-array residue, so "no queued messages" is absent, not noise. A blank
// stateDir (state persistence disabled, e.g. ephemeral `serf run`) is a no-op,
// matching maybeAutoSave's own convention.
func saveQueues(stateDir, id string, steering []steeringMessage, input []queuedInput) error {
	return saveQueuesFS(afero.NewOsFs(), stateDir, id, steering, input)
}

// saveQueuesFS is the filesystem seam beneath saveQueues (mirrors
// schema.saveSessionMetaFS / TaskStore's injected afero.Fs): production calls
// saveQueues, which passes afero.NewOsFs() (byte-identical to direct os
// calls); tests inject an in-memory or sandboxed filesystem.
func saveQueuesFS(fs afero.Fs, stateDir, id string, steering []steeringMessage, input []queuedInput) error {
	if stateDir == "" {
		return nil
	}
	path := queuesFilePath(stateDir, id)
	if len(steering) == 0 && len(input) == 0 {
		if err := fs.Remove(path); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove empty queue snapshot: %w", err)
		}
		return nil
	}

	dir := filepath.Dir(path)
	if err := fs.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create queues dir: %w", err)
	}
	data, err := json.Marshal(persistedQueues{Steering: steering, Input: input})
	if err != nil {
		return fmt.Errorf("marshal queues: %w", err)
	}
	tmp := path + ".tmp"
	if err := afero.WriteFile(fs, tmp, data, 0o644); err != nil {
		return fmt.Errorf("write temp queues file: %w", err)
	}
	if err := fs.Rename(tmp, path); err != nil {
		_ = fs.Remove(tmp)
		return fmt.Errorf("rename queues file: %w", err)
	}
	return nil
}

// loadQueues reads a session's persisted queue snapshot. A missing file
// (never persisted, or drained to empty before the last exit) is not an
// error: it returns two nil slices, exactly the zero-value queues a fresh
// session starts with.
func loadQueues(stateDir, id string) ([]steeringMessage, []queuedInput, error) {
	return loadQueuesFS(afero.NewOsFs(), stateDir, id)
}

// loadQueuesFS is the filesystem seam beneath loadQueues; see saveQueuesFS.
func loadQueuesFS(fs afero.Fs, stateDir, id string) ([]steeringMessage, []queuedInput, error) {
	if stateDir == "" {
		return nil, nil, nil
	}
	data, err := afero.ReadFile(fs, queuesFilePath(stateDir, id))
	if os.IsNotExist(err) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, fmt.Errorf("read queues: %w", err)
	}
	var snap persistedQueues
	if err := json.Unmarshal(data, &snap); err != nil {
		return nil, nil, fmt.Errorf("unmarshal queues: %w", err)
	}
	return snap.Steering, snap.Input, nil
}

// persistQueuesSnapshot writes the session's current steering and input
// queues to disk. Called after every queue mutation (enqueue, drain,
// promote, cancel, pop/push), always with s.mu already released — matching
// maybeAutoSave's discipline of never holding a Session lock across file I/O.
// A write failure is reported as a warning, not a returned error: the
// in-memory queue mutation the caller already committed must not be undone
// or blocked by a disk problem (same non-fatal posture maybeAutoSave takes
// for meta.json).
//
// queuePersistMu wraps the ENTIRE snapshot-then-write sequence (not just the
// disk write): two concurrent callers must take their s.mu-guarded snapshot
// while serialized against each other, so whichever call's write actually
// lands last on disk is guaranteed to have read the more current state.
// Reading the snapshot before acquiring queuePersistMu would reopen the race
// (see the field doc on session.go) by letting an earlier, staler read's
// write land after a later, fresher one's.
func (s *Session) persistQueuesSnapshot() {
	s.queuePersistMu.Lock()
	defer s.queuePersistMu.Unlock()
	s.mu.Lock()
	steering := userSourcedSteering(s.steeringQueue)
	input := append([]queuedInput{}, s.inputQueue...)
	stateDir := s.stateDir
	id := s.id
	s.mu.Unlock()
	if err := saveQueues(stateDir, id, steering, input); err != nil {
		s.emit(events.EventWarning, events.WarningData{Message: fmt.Sprintf("queue persist failed: %v", err)})
	}
}

// userSourcedSteering returns the entries of queue sent by a human (Source ==
// events.SteeringSourceUser) — the only steeringQueue entries in the scope of
// restart parity (turn/steer, DrainAsSteer, PromoteQueuedAsSteer). Daemon/hook
// -authored steering (Source == "": SessionStart hook context, compaction and
// task-reminder nudges, vision descriptions, subagent task reminders — every
// plain Steer() call site) is deliberately excluded from persistence: it is
// inherently tied to the CURRENT process's evaluation of CURRENT state
// (current task, current hook matchers, the tool round just executed),
// re-derived fresh rather than replayed. Blindly persisting it is actively
// wrong, not merely unnecessary — session_init.go's own dedicated resume path
// (drainPendingSessionStartHooksForUserTurn) replays SessionStart hook output
// on resume by re-matching hooks against the "resume" event; a hook whose
// matcher is startup-only must NOT fire again on resume
// (TestResume_DualFlavorPlugin_DoesNotReinject). Resurrecting a stale,
// already-queued-but-undrained "startup" hook message from the old process
// via the queue-persistence path would silently reintroduce exactly the
// re-injection that mechanism exists to prevent.
func userSourcedSteering(queue []steeringMessage) []steeringMessage {
	var out []steeringMessage
	for _, m := range queue {
		if m.Source == events.SteeringSourceUser {
			out = append(out, m)
		}
	}
	return out
}
