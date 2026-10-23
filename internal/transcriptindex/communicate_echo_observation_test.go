package transcriptindex

import (
	"os"
	"path/filepath"
	"testing"

	"primeradiant.com/evener/agent/provenance"
	"primeradiant.com/evener/appwire"
)

func TestEchoingCommunicateRetainsTerminalEvidence(t *testing.T) {
	t.Parallel()
	for _, outcome := range []string{"accepted", "rejected", "nonterminal"} {
		for _, reopen := range []bool{false, true} {
			t.Run(outcome+map[bool]string{false: "/fresh", true: "/reopened"}[reopen], func(t *testing.T) {
				fx := newFormatFixture(t, "communicate echoes")
				fx.lines = fx.lines[:4]
				comm := &fx.lines[2].turn
				comm.CausalKnown = true
				comm.Causal = provenance.WithWatch(nil, "watch-A", "generation-A", "delivery-A", "source-A", "job-A")
				comm.Communicate.EndTurn = outcome != "nonterminal"
				comm.Communicate.EndReason = "done"
				result := fx.lines[3].turn.Message.Content[0].ToolResult
				result.Content = `{"accepted":true,"end_turn":true}`
				if outcome == "rejected" {
					result.Content = `{"accepted":false,"end_turn":true}`
				}
				if outcome == "nonterminal" {
					result.Content = `{"accepted":true,"end_turn":false}`
				}
				path := writeFixture(t, fx)
				dir := filepath.Join(t.TempDir(), "index")
				if reopen {
					header, lines := fx.encode(t)
					if err := os.WriteFile(path, append(append(header, lines[0]...), lines[1]...), 0600); err != nil {
						t.Fatal(err)
					}
					x := openIndex(t, path, dir)
					if err := x.Close(); err != nil {
						t.Fatal(err)
					}
					f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
					if err != nil {
						t.Fatal(err)
					}
					if _, err := f.Write(append(lines[2], lines[3]...)); err != nil {
						t.Fatal(err)
					}
					if err := f.Close(); err != nil {
						t.Fatal(err)
					}
				}
				x := openIndex(t, path, dir)
				window, err := x.Latest(20)
				if err != nil {
					t.Fatal(err)
				}
				var messages []appwire.ThreadItem
				for _, candidate := range window.Candidates {
					if candidate.Item.Type == "agentMessage" {
						messages = append(messages, candidate.Item)
					}
				}
				if len(messages) != 1 {
					t.Fatalf("visible messages = %d, want one", len(messages))
				}
				item := messages[0]
				if item.Position.Entry != 2 || item.Text != "same words" {
					t.Fatalf("echo changed visible message identity/text: %+v", item)
				}
				obs := item.Observation
				if obs == nil || !obs.CausalKnown || obs.Causal == nil || obs.TurnID != "turn_m20" || item.CallID != "ek1" {
					t.Fatalf("echo lost recorded observation: %+v", item)
				}
				if outcome == "nonterminal" {
					if obs.EndTurn || obs.TerminalAccepted != nil {
						t.Fatalf("nonterminal became completion: %+v", obs)
					}
				} else if obs.TerminalAccepted == nil || *obs.TerminalAccepted != (outcome == "accepted") {
					t.Fatalf("terminal acceptance = %+v", obs)
				}
			})
		}
	}
}
