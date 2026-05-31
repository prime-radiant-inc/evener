package agent

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/oklog/ulid/v2"

	"primeradiant.com/serf/llm"
)

// ForkSession creates a new session branched from a parent session at a given divergence turn.
//
// divergenceTurn is a 1-based index into the parent's full entry list (all turns, not
// just USER_INPUT). It copies the first (divergenceTurn-1) entries into a new child
// transcript, then appends the edited message as a new USER_INPUT turn at position
// divergenceTurn; the entry it replaces must itself be a USER_INPUT turn. The child gets
// its own fresh session ID and metadata pointing back to the parent.
//
// If parentForkLabel is non-empty, the parent's meta is updated with that label so the
// parent branch can be identified in session listings.
func ForkSession(stateDir, parentID string, divergenceTurn int, editedMessage, parentForkLabel string) (string, error) {
	if divergenceTurn < 1 {
		return "", fmt.Errorf("divergenceTurn must be >= 1, got %d", divergenceTurn)
	}

	transcriptPath := filepath.Join(stateDir, sessionsSubdir, parentID+".transcript.jsonl")

	// Read the raw transcript lines so we can replay entry lines into the child.
	f, err := os.Open(transcriptPath)
	if err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("parent transcript not found for session %s", parentID)
		}
		return "", fmt.Errorf("open parent transcript: %w", err)
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 10*1024*1024)

	// First line is the header.
	if !scanner.Scan() {
		if err := scanner.Err(); err != nil {
			return "", fmt.Errorf("reading parent transcript header: %w", err)
		}
		return "", fmt.Errorf("parent transcript is empty")
	}

	var parentHeader TranscriptHeader
	if err := json.Unmarshal(scanner.Bytes(), &parentHeader); err != nil {
		return "", fmt.Errorf("parsing parent transcript header: %w", err)
	}

	// Collect all entry lines from the parent transcript.
	// divergenceTurn is a 1-based index into the full entry list (all turns, not just
	// USER_INPUT). The entry at position divergenceTurn (1-based) must be a USER_INPUT
	// turn; it is replaced by the edited message in the child. All entries before it
	// form the prefix.
	//
	// Example with transcript [U1, A1, U2, A2] and divergenceTurn=3:
	//   entries[2] (1-based: turn 3) = U2. Prefix = [U1, A1].
	//   Child = [U1, A1, edited-U2].
	var allEntries []TranscriptEntry

	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		// Peek at "kind" to decide how to handle.
		var peek struct {
			Kind string `json:"kind"`
		}
		if err := json.Unmarshal(line, &peek); err != nil {
			continue // skip corrupt lines
		}

		if peek.Kind != "entry" {
			continue // skip api_call lines and anything else
		}

		var entry TranscriptEntry
		if err := json.Unmarshal(line, &entry); err != nil {
			continue // skip corrupt entry lines
		}

		allEntries = append(allEntries, entry)
	}

	if err := scanner.Err(); err != nil {
		return "", fmt.Errorf("reading parent transcript entries: %w", err)
	}

	// Validate that divergenceTurn points to a valid entry in the parent transcript.
	if divergenceTurn > len(allEntries) {
		return "", fmt.Errorf("divergenceTurn %d exceeds parent turn count %d", divergenceTurn, len(allEntries))
	}

	// The entry at the divergence position must be a USER_INPUT turn.
	divergenceEntry := allEntries[divergenceTurn-1]
	if divergenceEntry.Turn.Kind != TurnUserInput {
		return "", fmt.Errorf("entry at divergenceTurn %d is not a USER_INPUT turn (got %s)", divergenceTurn, divergenceEntry.Turn.Kind)
	}

	// Prefix is all entries before the divergence position.
	prefixEntries := allEntries[:divergenceTurn-1]

	// Load parent meta — required for copying fields to the child.
	parentMeta, err := LoadSessionMeta(stateDir, parentID)
	if err != nil {
		return "", fmt.Errorf("load parent session meta: %w", err)
	}

	// Mint a new child session ID.
	childID := ulid.Make().String()

	// Build the child transcript header from the parent's fields.
	now := time.Now().UTC()
	childHeader := TranscriptHeader{
		SessionID:        childID,
		ParentSessionID:  parentID,
		CreatedAt:        now,
		ProfileID:        parentHeader.ProfileID,
		Model:            parentHeader.Model,
		WorkingDir:       parentHeader.WorkingDir,
		Depth:            parentHeader.Depth,
		BuildVersion:     parentHeader.BuildVersion,
		SystemPrompt:     parentHeader.SystemPrompt,
		AgentTasks:       parentHeader.AgentTasks,
		Task:             parentHeader.Task,
		ParentToolCallID: parentHeader.ParentToolCallID,
	}

	childTranscriptPath := filepath.Join(stateDir, sessionsSubdir, childID+".transcript.jsonl")
	tw, err := NewTranscriptWriter(childTranscriptPath, childHeader)
	if err != nil {
		return "", fmt.Errorf("create child transcript: %w", err)
	}
	defer tw.Close()

	modelResponses := 0
	// Replay prefix entries into the child transcript.
	for _, entry := range prefixEntries {
		if err := tw.Append(entry.Turn); err != nil {
			return "", fmt.Errorf("append prefix turn to child transcript: %w", err)
		}
		if entry.Turn.Kind == TurnAssistant {
			modelResponses++
		}
	}

	// Append the edited turn as a new USER_INPUT turn.
	editedTurn := NewTurn(TurnUserInput, llm.User(editedMessage))
	if err := tw.Append(editedTurn); err != nil {
		return "", fmt.Errorf("append edited turn to child transcript: %w", err)
	}

	if err := tw.Close(); err != nil {
		return "", fmt.Errorf("close child transcript: %w", err)
	}

	// Build and save the child meta.
	childMeta := SessionMeta{
		ID:              childID,
		ProfileID:       parentMeta.ProfileID,
		Model:           parentMeta.Model,
		Config:          parentMeta.Config,
		EnvInfo:         parentMeta.EnvInfo,
		CreatedAt:       now,
		UpdatedAt:       now,
		TurnCount:       modelResponses,
		OriginalPrompt:  parentMeta.OriginalPrompt,
		ParentSessionID: parentID,
		DivergenceTurn:  divergenceTurn,
		ForkLabel:       "", // child carries no fork label; parent gets it
	}

	if err := SaveSessionMeta(stateDir, childMeta); err != nil {
		return "", fmt.Errorf("save child session meta: %w", err)
	}

	// Update the parent meta with the fork label if provided.
	if parentForkLabel != "" {
		parentMeta.ForkLabel = parentForkLabel
		if err := SaveSessionMeta(stateDir, parentMeta); err != nil {
			return "", fmt.Errorf("update parent session meta with fork label: %w", err)
		}
	}

	return childID, nil
}
