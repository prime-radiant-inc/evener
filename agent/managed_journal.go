package agent

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"sync"
)

const managedJournalVersion = 1
const maxManagedJournalBytes = 32 << 20
const maxManagedPending = 256
const maxManagedResultBytes = 4 << 20
const maxManagedRequestBytes = 4 << 20

type managedInvocation struct {
	ID             string         `json:"id"`
	SessionID      string         `json:"session_id"`
	AttemptGroupID string         `json:"attempt_group_id"`
	ToolIndex      int            `json:"tool_index"`
	AssistantSeq   int            `json:"assistant_seq"`
	ToolName       string         `json:"tool_name"`
	CallID         string         `json:"call_id"`
	Arguments      []byte         `json:"original_arguments"`
	Request        ManagedRequest `json:"request"`
	Result         *ManagedResult `json:"result,omitempty"`
}

// MarshalJSON keeps immutable dispatch bytes out of RawMessage's compaction.
func (m managedInvocation) MarshalJSON() ([]byte, error) {
	type plain managedInvocation
	raw := []byte(m.Request.Arguments)
	m.Request.Arguments = nil
	return json.Marshal(struct {
		plain
		RequestArguments []byte `json:"request_arguments"`
	}{plain(m), raw})
}
func (m *managedInvocation) UnmarshalJSON(data []byte) error {
	type plain managedInvocation
	v := struct {
		plain
		RequestArguments []byte `json:"request_arguments"`
	}{}
	if err := json.Unmarshal(data, &v); err != nil {
		return err
	}
	*m = managedInvocation(v.plain)
	m.Request.Arguments = append(json.RawMessage(nil), v.RequestArguments...)
	return nil
}

type managedJournalSnapshot struct {
	Version   int                 `json:"version"`
	SessionID string              `json:"session_id"`
	Pending   []managedInvocation `json:"pending"`
}

type managedJournal struct {
	mu              sync.Mutex
	path, sessionID string
	entries         map[string]managedInvocation
	dirty           bool
	// fault names the real filesystem barrier about to be attempted. Nil in production.
	fault func(string) error
}

