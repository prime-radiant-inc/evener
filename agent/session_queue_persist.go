package agent

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/execsupport/fsatomic"
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
// daemon-authored steering queue. Client input and steering live exclusively in
// the durable client-mutation snapshot.
type persistedQueues struct {
	Steering []steeringMessage `json:"steering,omitempty"`
}

// queuesFilePath returns the path a session's queue snapshot lives at.
func queuesFilePath(stateDir, id string) string {
	return filepath.Join(stateDir, queuePersistSubdir, id+".json")
}

// saveQueues writes the given daemon steering queue to
// <stateDir>/queues/<id>.json durably and atomically (fsatomic.Write), matching
// TaskStore.save's pattern. An empty slice removes the file instead of writing
// an empty-array residue, so "no queued steering" is absent, not noise. A blank
// stateDir (state persistence disabled, e.g. ephemeral `evener run`) is a no-op,
// matching maybeAutoSave's own convention.
func saveQueues(stateDir, id string, steering []steeringMessage) error {
	if stateDir == "" {
		return nil
	}
	path := queuesFilePath(stateDir, id)
	if len(steering) == 0 {
		if err := fsatomic.Remove(path); err != nil {
			return fmt.Errorf("remove empty queue snapshot: %w", err)
		}
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create queues dir: %w", err)
	}
	data, err := json.Marshal(persistedQueues{Steering: steering})
	if err != nil {
		return fmt.Errorf("marshal queues: %w", err)
	}
	if err := fsatomic.Write(path, data, 0o600); err != nil {
		return fmt.Errorf("write queues file: %w", err)
	}
	return nil
}

// loadQueues reads a session's persisted daemon steering snapshot. A missing
// file (never persisted, or drained to empty before the last exit) is not an
// error: it returns a nil slice, the zero-value steering queue a fresh session
// starts with.
func loadQueues(stateDir, id string) ([]steeringMessage, error) {
	if stateDir == "" {
		return nil, nil
	}
	data, err := os.ReadFile(queuesFilePath(stateDir, id))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read queues: %w", err)
	}
	var snap persistedQueues
	if err := json.Unmarshal(data, &snap); err != nil {
		return nil, fmt.Errorf("unmarshal queues: %w", err)
	}
	return snap.Steering, nil
}

// persistQueuesSnapshot writes only daemon-authored steering to the legacy
// queue file. Client queue and steering authority lives exclusively in the
// mutation snapshot. Called with s.mu already released — matching
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
	steering := daemonSourcedSteering(s.steeringQueue)
	stateDir := s.stateDir
	id := s.id
	s.mu.Unlock()
	if err := saveQueues(stateDir, id, steering); err != nil {
		s.emit(events.WarningData{Message: fmt.Sprintf("queue persist failed: %v", err)})
	}
}

func daemonSourcedSteering(queue []steeringMessage) []steeringMessage {
	var out []steeringMessage
	for _, m := range queue {
		// SessionStart hook context has a dedicated matcher-aware resume path.
		// Persisting that daemon entry here would inject it a second time.
		if m.Source != events.SteeringSourceUser && m.Kind != events.SteeringKindHookContext {
			out = append(out, m)
		}
	}
	return out
}
