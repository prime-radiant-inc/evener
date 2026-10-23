package agent

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/llm"
)

// persistWaitCheckpoint records only transcript evidence, never model history.
// Callers own activation/communicate admission and delegate journal validation.
// A retained-unsynced error must not be treated as a safe yield or re-appended:
// the existing writer's EstablishDurability barrier settles that record.
func (s *Session) persistWaitCheckpoint(checkpoint schema.WaitCheckpointInfo) error {
	if err := validateWaitCheckpoint(checkpoint, s.id); err != nil {
		return err
	}
	writer := s.attachedTranscript()
	if writer == nil {
		return errors.New("wait checkpoint requires a transcript writer")
	}
	if header := writer.Header(); header.SessionID != s.id {
		return &transcript.SessionIDMismatchError{HeaderSessionID: header.SessionID, RequestedSessionID: s.id}
	}
	rec, err := s.recordTranscriptOnlyThrough(schema.Turn{
		Kind: schema.TurnWaitCheckpoint, WaitCheckpoint: &checkpoint,
	}, transcript.DoorSynced, transcript.PlaceInTurn(waitCheckpointRecordTurn(checkpoint)))
	if err != nil {
		return err
	}
	if !rec.Recorded {
		return errors.New("wait checkpoint was not recorded")
	}
	return nil
}

// readWaitCheckpoint returns the latest yield or resumption in a validated
// transcript. Identical retries are idempotent, including an old yield repeated
// after its resumption. Contradictions and incomplete tails return no evidence.
// Only a successful complete read with no checkpoints returns (nil, nil).
//
// This read is read-only: readable bytes do not prove durability. Before using
// the result as safe waiting, the caller must establish durability through the
// existing transcript writer's EstablishDurability and validate the activation,
// communicate outcome and delegate journal generation. This helper is not a
// restore classifier and does not establish that the latest execution yielded.
func readWaitCheckpoint(path, sessionID string) (*schema.WaitCheckpointInfo, error) {
	if sessionID == "" {
		return nil, errors.New("wait checkpoint requires a session ID")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open wait checkpoint transcript: %w", err)
	}
	defer func() { _ = file.Close() }()
	reader := bufio.NewReader(file)
	header, err := transcript.ReadHeader(context.Background(), reader, transcript.DefaultMaxLineBytes)
	if err != nil {
		return nil, err
	}
	if header.SessionID != sessionID {
		return nil, &transcript.SessionIDMismatchError{HeaderSessionID: header.SessionID, RequestedSessionID: sessionID}
	}

	checkpoints := make(map[string]schema.WaitCheckpointInfo)
	var latest *schema.WaitCheckpointInfo
	for {
		line, complete, read, err := transcript.ReadLine(reader, transcript.DefaultMaxLineBytes)
		if err != nil {
			return nil, err
		}
		if !complete {
			if read != 0 {
				return nil, fmt.Errorf("incomplete wait checkpoint transcript: %w", io.ErrUnexpectedEOF)
			}
			return latest, nil
		}
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		entry, err := transcript.DecodeEntry(line)
		if err != nil {
			return nil, err
		}
		turn := entry.Turn
		if turn.Kind != schema.TurnWaitCheckpoint {
			if turn.WaitCheckpoint != nil {
				return nil, errors.New("wait checkpoint payload on another record kind")
			}
			continue
		}
		if turn.WaitCheckpoint == nil {
			return nil, errors.New("wait checkpoint record has no payload")
		}
		checkpoint := *turn.WaitCheckpoint
		if err := validateWaitCheckpoint(checkpoint, sessionID); err != nil {
			return nil, err
		}
		if turn.Format != schema.TurnFormatIdentity || turn.TurnID != waitCheckpointRecordTurn(checkpoint) {
			return nil, errors.New("wait checkpoint record has mismatched turn identity")
		}

		previous, exists := checkpoints[checkpoint.ExecutionTurnID]
		if !exists {
			if checkpoint.ResumedByTurnID != "" {
				return nil, errors.New("wait checkpoint resumption has no earlier yield")
			}
		} else {
			identity := checkpoint
			identity.ResumedByTurnID = previous.ResumedByTurnID
			if identity != previous {
				return nil, errors.New("wait checkpoint identity contradicts earlier yield")
			}
			if checkpoint.ResumedByTurnID == "" || checkpoint.ResumedByTurnID == previous.ResumedByTurnID {
				continue // A duplicate cannot reopen a claimed yield or rewind latest.
			}
			if previous.ResumedByTurnID != "" {
				return nil, errors.New("wait checkpoint has conflicting resumption claims")
			}
		}
		checkpoints[checkpoint.ExecutionTurnID] = checkpoint
		latest = &checkpoint
	}
}

