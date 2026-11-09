package server

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/execsupport/fsatomic"
)

const threadClearJournalName = "thread-clear-journal.json"

type threadClearRecordState string

const (
	threadClearReserved threadClearRecordState = "reserved"
	threadClearApplied  threadClearRecordState = "applied"
)

// threadClearRecord is the durable identity fence for one clear request. A
// reserved record survives a daemon restart so the same request can resume (or
// recover the already-installed replacement) without accepting a second clear
// against the same stable workspace ref. The journal holds at most one record
// per stable ref: a newer clear's reservation supersedes any older record for
// the same ref, whose expected instance no such retry can name anymore.
type threadClearRecord struct {
	ClientMutationID   string                       `json:"client_mutation_id"`
	RequestHash        string                       `json:"request_hash"`
	Ref                string                       `json:"ref"`
	ExpectedInstanceID string                       `json:"expected_instance_id"`
	State              threadClearRecordState       `json:"state"`
	Response           *appwire.ThreadClearResponse `json:"response,omitempty"`
}

type threadClearJournal struct {
	Records []threadClearRecord `json:"records"`
}

// threadClearJournalFile is the in-memory view of the journal and the file it
// persists to. It holds no lock: Server.mu guards it, and every method here
// expects the caller to hold that lock.
type threadClearJournalFile struct {
	path    string
	records map[string]threadClearRecord
	// loadErr is why the journal could not be read at startup; while set, no
	// clear can be recorded, so none may run.
	loadErr error
}

func openThreadClearJournal(stateDir string) threadClearJournalFile {
	path := threadClearJournalPath(stateDir)
	records, err := loadThreadClearJournal(path)
	if records == nil {
		records = make(map[string]threadClearRecord)
	}
	return threadClearJournalFile{path: path, records: records, loadErr: err}
}

func (j *threadClearJournalFile) persist() error {
	return persistThreadClearJournal(j.path, j.records)
}

// threadClearReservation remembers the records a reservation superseded, so a
// failed clear can put them back.
type threadClearReservation struct {
	before   map[string]threadClearRecord
	reserved map[string]threadClearRecord
}

// reserve records that record's clear is in flight. It supersedes every older
// record for the same stable ref: the instance those records expected (or
// installed) is being replaced, so none of them can be a live client's retry
// anymore. That bounds the journal at one record per ref instead of one per
// clear, forever. When the reservation cannot be persisted the journal is
// left as it was.
func (j *threadClearJournalFile) reserve(record threadClearRecord) (threadClearReservation, error) {
	before := maps.Clone(j.records)
	for id, existing := range j.records {
		if existing.Ref == record.Ref {
			delete(j.records, id)
		}
	}
	j.records[record.ClientMutationID] = record
	if err := j.persist(); err != nil {
		j.records = before
		return threadClearReservation{}, err
	}
	return threadClearReservation{before: before, reserved: maps.Clone(j.records)}, nil
}

// release withdraws a reservation whose clear installed nothing. The records it
// superseded describe the still-current instance, so restoring them keeps an
// older receipt's replay alive after a failed newer clear. If the restored
// journal cannot be persisted, memory goes back to the reservation the journal
// last persisted: disk may already hold the rollback if only the post-rename
// directory sync failed, but either state is safe, since every persist
// rewrites the whole file from memory and a retry that re-invokes the clear
// re-runs work that failed without installing anything.
func (j *threadClearJournalFile) release(r threadClearReservation) error {
	j.records = r.before
	if err := j.persist(); err != nil {
		j.records = r.reserved
		return err
	}
	return nil
}

// markApplied stores the receipt of a clear whose replacement is installed. If
// the receipt cannot be persisted the record stays reserved, so a retry
// persists it before reporting success.
func (j *threadClearJournalFile) markApplied(clientMutationID string, response *appwire.ThreadClearResponse) error {
	reserved := j.records[clientMutationID]
	applied := reserved
	applied.State = threadClearApplied
	applied.Response = response
	j.records[clientMutationID] = applied
	if err := j.persist(); err != nil {
		j.records[clientMutationID] = reserved
		return err
	}
	return nil
}

func threadClearJournalPath(stateDir string) string {
	if strings.TrimSpace(stateDir) == "" {
		return ""
	}
	return filepath.Join(stateDir, threadClearJournalName)
}

func loadThreadClearJournal(path string) (map[string]threadClearRecord, error) {
	records := make(map[string]threadClearRecord)
	if path == "" {
		return records, nil
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return records, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read thread clear journal: %w", err)
	}
	var journal threadClearJournal
	if err := json.Unmarshal(data, &journal); err != nil {
		return nil, fmt.Errorf("decode thread clear journal: %w", err)
	}
	for _, record := range journal.Records {
		if strings.TrimSpace(record.ClientMutationID) == "" {
			return nil, errors.New("thread clear journal contains a record without a client mutation id")
		}
		if record.State != threadClearReserved && record.State != threadClearApplied {
			return nil, fmt.Errorf("thread clear journal contains unknown state %q", record.State)
		}
		if _, exists := records[record.ClientMutationID]; exists {
			return nil, fmt.Errorf("thread clear journal contains duplicate client mutation id %q", record.ClientMutationID)
		}
		records[record.ClientMutationID] = record
	}
	return records, nil
}

func persistThreadClearJournal(path string, records map[string]threadClearRecord) error {
	if path == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create thread clear journal directory: %w", err)
	}
	if err := os.Chmod(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("secure thread clear journal directory: %w", err)
	}
	ids := make([]string, 0, len(records))
	for id := range records {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	journal := threadClearJournal{Records: make([]threadClearRecord, 0, len(ids))}
	for _, id := range ids {
		journal.Records = append(journal.Records, records[id])
	}
	data, err := json.Marshal(journal)
	if err != nil {
		return fmt.Errorf("encode thread clear journal: %w", err)
	}
	if err := fsatomic.Write(path, data, 0o600); err != nil {
		return fmt.Errorf("write thread clear journal: %w", err)
	}
	return nil
}

func threadClearRequestHash(params appwire.ThreadClearParams) (string, error) {
	data, err := json.Marshal(params)
	if err != nil {
		return "", fmt.Errorf("encode thread clear request for hashing: %w", err)
	}
	hash := sha256.Sum256(data)
	return hex.EncodeToString(hash[:]), nil
}
