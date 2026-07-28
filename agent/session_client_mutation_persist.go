package agent

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"

	"github.com/spf13/afero"

	"primeradiant.com/serf/appwire"
)

const (
	clientMutationPersistSubdir   = "mutations"
	clientMutationSnapshotVersion = 1
)

type clientMutationWriteKind uint8

const (
	clientMutationWriteReservation clientMutationWriteKind = iota
	clientMutationWriteEffect
)

func clientMutationFilePath(stateDir, sessionID string) string {
	return filepath.Join(stateDir, clientMutationPersistSubdir, sessionID+".json")
}

func loadClientMutationSnapshotFS(fs afero.Fs, stateDir, sessionID string) (clientMutationSnapshot, error) {
	empty := newEmptyClientMutationSnapshot(sessionID)
	if stateDir == "" {
		return empty, nil
	}
	data, err := afero.ReadFile(fs, clientMutationFilePath(stateDir, sessionID))
	if os.IsNotExist(err) {
		return empty, nil
	}
	if err != nil {
		return clientMutationSnapshot{}, fmt.Errorf("read client mutation snapshot: %w", err)
	}

	var snapshot clientMutationSnapshot
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&snapshot); err != nil {
		return clientMutationSnapshot{}, fmt.Errorf("decode client mutation snapshot: %w", err)
	}
	if err := rejectTrailingClientMutationJSON(decoder); err != nil {
		return clientMutationSnapshot{}, err
	}
	if err := validateClientMutationSnapshot(snapshot, sessionID); err != nil {
		return clientMutationSnapshot{}, fmt.Errorf("validate client mutation snapshot: %w", err)
	}
	return snapshot, nil
}

func saveClientMutationSnapshotFS(
	fs afero.Fs,
	stateDir string,
	sessionID string,
	snapshot clientMutationSnapshot,
	kind clientMutationWriteKind,
	faults clientMutationFaults,
) (renamed bool, err error) {
	if stateDir == "" {
		return true, nil
	}
	if err := validateClientMutationSnapshot(snapshot, sessionID); err != nil {
		return false, fmt.Errorf("validate client mutation snapshot: %w", err)
	}
	data, err := json.Marshal(snapshot)
	if err != nil {
		return false, fmt.Errorf("marshal client mutation snapshot: %w", err)
	}

	path := clientMutationFilePath(stateDir, sessionID)
	dir := filepath.Dir(path)
	if err := fs.MkdirAll(dir, 0o755); err != nil {
		return false, fmt.Errorf("create client mutations dir: %w", err)
	}
	temp, err := afero.TempFile(fs, dir, filepath.Base(path)+".tmp-*")
	if err != nil {
		return false, fmt.Errorf("create temp client mutation snapshot: %w", err)
	}
	tempPath := temp.Name()
	defer func() {
		if temp != nil {
			_ = temp.Close()
		}
		if !renamed {
			_ = fs.Remove(tempPath)
		}
	}()

	if _, err := temp.Write(data); err != nil {
		return false, fmt.Errorf("write temp client mutation snapshot: %w", err)
	}
	if err := temp.Sync(); err != nil && !clientMutationSyncUnsupported(err) {
		return false, fmt.Errorf("sync temp client mutation snapshot: %w", err)
	}
	if err := temp.Close(); err != nil {
		return false, fmt.Errorf("close temp client mutation snapshot: %w", err)
	}
	temp = nil

	if kind == clientMutationWriteEffect && faults.BeforeEffectSnapshotRename != nil {
		if err := faults.BeforeEffectSnapshotRename(); err != nil {
			return false, err
		}
	}
	if err := fs.Rename(tempPath, path); err != nil {
		return false, fmt.Errorf("rename client mutation snapshot: %w", err)
	}
	renamed = true
	if kind == clientMutationWriteEffect && faults.AfterEffectSnapshotRename != nil {
		if err := faults.AfterEffectSnapshotRename(); err != nil {
			return true, err
		}
	}
	return true, nil
}

func newEmptyClientMutationSnapshot(sessionID string) clientMutationSnapshot {
	return clientMutationSnapshot{
		Version:            clientMutationSnapshotVersion,
		SessionID:          sessionID,
		Journal:            make(map[string]clientMutationRecord),
		BudgetReservations: make(map[string]clientMutationBudgetReservation),
		PendingExecutions:  make(map[string]appwire.PendingMutation),
	}
}

func validateClientMutationSnapshot(snapshot clientMutationSnapshot, sessionID string) error {
	if snapshot.Version != clientMutationSnapshotVersion {
		return fmt.Errorf("unsupported version %d", snapshot.Version)
	}
	if snapshot.SessionID != sessionID {
		return fmt.Errorf("session ID %q does not match %q", snapshot.SessionID, sessionID)
	}
	if snapshot.Journal == nil {
		return errors.New("journal is missing")
	}
	if snapshot.BudgetReservations == nil {
		return errors.New("budget reservations are missing")
	}
	if snapshot.PendingExecutions == nil {
		return errors.New("pending executions are missing")
	}
	for id, record := range snapshot.Journal {
		if id == "" || record.ClientMutationID != id {
			return fmt.Errorf("journal key %q does not match record ID %q", id, record.ClientMutationID)
		}
		if record.Method == "" {
			return fmt.Errorf("client mutation %q has no method", id)
		}
		if len(record.Payload) == 0 || !json.Valid(record.Payload) {
			return fmt.Errorf("client mutation %q has an invalid payload", id)
		}
		sum := sha256.Sum256(record.Payload)
		if record.PayloadHash != hex.EncodeToString(sum[:]) {
			return fmt.Errorf("client mutation %q payload hash does not match payload", id)
		}
		if record.AttemptGeneration == 0 {
			return fmt.Errorf("client mutation %q has no attempt generation", id)
		}
		if record.ExecutionState == "" {
			return fmt.Errorf("client mutation %q has no execution state", id)
		}
		switch record.OperationState {
		case clientMutationOperationInFlight, clientMutationOperationApplied, clientMutationOperationTerminal:
			if record.Rejection != nil {
				return fmt.Errorf("client mutation %q has rejection outside rejected state", id)
			}
		case clientMutationOperationRejected:
			if record.Rejection == nil {
				return fmt.Errorf("rejected client mutation %q has no rejection", id)
			}
		default:
			return fmt.Errorf("client mutation %q has invalid operation state %q", id, record.OperationState)
		}
		switch record.ProjectionState {
		case appwire.MutationProjectionPending, appwire.MutationProjectionReflected, appwire.MutationProjectionRemoved:
		default:
			return fmt.Errorf("client mutation %q has invalid projection state %q", id, record.ProjectionState)
		}
	}
	for id, pending := range snapshot.PendingExecutions {
		if id == "" || pending.ClientMutationID != id {
			return fmt.Errorf("pending execution key %q does not match mutation ID %q", id, pending.ClientMutationID)
		}
	}
	return nil
}

func rejectTrailingClientMutationJSON(decoder *json.Decoder) error {
	var trailing any
	err := decoder.Decode(&trailing)
	if errors.Is(err, io.EOF) {
		return nil
	}
	if err == nil {
		return errors.New("decode client mutation snapshot: trailing JSON value")
	}
	return fmt.Errorf("decode client mutation snapshot trailing data: %w", err)
}

func clientMutationSyncUnsupported(err error) bool {
	return errors.Is(err, syscall.ENOSYS) ||
		errors.Is(err, syscall.ENOTSUP) ||
		errors.Is(err, syscall.EINVAL)
}
