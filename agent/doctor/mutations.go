package doctor

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
)

// clientMutationStoreVersion is the only snapshot version the runtime writes
// (agent's clientMutationSnapshotVersion). Anything else is a shape this reader
// was not written against, and it says so rather than guessing.
const clientMutationStoreVersion = 1

// MutationReport is the forensic view of one session's durable client-mutation
// store. The store journals every client mutation the daemon accepted AND every
// one it rejected, which makes it the artifact that settles "did the user's
// input ever reach the daemon": absence from the journal means the request never
// arrived; presence shows exactly what happened to it.
type MutationReport struct {
	SessionID     string `json:"session_id"`
	MutationsPath string `json:"mutations_path"`
	// Present is false when the session has no store on disk — it accepted no
	// client mutations. That is a clean answer, not an error.
	Present           bool                   `json:"present"`
	AcceptedTurns     uint64                 `json:"accepted_turns"`
	QueueRevision     uint64                 `json:"queue_revision"`
	Journal           []MutationRecordView   `json:"journal"`
	InputQueue        []QueuedInputView      `json:"input_queue"`
	PendingExecutions []PendingExecutionView `json:"pending_executions"`
}

// MutationRecordView is one journaled client mutation: what the client asked
// for, and what the daemon did with it. Rejection code and message are present
// exactly on the records the daemon refused.
type MutationRecordView struct {
	ClientMutationID string `json:"client_mutation_id"`
	Method           string `json:"method"`
	OperationState   string `json:"operation_state"`
	ExecutionState   string `json:"execution_state"`
	StableTurnID     string `json:"stable_turn_id,omitempty"`
	RejectionCode    int    `json:"rejection_code,omitempty"`
	RejectionMessage string `json:"rejection_message,omitempty"`
}

// QueuedInputView is one input still sitting in the durable queue, waiting for a
// turn to drain it. Items is the input-item count; the items themselves are the
// user's content and stay out of the report.
type QueuedInputView struct {
	EntryID          string `json:"entry_id"`
	ClientMutationID string `json:"client_mutation_id"`
	Items            int    `json:"items"`
}

// PendingExecutionView is one mutation the daemon is still executing.
type PendingExecutionView struct {
	ClientMutationID string `json:"client_mutation_id"`
	Method           string `json:"method"`
	ExecutionState   string `json:"execution_state"`
	TurnID           string `json:"turn_id,omitempty"`
	ProjectionState  string `json:"projection_state,omitempty"`
}

// Mutations resolves the selector and reports its durable client-mutation store.
// A session with no store reports Present=false and empty rows; a store whose
// bytes are not a client-mutation snapshot is an error naming the file.
func Mutations(stateBase, selector string) (MutationReport, error) {
	paths, err := Locate(stateBase, selector)
	if err != nil {
		return MutationReport{}, err
	}
	report := MutationReport{
		SessionID:         paths.SessionID,
		MutationsPath:     paths.MutationsPath,
		Journal:           []MutationRecordView{},
		InputQueue:        []QueuedInputView{},
		PendingExecutions: []PendingExecutionView{},
	}
	data, err := os.ReadFile(paths.MutationsPath)
	if errors.Is(err, os.ErrNotExist) {
		return report, nil
	}
	if err != nil {
		return MutationReport{}, fmt.Errorf("read client mutation store %s: %w", paths.MutationsPath, err)
	}
	store, err := decodeClientMutationStore(data, paths.MutationsPath, paths.SessionID)
	if err != nil {
		return MutationReport{}, err
	}

	report.Present = true
	report.AcceptedTurns = store.AcceptedTurns
	report.QueueRevision = store.QueueRevision
	for _, record := range store.Journal {
		view := MutationRecordView{
			ClientMutationID: record.ClientMutationID,
			Method:           record.Method,
			OperationState:   record.OperationState,
			ExecutionState:   record.ExecutionState,
			StableTurnID:     record.StableTurnID,
		}
		if record.Rejection != nil {
			view.RejectionCode = record.Rejection.Code
			view.RejectionMessage = record.Rejection.Message
		}
		report.Journal = append(report.Journal, view)
	}
	sort.Slice(report.Journal, func(i, j int) bool {
		return report.Journal[i].ClientMutationID < report.Journal[j].ClientMutationID
	})
	// The queue is a slice on disk: its order is the drain order, so it is kept.
	for _, entry := range store.InputQueue {
		report.InputQueue = append(report.InputQueue, QueuedInputView{
			EntryID:          entry.ID,
			ClientMutationID: entry.ClientMutationID,
			Items:            len(entry.Input),
		})
	}
	for _, pending := range store.PendingExecutions {
		report.PendingExecutions = append(report.PendingExecutions, PendingExecutionView{
			ClientMutationID: pending.ClientMutationID,
			Method:           pending.Method,
			ExecutionState:   pending.ExecutionState,
			TurnID:           pending.TurnID,
			ProjectionState:  pending.ProjectionState,
		})
	}
	sort.Slice(report.PendingExecutions, func(i, j int) bool {
		return report.PendingExecutions[i].ClientMutationID < report.PendingExecutions[j].ClientMutationID
	})
	return report, nil
}

