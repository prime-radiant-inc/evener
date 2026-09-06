package agent

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/llm"
)

// foldDelegateAttentionSeeded folds a resume's suffix entries over the
// prefix snapshot a validated sidecar carried. It reconstructs exactly the
// state foldDelegateAttention would have produced over the full entry list:
// the seed installs each pending attention's content and resolution before
// the suffix entries fold over them, so an attention opened before the
// boundary and resolved after it resolves correctly instead of tripping the
// fold's "resolved before appended" guard.
func foldDelegateAttentionSeeded(sidecar transcript.ResumeSidecar, suffix []transcript.Entry) (delegateAttentionFold, error) {
	if !sidecar.SnapshotsComplete {
		// An incomplete snapshot cannot seed the fold; the caller must fall
		// back to the full file read.
		return delegateAttentionFold{}, errors.New("resume sidecar fold snapshot is incomplete")
	}
	fold := newDelegateAttentionFold()
	for _, pending := range sidecar.PendingAttention {
		var message llm.Message
		if err := json.Unmarshal(pending.Message, &message); err != nil {
			return delegateAttentionFold{}, fmt.Errorf("sidecar attention %q message: %w", pending.AttentionID, err)
		}
		if message.Role == "" {
			return delegateAttentionFold{}, fmt.Errorf("sidecar attention %q message is empty", pending.AttentionID)
		}
		fold.content[pending.AttentionID] = message
		turn := schema.NewTurn(schema.TurnSteering, message)
		turn.AttentionID = pending.AttentionID
		fold.turns[pending.AttentionID] = turn
		fold.order = append(fold.order, pending.AttentionID)
		if resolution := pending.Resolution; resolution != "" {
			fold.resolutions[pending.AttentionID] = delegateAttentionResolution(resolution)
			fold.resumeGenerations[pending.AttentionID] = pending.ResumeGeneration
		}
	}
	for _, commit := range sidecar.DeliveryCommits {
		fold.deliveryCommits[commit.DeliveryID] = commit.ToolCallID
	}
	return foldDelegateAttentionSuffix(fold, suffix)
}

// foldDelegateAttentionSuffix folds suffix entries onto an existing fold
// state. It is the general per-entry fold: foldDelegateAttention is this
// function over a fresh fold, and the seeded form is this function over the
// sidecar's pre-seeded state — one set of rules, three entry points.
func foldDelegateAttentionSuffix(fold delegateAttentionFold, entries []transcript.Entry) (delegateAttentionFold, error) {
	for _, entry := range entries {
		turn := entry.Turn
		if err := foldDelegateDeliveryCommits(&fold, turn); err != nil {
			return delegateAttentionFold{}, err
		}
		if turn.AttentionID != "" {
			if turn.Kind != schema.TurnSteering || turn.AttentionResolution != nil {
				return delegateAttentionFold{}, fmt.Errorf("attention %q is not a steering turn", turn.AttentionID)
			}
			if previous, exists := fold.content[turn.AttentionID]; exists {
				if !reflect.DeepEqual(previous, turn.Message) {
					return delegateAttentionFold{}, fmt.Errorf("attention %q has conflicting content", turn.AttentionID)
				}
			} else {
				fold.content[turn.AttentionID] = turn.Message
				fold.turns[turn.AttentionID] = turn
				fold.order = append(fold.order, turn.AttentionID)
			}
		}
		resolution := turn.AttentionResolution
		if resolution == nil {
			if turn.Kind == schema.TurnAttentionResolution {
				return delegateAttentionFold{}, errors.New("attention resolution turn has no resolution")
			}
			continue
		}
		if turn.Kind != schema.TurnAttentionResolution || turn.AttentionID != "" || resolution.AttentionID == "" {
			return delegateAttentionFold{}, errors.New("invalid attention resolution turn")
		}
		disposition := delegateAttentionResolution(resolution.Disposition)
		if disposition != delegateAttentionConsumed && disposition != delegateAttentionDiscarded {
			return delegateAttentionFold{}, fmt.Errorf("attention %q has invalid resolution %q", resolution.AttentionID, resolution.Disposition)
		}
		if disposition == delegateAttentionDiscarded && resolution.ResumeGeneration != 0 {
			return delegateAttentionFold{}, fmt.Errorf("discarded attention %q has resume generation %d", resolution.AttentionID, resolution.ResumeGeneration)
		}
		if _, exists := fold.content[resolution.AttentionID]; !exists {
			return delegateAttentionFold{}, fmt.Errorf("attention %q resolved before it was appended", resolution.AttentionID)
		}
		if previous, exists := fold.resolutions[resolution.AttentionID]; exists {
			if previous != disposition || fold.resumeGenerations[resolution.AttentionID] != resolution.ResumeGeneration {
				return delegateAttentionFold{}, fmt.Errorf("attention %q has conflicting resolutions", resolution.AttentionID)
			}
			continue
		}
		if resolution.ResumeGeneration != 0 {
			for previousID, generation := range fold.resumeGenerations {
				if generation == resolution.ResumeGeneration && previousID != resolution.AttentionID {
					return delegateAttentionFold{}, fmt.Errorf("resume generation %d claims attention %q and %q", resolution.ResumeGeneration, previousID, resolution.AttentionID)
				}
			}
		}
		fold.resolutions[resolution.AttentionID] = disposition
		fold.resumeGenerations[resolution.AttentionID] = resolution.ResumeGeneration
	}
	return fold, nil
}

// writeCompactionSidecarAnchor writes the resume sidecar from the session's
// attached writer at the boundary the just-appended compaction turn defines.
// It is one of the two anchors: the writer recorded the byte offset where
// the checkpoint entry it just appended BEGINS, so the sidecar windows the
// next resume to exactly ResumeHistory's window — [checkpoint, ...rest],
// the checkpoint entry included.
//
// The fold snapshots are NOT complete for this anchor and it says so: the
// session's live attention state is process-local and cannot vouch for the
// whole prefix the way a full decode can, and the prefix-turn count is not
// computable without the prefix entries. A resume that needs either falls
// back honestly (the rearm re-reads the file; serve's identity projection
// takes the file form) rather than trusting a snapshot this anchor never
// computed. The post-full-scan anchor is the one that computes them.
//
// Best-effort by contract: the error is reported to the caller, which must
// not fail compaction on it.
//
// The clean-shutdown path deliberately writes NO anchor: shutdown cannot know
// where the last checkpoint sits without re-reading the transcript, and a
// wrong offset would skip live history (the exact failure the restore tests
// caught in the first draft). The opportunistic post-full-scan anchor covers
// the shutdown case instead — the resume itself just decoded the whole file
// and knows the true boundary.
func (s *Session) writeCompactionSidecarAnchor() error {
	s.mu.Lock()
	writer := s.transcript
	ready := s.transcriptReady
	s.mu.Unlock()
	if !ready || writer == nil {
		return nil
	}
	return writer.WriteSidecarFromWriter(s.TranscriptPath(), nil, nil, nil, false)
}
