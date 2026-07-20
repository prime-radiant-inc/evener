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

	parentHeader, allEntries, err := readForkParent(fs, stateDir, parentID, deps.maxScanToken)
	if err != nil {
		return "", err
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

	childID, err := writeForkChild(fs, stateDir, parentID, parentHeader, parentMeta, prefixEntries, divergenceTurn, &editedMessage, deps)
	if err != nil {
		return "", err
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

// readForkParent loads the parent transcript's header and full entry list.
// divergenceTurn indexing in ForkSession is 1-based into this entry list (all
// turns, not just USER_INPUT).
func readForkParent(fs afero.Fs, stateDir, parentID string, maxScanToken int) (transcript.Header, []transcript.Entry, error) {
	transcriptPath := filepath.Join(stateDir, sessionsSubdir, parentID+".transcript.jsonl")

	// Read the raw transcript lines so we can replay entry lines into the child.
	f, err := fs.Open(transcriptPath)
	if err != nil {
		if os.IsNotExist(err) {
			return transcript.Header{}, nil, fmt.Errorf("parent transcript not found for session %s", parentID)
		}
		return transcript.Header{}, nil, fmt.Errorf("open parent transcript: %w", err)
	}
	defer func() { _ = f.Close() }() // read-only handle; close error is immaterial

	if maxScanToken <= 0 {
		maxScanToken = 10 * 1024 * 1024
	}
	reader := bufio.NewReaderSize(f, min(64*1024, maxScanToken))

	// The first non-empty complete line is the header.
	var parentHeader transcript.Header
	for {
		line, complete, _, readErr := transcript.ReadLine(reader, maxScanToken)
		if readErr != nil {
			return transcript.Header{}, nil, fmt.Errorf("reading parent transcript header: %w", readErr)
		}
		if !complete {
			return transcript.Header{}, nil, errors.New("parent transcript is empty")
		}
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		parentHeader, err = transcript.DecodeHeader(line)
		if err != nil {
			return transcript.Header{}, nil, fmt.Errorf("parsing parent transcript header: %w", err)
		}
		break
	}

	// Collect all entry lines from the parent transcript.
	var allEntries []transcript.Entry
	for {
		line, complete, _, readErr := transcript.ReadLine(reader, maxScanToken)
		if readErr != nil {
			return transcript.Header{}, nil, fmt.Errorf("reading parent transcript entries: %w", readErr)
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
			return transcript.Header{}, nil, fmt.Errorf("parsing parent transcript entry: %w", err)
		}

		allEntries = append(allEntries, entry)
	}
	return parentHeader, allEntries, nil
}

// writeForkChild writes the child transcript (prefix entries plus, when
// editedMessage is non-nil, a replacement USER_INPUT turn carrying that text)
// and saves the child meta. divergenceTurn is recorded in the child meta as
// the first turn unique to this branch; editedMessage == nil means the branch
// diverges past the parent's tip with no replacement turn (the aside case).
func writeForkChild(fs afero.Fs, stateDir, parentID string, parentHeader transcript.Header, parentMeta schema.SessionMeta, prefixEntries []transcript.Entry, divergenceTurn int, editedMessage *string, deps forkSessionDeps) (string, error) {
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
	// below and checks that error, after which this defer is a no-op.
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

	// Append the edited turn as a new USER_INPUT turn when this is a
	// divergence fork (aside forks pass nil and copy the tip verbatim).
	if editedMessage != nil {
		editedTurn := schema.NewTurn(schema.TurnUserInput, llm.User(*editedMessage))
		if err := tw.Append(editedTurn); err != nil {
			return "", fmt.Errorf("append edited turn to child transcript: %w", err)
		}
		acceptedInputTurns++
	}

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

	return childID, nil
}