// clientMutationStoreFile mirrors the persisted shape of agent's
// clientMutationSnapshot (written by saveClientMutationSnapshotFS). The runtime
// types are unexported, so this reader cannot import them; it compensates with
// the runtime's own strictness — unknown fields, trailing bytes, a foreign
// version, a foreign session ID, and absent containers are all refused. Every
// persisted field is declared here even when it is not reported, because a field
// added to the runtime snapshot must break this decode loudly instead of being
// silently dropped from a diagnosis.
type clientMutationStoreFile struct {
	Version                int                                   `json:"version"`
	SessionID              string                                `json:"session_id"`
	ActiveTurnID           string                                `json:"active_turn_id"`
	AcceptedTurns          uint64                                `json:"accepted_turns"`
	Journal                map[string]clientMutationStoreRecord  `json:"journal"`
	InputQueue             []clientMutationStoreQueueEntry       `json:"input_queue"`
	QueueRevision          uint64                                `json:"queue_revision"`
	NextTurnSequence       uint64                                `json:"next_turn_sequence"`
	NextQueueEntrySequence uint64                                `json:"next_queue_entry_sequence"`
	BudgetReservations     map[string]json.RawMessage            `json:"budget_reservations"`
	InterruptFence         json.RawMessage                       `json:"interrupt_fence"`
	PendingExecutions      map[string]clientMutationStorePending `json:"pending_executions"`
	SteeringOrder          []string                              `json:"steering_order"`
}

type clientMutationStoreRecord struct {
	ClientMutationID    string                        `json:"client_mutation_id"`
	Method              string                        `json:"method"`
	Payload             json.RawMessage               `json:"payload"`
	Preconditions       json.RawMessage               `json:"preconditions"`
	StableTurnID        string                        `json:"stable_turn_id"`
	StableQueueEntryIDs []string                      `json:"stable_queue_entry_ids"`
	PayloadHash         string                        `json:"payload_hash"`
	OperationState      string                        `json:"operation_state"`
	ExecutionState      string                        `json:"execution_state"`
	ProjectionState     string                        `json:"projection_state"`
	Result              json.RawMessage               `json:"result"`
	Rejection           *clientMutationStoreRejection `json:"rejection"`
	Failure             json.RawMessage               `json:"failure"`
	AttemptGeneration   uint64                        `json:"attempt_generation"`
}

type clientMutationStoreRejection struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data"`
}

type clientMutationStoreQueueEntry struct {
	ID               string            `json:"id"`
	ClientMutationID string            `json:"client_mutation_id"`
	Input            []json.RawMessage `json:"input"`
}

type clientMutationStorePending struct {
	ClientMutationID string            `json:"client_mutation_id"`
	Method           string            `json:"method"`
	Input            []json.RawMessage `json:"input"`
	ExecutionState   string            `json:"execution_state"`
	TurnID           string            `json:"turn_id"`
	QueueEntryIDs    []string          `json:"queue_entry_ids"`
	ProjectionState  string            `json:"projection_state"`
}

