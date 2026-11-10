package transcriptindex

import (
	"encoding/json"
	"fmt"
	"reflect"
	"testing"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/internal/apptranscript"
)

func TestToolSettlementPreservesAuthorship(t *testing.T) {
	t.Parallel()
	for _, format := range []int{0, schema.TurnFormatIdentity} {
		for _, author := range []struct {
			name string
			from *schema.TranscriptAuthorship
			want *appwire.TranscriptAuthorship
		}{
			{
				name: "inherited-agent",
				from: &schema.TranscriptAuthorship{Author: schema.TranscriptAuthor{Kind: schema.TranscriptAuthorAgent, SessionID: "origin", OwnerSessionID: "parent", DelegateID: "dlg_origin", DelegateName: "Origin"}, Inherited: true},
				want: &appwire.TranscriptAuthorship{Author: appwire.TranscriptAuthor{Kind: appwire.TranscriptAuthorAgent, SessionRef: "local:origin", OwnerRef: "local:parent", DelegateID: "dlg_origin", DelegateName: "Origin"}, Inherited: true},
			},
			{
				name: "inherited-unknown",
				from: &schema.TranscriptAuthorship{Author: schema.TranscriptAuthor{Kind: schema.TranscriptAuthorUnknown}, Inherited: true},
				want: &appwire.TranscriptAuthorship{Author: appwire.TranscriptAuthor{Kind: appwire.TranscriptAuthorUnknown}, Inherited: true},
			},
			{name: "absent"},
		} {
			for _, outcome := range []string{"failed", "image"} {
				t.Run(fmt.Sprintf("format-%d/%s/%s", format, author.name, outcome), func(t *testing.T) {
					input := user("input sentinel")
					invocation := assistant(call("call_tool", "read_file", `{"file_path":"fixture.png"}`))
					invocation.Authorship = author.from
					part := result("call_tool", "read_file", "result sentinel")
					if outcome == "failed" {
						part.ToolResult.IsError = true
						part.ToolResult.ToolState = json.RawMessage(`{"exit_code":1}`)
					} else {
						part.ToolResult.ImageData = pngBytes
						part.ToolResult.ImageMediaType = "image/png"
					}
					settlement := results(part) // Ordinary results carry no author of their own.
					if format == schema.TurnFormatIdentity {
						input = opens("copied", schema.TurnSpanExecution, input)
						invocation = inRound(inTurn("copied", invocation), "round_origin")
						settlement = inRound(inTurn("copied", settlement), "round_origin")
					}
					path, lines := writeHeaderOnly(t, fixture{header: transcript.Header{SessionID: "leaf"}, lines: []fixtureLine{
						entryLine(input), entryLine(invocation), entryLine(settlement),
					}})
					appendBytes(t, path, lines[0])
					appendBytes(t, path, lines[1])
					dir := t.TempDir()
					x := openIndex(t, path, dir)

					check := func(t *testing.T, item appwire.ThreadItem, settled bool) {
						t.Helper()
						if item.Type != "commandExecution" || item.CallID != "call_tool" || item.ToolName != "read_file" {
							t.Fatalf("tool identity = %+v", item)
						}
						wantStatus := appwire.TurnStatusInProgress
						if settled {
							wantStatus = appwire.TurnStatusCompleted
							if outcome == "failed" {
								wantStatus = appwire.TurnStatusFailed
								if item.ExitCode == nil || *item.ExitCode != 1 {
									t.Fatalf("failed result missing exit code: %+v", item)
								}
							} else if len(item.OutputImages) != 1 || item.OutputImages[0].MediaType != "image/png" {
								t.Fatalf("settled result missing image: %+v", item)
							}
						}
						if item.Status != wantStatus {
							t.Fatalf("status = %s, want %s", item.Status, wantStatus)
						}
						if !reflect.DeepEqual(item.Authorship, author.want) {
							t.Errorf("settled=%v authorship=%+v, want %+v", settled, item.Authorship, author.want)
						}
					}
					read := func(t *testing.T, settled bool) {
						t.Helper()
						t.Run("full", func(t *testing.T) {
							reg := apptranscript.NewToolCallRegistry()
							turns, err := apptranscript.ItemTurnsFromFile(path, 128<<20, func(turn schema.Turn, turnID string, entryIndex int) []appwire.ThreadItem {
								items, _ := apptranscript.ProjectEntryParts(turnID, entryIndex, turn, reg, nil, apptranscript.ToolResultOutputImages)
								return items
							})
							if err != nil {
								t.Fatal(err)
							}
							if len(turns) != 1 || len(turns[0].Items) != 2 {
								t.Fatalf("full history duplicated or lost an item: %+v", turns)
							}
							check(t, turns[0].Items[1], settled)
						})
						t.Run("indexed", func(t *testing.T) {
							window, err := x.Latest(1)
							if err != nil {
								t.Fatal(err)
							}
							if len(window.Candidates) != 1 || !window.HasOlder {
								t.Fatalf("bounded history = %+v", window)
							}
							check(t, window.Candidates[0].Item, settled)
						})
					}
					t.Run("open", func(t *testing.T) { read(t, false) })
					// Resume from persisted opener state, then settle the same call.
					if err := x.Close(); err != nil {
						t.Fatal(err)
					}
					x = openIndex(t, path, dir)
					appendBytes(t, path, lines[2])
					catchUp(t, x)
					t.Run("settled", func(t *testing.T) { read(t, true) })
					if err := x.Close(); err != nil {
						t.Fatal(err)
					}
					x = openIndex(t, path, dir)
					t.Run("reopened", func(t *testing.T) { read(t, true) })
				})
			}
		}
	}
}