func validateWaitCheckpoint(checkpoint schema.WaitCheckpointInfo, sessionID string) error {
	if checkpoint.SessionID == "" || checkpoint.SessionID != sessionID {
		return errors.New("wait checkpoint session identity does not match")
	}
	if checkpoint.ExecutionTurnID == "" || checkpoint.CommunicateCallID == "" {
		return errors.New("wait checkpoint requires execution and communicate identity")
	}
	if checkpoint.ResumedByTurnID != "" && checkpoint.ResumedByTurnID == checkpoint.ExecutionTurnID {
		return errors.New("wait checkpoint resumption requires a new turn ID")
	}
	return nil
}

func waitCheckpointRecordTurn(checkpoint schema.WaitCheckpointInfo) string {
	if checkpoint.ResumedByTurnID != "" {
		return checkpoint.ResumedByTurnID
	}
	return checkpoint.ExecutionTurnID
}

// safeWaitBoundary checks execution evidence, not just checkpoint shape. The
// caller still owns the durability barrier and the journal generation check.
// A resumption claim never covers a later interrupted activation.
func safeWaitBoundary(checkpoint schema.WaitCheckpointInfo, entries []transcript.Entry) bool {
	if checkpoint.ResumedByTurnID != "" {
		return false
	}
	started, completed, communicated, accepted := false, false, false, false
	pending := make(map[string]bool)
	for _, entry := range entries {
		turn := entry.Turn
		if turn.OriginalOrdinal != nil {
			continue // a compaction copy is not a new execution or outcome
		}
		if turn.TurnKind == schema.TurnSpanExecution || turn.Kind == schema.TurnReopen {
			if started || turn.TurnID != checkpoint.ExecutionTurnID {
				// Earlier executions are fine, later ones invalidate this wait.
				if started {
					return false
				}
				continue
			}
			started = true
		}
		if !started {
			continue
		}
		// Questions and approval state can be recorded in delivery/gap spans,
		// not only in the yielding execution. They retain their own hold owner.
		if completed && turn.Notice != nil && turn.Notice.Kind == schema.NoticeApprovalDecision {
			return false
		}
		for _, part := range turn.Message.Content {
			if completed && part.ToolResult != nil && part.ToolResult.Name == askUserToolName {
				return false
			}
		}
		if turn.Kind == schema.TurnFailure || turn.Kind == schema.TurnCompletion && turn.TurnID != checkpoint.ExecutionTurnID {
			return false
		}
		if turn.TurnID != checkpoint.ExecutionTurnID {
			// Delivery turns do not resume an execution. The executor does.
			continue
		}
		switch turn.Kind {
		case schema.TurnFailure, schema.TurnReopen:
			return false
		case schema.TurnAssistant, schema.TurnTool, schema.TurnToolResults:
			if completed {
				return false
			}
		case schema.TurnCommunicate:
			info := turn.Communicate
			if completed || info == nil || !pending[info.CallID] {
				return false
			}
			if info.CallID != checkpoint.CommunicateCallID {
				if info.EndTurn || communicated {
					return false
				}
				break // An earlier nonterminal update still needs its result below.
			}
			if !info.EndTurn || info.EndReason != "waiting_on_work" || info.WaitingOn != checkpoint.WaitingOn {
				return false
			}
			communicated = true
		case schema.TurnCompletion:
			if turn.Completion == nil || turn.Completion.Status != schema.TurnCompleted || len(pending) != 0 || !communicated || !accepted {
				return false
			}
			completed = true
		}
		for _, part := range turn.Message.Content {
			switch part.Kind {
			case llm.ContentToolCall:
				call := part.ToolCall
				if call == nil || call.ID == "" || pending[call.ID] {
					return false
				}
				pending[call.ID] = true
				if call.ID == checkpoint.CommunicateCallID {
					var args struct {
						EndTurn   bool   `json:"end_turn"`
						EndReason string `json:"end_reason"`
						WaitingOn string `json:"waiting_on"`
					}
					if call.Name != "communicate" || json.Unmarshal(call.Arguments, &args) != nil || !args.EndTurn || args.EndReason != "waiting_on_work" || args.WaitingOn != checkpoint.WaitingOn {
						return false
					}
				}
			case llm.ContentToolResult:
				result := part.ToolResult
				if result == nil || !pending[result.ToolCallID] {
					return false
				}
				delete(pending, result.ToolCallID)
				if result.ToolCallID == checkpoint.CommunicateCallID {
					var state communicateEndState
					if result.Name != "communicate" || result.IsError || json.Unmarshal(result.ToolState, &state) != nil || state.EndReason != "waiting_on_work" {
						return false
					}
					accepted = true
				}
			}
		}
	}
	return started && completed && communicated && accepted && len(pending) == 0
}
