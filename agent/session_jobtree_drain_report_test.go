package agent

import (
	"context"
	"errors"
	"testing"
)

// inputRunning is the comm-level effect of one input on a session: it starts
// with a fresh communicate result, and a terminal communicate then fills it.
func inputRunning(s *Session, reply string) {
	s.mu.Lock()
	s.comm = communicateResult{}
	s.mu.Unlock()
	if reply != "" {
		s.acceptCommunicateTerminal(context.Background(), reply, reply, reply, nil, "")
	}
}

// One drain call can run several inputs. A report accepted by an earlier input
// of the call survives a later input that reports nothing.
func TestDrainTurnKeepsTheLatestAcceptedReport(t *testing.T) {
	t.Parallel()
	s := newSession(t, withoutGitSnapshot())
	inputRunning(s, "REPORT_A")
	output := map[string]any{"message": "SUMMARY_B", "data": map[string]any{"generation": 2}}
	process := s.keepingReport(func(context.Context, string, []ImageAttachment, EntryKind) (string, error) {
		inputRunning(s, "")
		if !s.acceptCommunicateTerminal(context.Background(), "REPORT_B", "REPLY_B", "OUTPUT_B", output, "") {
			t.Fatal("report B was not accepted")
		}
		if s.acceptCommunicateTerminal(context.Background(), "LOSING_REPORT", "LOSING_REPLY", "LOSING_OUTPUT", map[string]any{"lost": true}, "") {
			t.Fatal("competing report was accepted")
		}
		inputRunning(s, "")
		return "", errBareTextWithoutResultTool
	})
	_, err := process(context.Background(), "", nil, EntryNotification)
	if err != nil {
		t.Fatalf("the housekeeping failure of a turn without a report propagated: %v", err)
	}
	if reply, reported := s.communicatedReply(); !reported || reply != "REPLY_B" || s.CommunicateOutput() != "OUTPUT_B" {
		t.Fatalf("reply=%q reported=%v output=%q, want report B restored", reply, reported, s.CommunicateOutput())
	}
	assertDelegateReportMessage(t, buildDelegateFinish(s, "REPLY_B", nil), "REPORT_B", output)
	assertDelegateReportMessage(t, (&subagent{sess: s}).buildDelegateFinish("REPLY_B", nil), "REPORT_B", output)
}

// Only the failure of a turn that reported nothing is swallowed: an unrelated
// error joined with it still propagates.
func TestDrainTurnWithoutAReportKeepsOtherErrors(t *testing.T) {
	t.Parallel()
	journalFailure := errors.New("mutation completion journal failed")
	for name, turnErr := range map[string]error{
		"joined with a bare-text failure": errors.Join(errBareTextWithoutResultTool, journalFailure),
		"unrelated":                       journalFailure,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			s := newSession(t, withoutGitSnapshot())
			inputRunning(s, "REPORT_A")
			process := s.keepingReport(func(context.Context, string, []ImageAttachment, EntryKind) (string, error) {
				inputRunning(s, "")
				return "", turnErr
			})
			if _, err := process(context.Background(), "", nil, EntryNotification); !errors.Is(err, journalFailure) {
				t.Fatalf("error = %v, want the journal failure to propagate", err)
			}
			if s.CommunicateOutput() != "REPORT_A" {
				t.Fatalf("output = %q, want report A kept", s.CommunicateOutput())
			}
		})
	}
}