func decodeClientMutationStore(data []byte, path, sessionID string) (clientMutationStoreFile, error) {
	var store clientMutationStoreFile
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&store); err != nil {
		return clientMutationStoreFile{}, fmt.Errorf("decode client mutation store %s: %w", path, err)
	}
	var trailing any
	switch err := decoder.Decode(&trailing); {
	case errors.Is(err, io.EOF):
	case err == nil:
		return clientMutationStoreFile{}, fmt.Errorf("decode client mutation store %s: trailing JSON value", path)
	default:
		return clientMutationStoreFile{}, fmt.Errorf("decode client mutation store %s trailing data: %w", path, err)
	}
	if err := store.validate(sessionID); err != nil {
		return clientMutationStoreFile{}, fmt.Errorf("client mutation store %s: %w", path, err)
	}
	return store, nil
}

// validate refuses a file whose structure this reader does not recognize. The
// runtime writes all three containers non-nil, so a nil one means these bytes
// are not a client-mutation store — and a wrong-shape file must never render as
// an empty journal, because "nothing ever reached the daemon" is the opposite
// conclusion from "this file is not the store".
func (store clientMutationStoreFile) validate(sessionID string) error {
	if store.Version != clientMutationStoreVersion {
		return fmt.Errorf("unsupported version %d", store.Version)
	}
	if store.SessionID != sessionID {
		return fmt.Errorf("session ID %q does not match %q", store.SessionID, sessionID)
	}
	if store.Journal == nil {
		return errors.New("journal is missing")
	}
	if store.BudgetReservations == nil {
		return errors.New("budget reservations are missing")
	}
	if store.PendingExecutions == nil {
		return errors.New("pending executions are missing")
	}
	return nil
}

// RenderMutations renders a MutationReport as a human-readable summary (the
// default, non-JSON output).
func RenderMutations(r MutationReport) string {
	var b strings.Builder
	fmt.Fprintf(&b, "session %s  (mutations: %s)\n", r.SessionID, r.MutationsPath)
	if !r.Present {
		b.WriteString("no client-mutation store on disk — this session accepted no client mutations\n")
		return b.String()
	}
	fmt.Fprintf(&b, "accepted turns: %d  queue revision: %d\n", r.AcceptedTurns, r.QueueRevision)

	rejected := 0
	for _, m := range r.Journal {
		if m.OperationState == "rejected" {
			rejected++
		}
	}
	fmt.Fprintf(&b, "journal: %d %s (%d rejected)\n", len(r.Journal), plural(len(r.Journal), "mutation"), rejected)
	for _, m := range r.Journal {
		fmt.Fprintf(&b, "  · %s  method=%s  operation=%s  execution=%s", dash(m.ClientMutationID), dash(m.Method), dash(m.OperationState), dash(m.ExecutionState))
		if m.StableTurnID != "" {
			fmt.Fprintf(&b, "  turn=%s", m.StableTurnID)
		}
		if m.RejectionMessage != "" || m.RejectionCode != 0 {
			fmt.Fprintf(&b, "  rejection=%d %q", m.RejectionCode, m.RejectionMessage)
		}
		b.WriteString("\n")
	}

	fmt.Fprintf(&b, "input queue: %d %s\n", len(r.InputQueue), plural(len(r.InputQueue), "entry"))
	for _, q := range r.InputQueue {
		fmt.Fprintf(&b, "  · %s  mutation=%s  items=%d\n", dash(q.EntryID), dash(q.ClientMutationID), q.Items)
	}

	fmt.Fprintf(&b, "pending executions: %d\n", len(r.PendingExecutions))
	for _, p := range r.PendingExecutions {
		fmt.Fprintf(&b, "  · %s  method=%s  execution=%s", dash(p.ClientMutationID), dash(p.Method), dash(p.ExecutionState))
		if p.TurnID != "" {
			fmt.Fprintf(&b, "  turn=%s", p.TurnID)
		}
		if p.ProjectionState != "" {
			fmt.Fprintf(&b, "  projection=%s", p.ProjectionState)
		}
		b.WriteString("\n")
	}
	return b.String()
}

// plural returns noun in the number n takes, so "1 mutation" does not render as
// "1 mutations". "entry" is the one noun here that does not just take an "s".
func plural(n int, noun string) string {
	if n == 1 {
		return noun
	}
	if noun == "entry" {
		return "entries"
	}
	return noun + "s"
}
