package agent

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/afero"

	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/agent/transcript"
	"primeradiant.com/serf/identifier"
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
	return forkSessionFS(afero.NewOsFs(), stateDir, parentID, divergenceTurn, editedMessage, parentForkLabel)
}

func forkSessionFS(fs afero.Fs, stateDir, parentID string, divergenceTurn int, editedMessage, parentForkLabel string) (string, error) {
	return forkSessionWithDeps(fs, stateDir, parentID, divergenceTurn, editedMessage, parentForkLabel, forkSessionDeps{
		newWriter: func(fs afero.Fs, path string, header transcript.Header) (forkTranscriptWriter, error) {
			return transcript.NewWriterWithFS(fs, path, header)
		},
		saveMeta:     schema.SaveSessionMetaWithFS,
		maxScanToken: 10 * 1024 * 1024,
	})
}

type forkTranscriptWriter interface {
	Append(schema.Turn) error
	Close() error
}

type forkSessionDeps struct {
	newWriter    func(afero.Fs, string, transcript.Header) (forkTranscriptWriter, error)
	saveMeta     func(afero.Fs, string, schema.SessionMeta) error
	maxScanToken int
}

func forkSessionWithDeps(fs afero.Fs, stateDir, parentID string, divergenceTurn int, editedMessage, parentForkLabel string, deps forkSessionDeps) (string, error) {
	if divergenceTurn < 1 {
		return "", fmt.Errorf("divergenceTurn must be >= 1, got %d", divergenceTurn)
	}

	transcriptPath := filepath.Join(stateDir, sessionsSubdir, parentID+".transcript.jsonl")

	// Read the raw transcript lines so we can replay entry lines into the child.
	f, err := fs.Open(transcriptPath)
	if err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("parent transcript not found for session %s", parentID)
		}
		return "", fmt.Errorf("open parent transcript: %w", err)
	}
	defer func() { _ = f.Close() }() // read-only handle; close error is immaterial

	maxScanToken := deps.maxScanToken
	if maxScanToken <= 0 {
		maxScanToken = 10 * 1024 * 1024
	}
	reader := bufio.NewReaderSize(f, min(64*1024, maxScanToken))

	// The first non-empty complete line is the header.
	var parentHeader transcript.Header
	for {
		line, complete, _, readErr := transcript.ReadLine(reader, maxScanToken)
		if readErr != nil {
			return "", fmt.Errorf("reading parent transcript header: %w", readErr)
		}
		if !complete {
			return "", errors.New("parent transcript is empty")
		}
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		parentHeader, err = transcript.DecodeHeader(line)
		if err != nil {
			return "", fmt.Errorf("parsing parent transcript header: %w", err)
		}
		break
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
	var allEntries []transcript.Entry

	for {
		line, complete, _, readErr := transcript.ReadLine(reader, maxScanToken)
		if readErr != nil {
			return "", fmt.Errorf("reading parent transcript entries: %w", readErr)
		}
		if !complete {
			break
		}
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}

		entry, err := transcript.DecodeEntry(line)
		if err != nil {
			return "", fmt.Errorf("parsing parent transcript entry: %w", err)
		}

		allEntries = append(allEntries, entry)
	}

	// Validate that divergenceTurn points to a valid entry in the parent transcript.
	if divergenceTurn > len(allEntries) {
		return "", fmt.Errorf("divergenceTurn %d exceeds parent turn count %d", divergenceTurn, len(allEntries))
	}

	// The entry at the divergence position must be a USER_INPUT turn.
	divergenceEntry := allEntries[divergenceTurn-1]
	if divergenceEntry.Turn.Kind != schema.TurnUserInput {
		return "", fmt.Errorf("entry at divergenceTurn %d is not a USER_INPUT turn (got %s)", divergenceTurn, divergenceEntry.Turn.Kind)
	}

	// Prefix is all entries before the divergence position.
	prefixEntries := allEntries[:divergenceTurn-1]

	// Load parent meta — required for copying fields to the child.
	parentMeta, err := schema.LoadSessionMetaWithFS(fs, stateDir, parentID)
	if err != nil {
		return "", fmt.Errorf("load parent session meta: %w", err)
	}

	// Mint a new child session ID.
	childID, err := identifier.NewSessionID()
	if err != nil {
		return "", fmt.Errorf("generate child session ID: %w", err)
	}

	// Build the child transcript header from the parent's fields.
	now := time.Now().UTC()
	childHeader := transcript.Header{
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
	tw, err := deps.newWriter(fs, childTranscriptPath, childHeader)
	if err != nil {
		return "", fmt.Errorf("create child transcript: %w", err)
	}
	// Safety net for early error returns; the success path closes tw explicitly
	// below (line ~165) and checks that error, after which this defer is a no-op.
	defer func() { _ = tw.Close() }()

	modelResponses := 0
	acceptedInputTurns := 0
	// Replay prefix entries into the child transcript.
	for _, entry := range prefixEntries {
		if err := tw.Append(entry.Turn); err != nil {
			return "", fmt.Errorf("append prefix turn to child transcript: %w", err)
		}
		if entry.Turn.Kind == schema.TurnAssistant {
			modelResponses++
		}
		if entry.Turn.Kind == schema.TurnUserInput {
			acceptedInputTurns++
		}
	}

	// Append the edited turn as a new USER_INPUT turn.
	editedTurn := schema.NewTurn(schema.TurnUserInput, llm.User(editedMessage))
	if err := tw.Append(editedTurn); err != nil {
		return "", fmt.Errorf("append edited turn to child transcript: %w", err)
	}
	acceptedInputTurns++

	if err := tw.Close(); err != nil {
		return "", fmt.Errorf("close child transcript: %w", err)
	}

	// Build and save the child meta.
	childMeta := schema.SessionMeta{
		ID:                 childID,
		ProfileID:          parentMeta.ProfileID,
		Model:              parentMeta.Model,
		Config:             parentMeta.Config,
		EnvInfo:            parentMeta.EnvInfo,
		CreatedAt:          now,
		UpdatedAt:          now,
		TurnCount:          modelResponses,
		AcceptedInputTurns: acceptedInputTurns,
		OriginalPrompt:     parentMeta.OriginalPrompt,
		ParentSessionID:    parentID,
		DivergenceTurn:     divergenceTurn,
		ForkLabel:          "", // child carries no fork label; parent gets it
	}

	if err := deps.saveMeta(fs, stateDir, childMeta); err != nil {
		return "", fmt.Errorf("save child session meta: %w", err)
	}

	// Update the parent meta with the fork label if provided.
	if parentForkLabel != "" {
		parentMeta.ForkLabel = parentForkLabel
		if err := deps.saveMeta(fs, stateDir, parentMeta); err != nil {
			return "", fmt.Errorf("update parent session meta with fork label: %w", err)
		}
	}

	return childID, nil
}