func openManagedJournal(path, sessionID string) (*managedJournal, error) {
	j := &managedJournal{path: path, sessionID: sessionID, entries: map[string]managedInvocation{}}
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return j, nil
	}
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	raw, err := io.ReadAll(io.LimitReader(f, maxManagedJournalBytes+1))
	if err != nil {
		return nil, err
	}
	if len(raw) > maxManagedJournalBytes {
		return nil, errors.New("managed journal too large")
	}
	var snapshot managedJournalSnapshot
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&snapshot); err != nil {
		return nil, fmt.Errorf("decode managed journal: %w", err)
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return nil, errors.New("trailing managed journal data")
	}
	if snapshot.Version != managedJournalVersion || snapshot.SessionID != sessionID || len(snapshot.Pending) > maxManagedPending {
		return nil, errors.New("invalid managed journal identity or version")
	}
	for _, entry := range snapshot.Pending {
		if err := j.validate(entry); err != nil {
			return nil, err
		}
		if _, ok := j.entries[entry.ID]; ok {
			return nil, errors.New("duplicate managed invocation")
		}
		for _, existing := range j.entries {
			if sameManagedOccurrence(existing, entry) {
				return nil, errors.New("duplicate managed occurrence")
			}
		}
		j.entries[entry.ID] = entry
	}
	// Reopening acknowledges readability, not a prior process's fsync. Establish
	// file and directory durability before any replay or journal settlement.
	j.dirty = true
	return j, nil
}
func sameManagedOccurrence(a, b managedInvocation) bool {
	return a.SessionID == b.SessionID && a.AttemptGroupID == b.AttemptGroupID && a.ToolIndex == b.ToolIndex
}
func (j *managedJournal) validate(e managedInvocation) error {
	if len(e.Request.Arguments) > maxManagedRequestBytes || len(e.Arguments) > maxManagedRequestBytes {
		return errors.New("managed arguments too large")
	}
	if e.Request.ToolName != e.ToolName || e.Request.ToolCallID != e.CallID || e.Request.InvocationID != e.ID {
		return errors.New("managed request occurrence mismatch")
	}
	if e.ID == "" || e.SessionID != j.sessionID || e.AttemptGroupID == "" || e.ToolIndex < 0 || e.AssistantSeq < 0 || e.ToolName == "" || e.CallID == "" || e.Request.Operation == "" || e.Request.InvocationID == "" || !json.Valid(e.Request.Arguments) || !json.Valid(e.Arguments) {
		return errors.New("incomplete managed invocation")
	}
	if err := validateManagedIdentity(e.Request.Identity); err != nil {
		return err
	}
	if e.Result != nil {
		raw, err := json.Marshal(e.Result)
		if err != nil || len(raw) > maxManagedResultBytes {
			return errors.New("invalid or oversized managed result")
		}
	}
	return nil
}
func cloneManagedInvocation(e managedInvocation) managedInvocation {
	e.Arguments = append([]byte(nil), e.Arguments...)
	e.Request.Arguments = append(json.RawMessage(nil), e.Request.Arguments...)
	if e.Result != nil {
		raw, _ := json.Marshal(e.Result)
		var result ManagedResult
		_ = json.Unmarshal(raw, &result)
		e.Result = &result
	}
	return e
}
func (j *managedJournal) pending() []managedInvocation {
	if j == nil {
		return nil
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.pendingLocked()
}
func (j *managedJournal) pendingLocked() []managedInvocation {
	result := make([]managedInvocation, 0, len(j.entries))
	for _, e := range j.entries {
		result = append(result, cloneManagedInvocation(e))
	}
	sort.Slice(result, func(a, b int) bool {
		if result[a].AssistantSeq != result[b].AssistantSeq {
			return result[a].AssistantSeq < result[b].AssistantSeq
		}
		return result[a].ToolIndex < result[b].ToolIndex
	})
	return result
}
func (j *managedJournal) put(e managedInvocation) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if err := j.validate(e); err != nil {
		return err
	}
	if old, ok := j.entries[e.ID]; ok {
		previous, next := cloneManagedInvocation(old), cloneManagedInvocation(e)
		previous.Result, next.Result = nil, nil
		a, _ := json.Marshal(previous)
		b, _ := json.Marshal(next)
		if !bytes.Equal(a, b) {
			return errors.New("managed request is immutable")
		}
		if old.Result != nil {
			a, _ = json.Marshal(old.Result)
			b, _ = json.Marshal(e.Result)
			if !bytes.Equal(a, b) {
				return errors.New("managed result is immutable")
			}
		}
	} else {
		if len(j.entries) >= maxManagedPending {
			return errors.New("managed journal capacity exhausted")
		}
		for _, old := range j.entries {
			if sameManagedOccurrence(old, e) {
				return errors.New("managed occurrence already admitted")
			}
		}
	}
	old, existed := j.entries[e.ID]
	j.entries[e.ID] = cloneManagedInvocation(e)
	if _, err := j.encodeLocked(); err != nil {
		if existed {
			j.entries[e.ID] = old
		} else {
			delete(j.entries, e.ID)
		}
		return err
	}
	// Retain even pre-rename candidates in this process: retry cannot remint an
	// operation after an ambiguous filesystem acknowledgment.
	j.dirty = true
	return j.writeLocked()
}
func (j *managedJournal) remove(id string) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.dirty {
		if err := j.writeLocked(); err != nil {
			return err
		}
	}
	old, ok := j.entries[id]
	if !ok {
		return nil
	}
	delete(j.entries, id)
	j.dirty = true
	renamed, err := j.replaceLocked()
	if err != nil && !renamed {
		j.entries[id] = old
	}
	j.dirty = err != nil
	return err
}
func (j *managedJournal) establishDurability() error {
	if j == nil {
		return ErrManagedUnavailable
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.writeLocked()
}
func (j *managedJournal) writeLocked() error {
	_, err := j.replaceLocked()
	j.dirty = err != nil
	return err
}
func (j *managedJournal) barrier(point string) error {
	if j.fault != nil {
		return j.fault(point)
	}
	return nil
}
func (j *managedJournal) replaceLocked() (renamed bool, err error) {
	data, err := j.encodeLocked()
	if err != nil {
		return false, err
	}
	file, err := os.CreateTemp(filepath.Dir(j.path), ".managed-*")
	if err != nil {
		return false, err
	}
	temp := file.Name()
	defer func() { _ = file.Close(); _ = os.Remove(temp) }()
	if _, err = file.Write(data); err != nil {
		return false, err
	}
	if err = j.barrier("file_sync"); err == nil {
		err = file.Sync()
	}
	if err != nil {
		return false, err
	}
	if err := file.Close(); err != nil {
		return false, err
	}
	if err = j.barrier("rename"); err == nil {
		err = os.Rename(temp, j.path)
	}
	if err != nil {
		return false, err
	}
	renamed = true
	dir, err := os.Open(filepath.Dir(j.path))
	if err != nil {
		return true, err
	}
	defer func() { _ = dir.Close() }()
	if err = j.barrier("directory_sync"); err == nil {
		err = dir.Sync()
	}
	return true, err
}

// encodeLocked reserves room for each admitted operation's maximum receipt.
// Capacity refusal happens before admission, so an oversized new operation
// cannot make existing pending work impossible to persist or settle.
func (j *managedJournal) encodeLocked() ([]byte, error) {
	data, err := json.Marshal(managedJournalSnapshot{Version: managedJournalVersion, SessionID: j.sessionID, Pending: j.pendingLocked()})
	if err != nil {
		return nil, err
	}
	reserved := 0
	for _, entry := range j.entries {
		if entry.Result == nil {
			reserved += maxManagedResultBytes + 256
		}
	}
	if len(data)+reserved > maxManagedJournalBytes {
		return nil, errors.New("managed journal capacity exhausted")
	}
	return data, nil
}
