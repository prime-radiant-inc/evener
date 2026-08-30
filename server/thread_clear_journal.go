package server

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"primeradiant.com/evener/appwire"
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
	tmp := path + ".tmp"
	file, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("open thread clear journal temporary file: %w", err)
	}
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		_ = os.Remove(tmp)
		return fmt.Errorf("write thread clear journal: %w", err)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		_ = os.Remove(tmp)
		return fmt.Errorf("sync thread clear journal: %w", err)
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("close thread clear journal: %w", err)
	}
	if err := os.Chmod(tmp, 0o600); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("secure thread clear journal: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("replace thread clear journal: %w", err)
	}
	// The rename is only durable once the directory entry itself is synced; a
	// crash right after the rename could otherwise lose the replacement.
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return fmt.Errorf("open thread clear journal directory: %w", err)
	}
	if err := dir.Sync(); err != nil {
		_ = dir.Close()
		return fmt.Errorf("sync thread clear journal directory: %w", err)
	}
	if err := dir.Close(); err != nil {
		return fmt.Errorf("close thread clear journal directory: %w", err)
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
