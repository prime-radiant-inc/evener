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

func TestEchoingCommunicateBatchRetainsAcceptedWinner(t *testing.T) {
	t.Parallel()
	for _, firstWins := range []bool{false, true} {
		for _, reopen := range []bool{false, true} {
			t.Run(map[bool]string{false: "nonterminal-then-terminal", true: "terminal-then-rejected"}[firstWins]+map[bool]string{false: "/fresh", true: "/reopened"}[reopen], func(t *testing.T) {
				fx := newFormatFixture(t, "communicate echoes")
				fx.lines = fx.lines[:4]
				firstArgs := `{"message":"same words","end_turn":false,"output":{"message":"","data":{},"artifacts":[]}}`
				terminalArgs := `{"message":"same words","end_turn":true,"output":{"message":"","data":{},"artifacts":[]}}`
				if firstWins {
					firstArgs = terminalArgs
				}
				fx.lines[1].turn.Message.Content[2] = call("ek1", "communicate", firstArgs)
				fx.lines[1].turn.Message.Content = append(fx.lines[1].turn.Message.Content, call("ek2", "communicate", terminalArgs))
				fx.lines[2].turn.CausalKnown = true
				fx.lines[2].turn.Communicate.EndTurn = firstWins
				fx.lines[2].turn.Communicate.EndReason = "done"
				second := fx.lines[2]
				communication := *second.turn.Communicate
				communication.CallID, communication.EndTurn = "ek2", true
				second.turn.Communicate = &communication
				verdict := fx.lines[3]
				firstResult, secondResult := `{"accepted":true,"end_turn":false}`, `{"accepted":true,"end_turn":true}`
				winner := "ek2"
				if firstWins {
					firstResult, secondResult = `{"accepted":true,"end_turn":true}`, `{"accepted":false,"end_turn":true}`
					winner = "ek1"
				}
				verdict.turn.Message.Content = append(verdict.turn.Message.Content[:0:0], result("ek1", "communicate", firstResult), result("ek2", "communicate", secondResult))
				fx.lines = append(fx.lines[:3], second, verdict)
				path := writeFixture(t, fx)
				dir := filepath.Join(t.TempDir(), "index")
				if reopen {
					header, lines := fx.encode(t)
					prefix := append([]byte{}, header...)
					for _, line := range lines[:4] {
						prefix = append(prefix, line...)
					}
					if err := os.WriteFile(path, prefix, 0600); err != nil {
						t.Fatal(err)
					}
					x := openIndex(t, path, dir)
					if err := x.Close(); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(path, append(prefix, lines[4]...), 0600); err != nil {
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
				if item.Text != "same words" || item.Position.Entry != 2 || item.CallID != winner || item.Observation == nil || !item.Observation.EndTurn || item.Observation.TerminalAccepted == nil || !*item.Observation.TerminalAccepted {
					t.Fatalf("echo lost accepted winner %s: %+v observation=%+v", winner, item, item.Observation)
				}
			})
		}
	}
}
