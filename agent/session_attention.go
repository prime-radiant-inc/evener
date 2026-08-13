package agent

import (
	"errors"
	"fmt"

	"primeradiant.com/serf/agent/transcript"
)

// resolveAttentionDurably appends attention markers through the one writer
// already owned by a resident Session. A post-append fold proves the marker is
// durable because transcript.Writer deliberately treats a closed writer as a
// successful no-op.
func (s *Session) resolveAttentionDurably(ids []string, disposition delegateAttentionResolution) error {
	if s == nil {
		return errors.New("attention resolution session is nil")
	}
	s.attentionMu.Lock()
	defer s.attentionMu.Unlock()

	s.mu.Lock()
	ready := s.transcriptReady
	writer := s.transcript
	sessionID := s.id
	stateDir := s.stateDir
	s.mu.Unlock()
	if !ready || writer == nil || sessionID == "" || stateDir == "" {
		return errors.New("attention resolution requires an attached transcript writer")
	}
	path := transcriptPath(stateDir, sessionID)
	fold, err := readDelegateAttentionFold(path, sessionID)
	if err != nil {
		return err
	}
	if err := appendDelegateAttentionResolutions(writer, fold, ids, disposition); err != nil {
		return err
	}
	verified, err := readDelegateAttentionFold(path, sessionID)
	if err != nil {
		return err
	}
	for _, attentionID := range ids {
		if verified.resolutions[attentionID] != disposition {
			return fmt.Errorf("attention %q resolution was not durably appended", attentionID)
		}
	}
	return nil
}

func appendDelegateAttentionResolutions(writer *transcript.Writer, fold delegateAttentionFold, ids []string, disposition delegateAttentionResolution) error {
	if disposition != delegateAttentionConsumed && disposition != delegateAttentionDiscarded {
		return fmt.Errorf("invalid attention resolution %q", disposition)
	}
	for _, attentionID := range ids {
		if attentionID == "" {
			return errors.New("attention resolution ID is empty")
		}
		if previous, resolved := fold.resolutions[attentionID]; resolved {
			if previous != disposition {
				return fmt.Errorf("attention %q has conflicting resolution %q", attentionID, previous)
			}
			continue
		}
		if _, pending := fold.content[attentionID]; !pending {
			return fmt.Errorf("attention %q is not pending", attentionID)
		}
		if err := writer.AppendDurable(delegateAttentionResolutionTurn(attentionID, disposition)); err != nil {
			return err
		}
		fold.resolutions[attentionID] = disposition
	}
	return nil
}
