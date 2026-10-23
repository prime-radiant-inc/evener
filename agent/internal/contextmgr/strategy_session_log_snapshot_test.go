package contextmgr

import (
	"context"
	"fmt"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"primeradiant.com/evener/agent/internal/sessionlog"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
)

const snapshotPartner = "PARTNER_SNAPSHOT_591"
const snapshotEarlier = "EARLIER_SNAPSHOT_702"

func snapshotStrategy(t *testing.T, route string) (*SessionLogStrategy, Strategy, []schema.Turn) {
	t.Helper()
	cm := partnerTestManager(emptySummaryReply)
	cm.CheckpointThreshold, cm.SummarizeThreshold = 0.00001, 2
	s := &SessionLogStrategy{cm: cm, session: &fakeStrategyHost{id: cm.Meta.SessionID, profile: cm.currentProfile()}, log: mustNewSessionLog(t, filepath.Join(t.TempDir(), "log.jsonl"))}
	var strategy Strategy = s
	if route == "ooda" {
		strategy = &OODAStrategy{SessionLogStrategy: s}
	}
	history := []schema.Turn{
		schema.NewTurn(schema.TurnSummary, llm.User(snapshotEarlier)),
		schema.NewTurn(schema.TurnUserInput, llm.User(snapshotPartner)),
		schema.NewTurn(schema.TurnAssistant, llm.Assistant("WORK_SNAPSHOT_813")),
		schema.NewTurn(schema.TurnAssistant, llm.Assistant("TAIL_SNAPSHOT_924")),
		schema.NewTurn(schema.TurnAssistant, llm.Assistant("TAIL_SNAPSHOT_035")),
	}
	return s, strategy, history
}

func snapshotEntry(id int) sessionlog.SessionLogEntry {
	return sessionlog.SessionLogEntry{Turn: id, Action: "assistant", Outcome: "success", Summary: fmt.Sprintf("LOG_SNAPSHOT_%d_146", id)}
}

func snapshotAppend(t *testing.T, s *SessionLogStrategy, entries ...sessionlog.SessionLogEntry) {
	t.Helper()
	for _, entry := range entries {
		if err := s.log.Append(entry); err != nil {
			t.Fatal(err)
		}
	}
}

func snapshotTail(history []schema.Turn) []schema.Turn {
	return append(history, schema.NewTurn(schema.TurnAssistant, llm.Assistant("NEXT_SNAPSHOT_257")), schema.NewTurn(schema.TurnAssistant, llm.Assistant("NEXT_SNAPSHOT_368")))
}

func snapshotFold(t *testing.T, strategy Strategy, history *[]schema.Turn, meta CompactionMeta) string {
	t.Helper()
	if err := strategy.ManageContext(WithCompactionMeta(context.Background(), meta), history, 0, noopEmit); err != nil {
		t.Fatal(err)
	}
	if len(*history) == 0 || (*history)[0].Kind != schema.TurnCheckpoint {
		t.Fatal("checkpoint route did not run")
	}
	return (*history)[0].Message.Text()
}

func snapshotCheckSources(t *testing.T, history []schema.Turn, conversation []checkpointConversationEntry, earlier string) {
	t.Helper()
	data := collectCheckpointData(history, 1, "communicate")
	if !reflect.DeepEqual(data.conversation, conversation) {
		t.Errorf("partner source changed: got %#v, want %#v", data.conversation, conversation)
	}
	if n := strings.Count(strings.Join(data.earlierSummaries, "\n"), earlier); n != 1 {
		t.Errorf("independent earlier source occurs %d times, want once", n)
	}
}

// Navigation changes must not participate in whole-payload coverage. These
// matrices adapt the independent PAR probes, retaining their input-owned oracle.
func TestSessionLogSnapshotPayloadCoverage(t *testing.T) {
	t.Parallel()
	for _, route := range []string{"session-log", "ooda"} {
		for _, mode := range []string{"unchanged", "growing", "restored-prefix", "restored-after-disjoint", "empty-to-entry"} {
			t.Run(route+"/"+mode, func(t *testing.T) {
				s, strategy, history := snapshotStrategy(t, route)
				wantCarry := []checkpointConversationEntry{{Role: "user", Text: snapshotPartner}}
				for fold := range 4 {
					var ids []int
					switch mode {
					case "unchanged":
						ids = []int{1}
					case "growing":
						for id := 1; id <= fold+1; id++ {
							ids = append(ids, id)
						}
					case "restored-prefix":
						ids = []int{2, 3}
						if fold > 0 {
							ids = []int{1, 2, 3}
						}
					case "restored-after-disjoint":
						ids = []int{1, 2}
						if fold == 0 {
							ids = []int{1}
						} else if fold == 1 {
							ids = []int{2}
						}
					case "empty-to-entry":
						if fold > 0 {
							ids = []int{1}
						}
					}
					s.log = mustNewSessionLog(t, filepath.Join(t.TempDir(), "current.jsonl"))
					for _, id := range ids {
						snapshotAppend(t, s, snapshotEntry(id))
					}
					meta := s.cm.Meta
					meta.AvailableTranscriptTools = [][]string{nil, {"read_transcript"}, {"find_session_transcripts"}, {"read_transcript", "find_session_transcripts"}}[fold]
					text := snapshotFold(t, strategy, &history, meta)
					wantSnapshots := 1
					if mode == "restored-after-disjoint" && fold == 1 {
						wantSnapshots = 2
					}
					if n := strings.Count(text, sessionLogCheckpointStart); n != wantSnapshots {
						t.Errorf("fold %d: got %d snapshots, want %d", fold, n, wantSnapshots)
					}
					navigation, ok := soleFencedText(markdownSection(text, "## Session Log Navigation"))
					if !ok {
						t.Fatal("navigation is not canonical fenced text")
					}
					for _, tool := range []string{"read_transcript", "find_session_transcripts"} {
						if strings.Contains(navigation, tool) != slices.Contains(meta.AvailableTranscriptTools, tool) {
							t.Errorf("fold %d: recovery command %s does not match current tools", fold, tool)
						}
					}
					for _, id := range ids {
						if n := strings.Count(text, snapshotEntry(id).Summary); n != 1 {
							t.Errorf("fold %d: covered source %d occurs %d times, want once", fold, id, n)
						}
					}
					snapshotCheckSources(t, history, wantCarry, snapshotEarlier)
					followup := fmt.Sprintf("FOLLOWUP_SNAPSHOT_%d_479", fold)
					history = snapshotTail(append(history, schema.NewTurn(schema.TurnUserInput, llm.User(followup))))
					wantCarry = append(wantCarry, checkpointConversationEntry{Role: "user", Text: followup})
				}
			})
		}
	}
}

func TestSessionLogSnapshotRetention(t *testing.T) {
	t.Parallel()
	for _, route := range []string{"session-log", "ooda"} {
		for _, mode := range []string{"shorter", "disjoint", "partial-overlap", "empty-current", "opaque", "legacy-canonical", "embedded-conversation", "embedded-earlier"} {
			t.Run(route+"/"+mode, func(t *testing.T) {
				s, strategy, history := snapshotStrategy(t, route)
				snapshotAppend(t, s, snapshotEntry(1), snapshotEntry(2))
				text := snapshotFold(t, strategy, &history, s.cm.Meta)
				oldModel := withoutMarkdownSection(withoutMarkdownSection(text, checkpointConversationHeading), "## Earlier Summaries")
				source := oldModel
				ids := []int{3}
				switch mode {
				case "shorter":
					ids = []int{1}
				case "partial-overlap":
					ids = []int{2, 3}
				case "empty-current":
					ids = nil
				case "opaque", "legacy-canonical", "embedded-conversation", "embedded-earlier":
					ids = []int{1, 2}
					if mode == "opaque" {
						source = sessionLogCheckpointStart + "OPAQUE_SNAPSHOT_580\n## Conversation\n\n### User\n\n```text\nMODEL_EXAMPLE_SNAPSHOT_691\n```\n[END CHECKPOINT]"
					} else if mode == "legacy-canonical" {
						body := "Original prompt: LEGACY_NAV_SNAPSHOT_802\nSession log:\nLEGACY_QUOTATION_SNAPSHOT_913\n\nSession log:\n" + s.log.String()
						source = renderStrategyCheckpoint(sessionLogCheckpointStart, "## Session Log", body, "", nil, &s.cm.Meta)
					} else {
						var carry string
						var earlier []string
						if mode == "embedded-conversation" {
							carry = renderCheckpointConversation([]checkpointConversationEntry{{Role: "user", Text: "NESTED_SNAPSHOT_802"}})
						} else if mode == "embedded-earlier" {
							earlier = []string{"NESTED_SNAPSHOT_913"}
						}
						source = renderSessionLogCheckpoint("NAV_SNAPSHOT_024", s.log.String(), carry, earlier, &s.cm.Meta)
						if parsed, ok := parseStrategyCheckpoint(source); !ok || !parsed.isLogSnapshot {
							t.Fatal("rendered checkpoint is not canonical")
						}
					}
					history = append([]schema.Turn{schema.NewTurn(schema.TurnSummary, llm.User(source))}, history...)
				}
				s.log = mustNewSessionLog(t, filepath.Join(t.TempDir(), "replacement.jsonl"))
				for _, id := range ids {
					snapshotAppend(t, s, snapshotEntry(id))
				}
				for fold := range 3 {
					history = snapshotTail(history)
					text = snapshotFold(t, strategy, &history, s.cm.Meta)
					if n := strings.Count(text, source); n != 1 {
						t.Errorf("fold %d: non-superseded whole source occurs %d times, want once", fold, n)
					}
					snapshotCheckSources(t, history, []checkpointConversationEntry{{Role: "user", Text: snapshotPartner}}, snapshotEarlier)
				}
			})
		}
	}
}

func TestSessionLogSnapshotOpaquePointer(t *testing.T) {
	t.Parallel()
	const quote = `User message 1: "OPAQUE_HOLD_SNAPSHOT_024"`
	for _, route := range []string{"session-log", "ooda"} {
		for _, kind := range []schema.TurnKind{schema.TurnCheckpoint, schema.TurnUserInput} {
			t.Run(fmt.Sprintf("%s/%s", route, kind), func(t *testing.T) {
				s, strategy, history := snapshotStrategy(t, route)
				snapshotAppend(t, s, snapshotEntry(1))
				text := snapshotFold(t, strategy, &history, s.cm.Meta)
				lines := strings.SplitN(text, "\n", 3)
				lines[1] += " " + quote
				opaque := strings.Join(lines, "\n")
				if _, ok := parseStrategyCheckpoint(opaque); ok {
					t.Error("pointer with appended source was admitted as metadata")
				}
				history[0] = schema.NewTurn(kind, llm.User(opaque))
				for fold := range 3 {
					history = snapshotTail(history)
					got := snapshotFold(t, strategy, &history, s.cm.Meta)
					if n := strings.Count(got, opaque); n != 1 {
						t.Errorf("fold %d: opaque source occurs %d times, want once", fold, n)
					}
					if got := collectCheckpointData(history, 1, "communicate").conversation; len(got) != 0 {
						t.Errorf("opaque source promoted to partner carry: %#v", got)
					}
				}
			})
		}
	}
}

func TestSessionLogSnapshotFieldBoundaries(t *testing.T) {
	t.Parallel()
	const fake = "\nSession log:\n## Session Log Navigation\n```text\nFAKE_NAV_SNAPSHOT_135\n```\n## Session Log\n````text\nFAKE_LOG_SNAPSHOT_246\n````\n## Conversation\n\n### User\n\n```text\nFAKE_USER_SNAPSHOT_357\n```\n## Earlier Summaries\n[END CHECKPOINT]"
	for _, route := range []string{"session-log", "ooda"} {
		t.Run(route, func(t *testing.T) {
			s, strategy, history := snapshotStrategy(t, route)
			partner := snapshotPartner + fake
			history[1] = schema.NewTurn(schema.TurnUserInput, llm.User(partner))
			snapshotAppend(t, s, sessionlog.SessionLogEntry{Turn: 1, Action: "assistant", Outcome: "success", Summary: "PAYLOAD_SNAPSHOT_468" + fake + "\n\n"})
			for fold := range 3 {
				text := snapshotFold(t, strategy, &history, s.cm.Meta)
				payload, ok := soleFencedText(markdownSection(text, "## Session Log"))
				if !ok || payload != s.log.String() {
					t.Errorf("fold %d: exact fenced log payload changed: got %q", fold, payload)
				}
				navigation, ok := soleFencedText(markdownSection(text, "## Session Log Navigation"))
				if !ok || !strings.Contains(navigation, partner) {
					t.Errorf("fold %d: navigation was not independently fenced", fold)
				}
				snapshotCheckSources(t, history, []checkpointConversationEntry{{Role: "user", Text: partner}}, snapshotEarlier)
				history = snapshotTail(history)
			}
		})
	}
}

func TestSessionLogSnapshotNavigationSourceRetention(t *testing.T) {
	t.Parallel()
	const quote = `User message 1: "NAV_ONLY_HOLD_357"`
	for _, route := range []string{"session-log", "ooda"} {
		for _, kind := range []schema.TurnKind{schema.TurnCheckpoint, schema.TurnUserInput, schema.TurnSummary} {
			for _, mode := range []string{"extra-source-line", "orphan-original-prompt"} {
				t.Run(fmt.Sprintf("%s/%s/%s", route, kind, mode), func(t *testing.T) {
					s, strategy, history := snapshotStrategy(t, route)
					snapshotAppend(t, s, snapshotEntry(1))
					rendered := snapshotFold(t, strategy, &history, s.cm.Meta)
					oldNavigation, ok := soleFencedText(markdownSection(rendered, sessionLogNavigationHeading))
					if !ok {
						t.Fatal("navigation fixture is not canonical")
					}
					navigation := oldNavigation + quote + "\n"
					if mode == "orphan-original-prompt" {
						_, recovery, found := strings.Cut(oldNavigation, "\n")
						if !found {
							t.Fatal("navigation fixture has no prompt line")
						}
						navigation = "Original prompt: " + quote + "\n" + recovery
					}
					source := strings.Replace(rendered,
						renderCompactionModelText(sessionLogNavigationHeading, oldNavigation+"\n"),
						renderCompactionModelText(sessionLogNavigationHeading, navigation+"\n"), 1)
					if strings.Count(source, quote) != 1 {
						t.Fatal("navigation source fixture is not unique")
					}
					if kind == schema.TurnSummary {
						source = withoutMarkdownSection(withoutMarkdownSection(source, checkpointConversationHeading), "## Earlier Summaries")
						history = append([]schema.Turn{schema.NewTurn(kind, llm.User(source))}, history...)
					} else {
						history[0] = schema.NewTurn(kind, llm.User(source))
					}
					for fold := range 4 {
						history = snapshotTail(history)
						text := snapshotFold(t, strategy, &history, s.cm.Meta)
						if !strings.Contains(text, quote) {
							t.Fatalf("fold %d: navigation quotation lost", fold)
						}
						if n := strings.Count(text, snapshotEntry(1).Summary); n != 1 {
							t.Errorf("fold %d: covered payload occurs %d times, want once", fold, n)
						}
						snapshotCheckSources(t, history, []checkpointConversationEntry{{Role: "user", Text: snapshotPartner}}, snapshotEarlier)
					}
				})
			}
		}
	}
}

func TestSessionLogSnapshotNavigationAfterBudgetedFold(t *testing.T) {
	t.Parallel()
	const quote = `User message 1: "NAV_SOLE_SOURCE_468"`
	legacy := sessionLogCheckpointStart + "Original prompt: " + quote + "\nSession log:\n" + strings.Repeat("legacy detail ", 6000) + "\n[END CHECKPOINT]"
	for _, route := range []string{"session-log", "ooda"} {
		for _, middle := range []string{"checkpoint", "obs-mask"} {
			t.Run(route+"/"+middle, func(t *testing.T) {
				s, strategy, history := snapshotStrategy(t, route)
				snapshotAppend(t, s, snapshotEntry(1))
				history = append([]schema.Turn{schema.NewTurn(schema.TurnUserInput, llm.User(legacy))}, history[2:]...)
				first := snapshotFold(t, strategy, &history, s.cm.Meta)
				if !strings.Contains(first, legacy) {
					t.Fatal("first fold did not keep opaque source whole")
				}
				navigation, ok := soleFencedText(markdownSection(first, sessionLogNavigationHeading))
				if !ok || !strings.Contains(navigation, quote) {
					t.Fatal("real renderer did not copy the legacy source into navigation")
				}
				history = snapshotTail(history)
				if middle == "checkpoint" {
					history = checkpoint(history, s.cm.PreserveRecentTurns, &s.cm.Meta, "communicate")
				} else if err := NewObsMaskStrategy(s.cm).ManageContext(context.Background(), &history, 0, noopEmit); err != nil {
					t.Fatal(err)
				}
				intermediate := history[0].Message.Text()
				if strings.Contains(intermediate, legacy) || strings.Count(intermediate, quote) != 1 {
					t.Fatal("intermediate did not shed the huge source while retaining its sole navigation quote")
				}
				for fold := range 4 {
					history = snapshotTail(history)
					text := snapshotFold(t, strategy, &history, s.cm.Meta)
					if n := strings.Count(text, quote); n != 1 {
						t.Fatalf("fold %d: sole navigation quote occurs %d times, want once", fold, n)
					}
					if n := strings.Count(text, snapshotEntry(1).Summary); n != 1 {
						t.Errorf("fold %d: covered payload occurs %d times, want once", fold, n)
					}
					if data := collectCheckpointData(history, 1, "communicate"); len(data.conversation) != 0 {
						t.Errorf("navigation promoted to partner carry: %#v", data.conversation)
					}
				}
			})
		}
	}
}

func TestSessionLogSnapshotHistoricalNavigationBoundaries(t *testing.T) {
	t.Parallel()
	fields := []string{"", "NAV_579", "NAV_579\n", "NAV_579\n\n", " \n\t\n", "NAV_579\r\n", "\n## Conversation\n```text\nMODEL_ONLY_680\n```\n## Earlier Summaries\n[END CHECKPOINT]\n"}
	for i, navigation := range fields {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			const payload = "ACTUAL_PAYLOAD_791"
			source := renderSessionLogCheckpoint(navigation, payload, "", nil, nil)
			if parsed, ok := parseStrategyCheckpoint(source); !ok || parsed.navigation != navigation {
				t.Fatal("parser did not return the exact navigation field")
			}
			kept := sessionLogEarlierContext([]string{source}, payload)
			if len(kept) != 1 {
				t.Fatalf("got %d historical sources, want one", len(kept))
			}
			got, ok := soleFencedText(markdownSection(kept[0], "## Historical Session Log Navigation"))
			if !ok || got != navigation {
				t.Fatalf("historical navigation changed: got %q, want %q", got, navigation)
			}
			history := []schema.Turn{schema.NewTurn(schema.TurnCheckpoint, llm.User(renderSessionLogCheckpoint("LIVE_802", payload, "", kept, nil)))}
			if data := collectCheckpointData(history, 1, "communicate"); len(data.conversation) != 0 {
				t.Errorf("historical navigation promoted to partner carry: %#v", data.conversation)
			}
			const independent = "INDEPENDENT_SOURCE_913"
			withDuplicates := []string{independent, source, independent, kept[0]}
			want := []string{independent, independent, kept[0]}
			if got := sessionLogEarlierContext(withDuplicates, payload); !reflect.DeepEqual(got, want) {
				t.Errorf("independent sources reordered or collapsed: got %#v, want %#v", got, want)
			}
			if got := sessionLogEarlierContext([]string{source, source}, payload); !reflect.DeepEqual(got, kept) {
				t.Errorf("identical generated navigation accumulated: %#v", got)
			}
			for _, route := range []string{"session-log", "ooda"} {
				t.Run(route, func(t *testing.T) {
					s, strategy, history := snapshotStrategy(t, route)
					snapshotAppend(t, s, snapshotEntry(1))
					source := renderSessionLogCheckpoint(navigation, s.log.String(), "", nil, nil)
					history = append([]schema.Turn{schema.NewTurn(schema.TurnSummary, llm.User(source))}, history...)
					for fold := range 3 {
						snapshotFold(t, strategy, &history, s.cm.Meta)
						matches := 0
						for _, summary := range collectCheckpointData(history, 1, "communicate").earlierSummaries {
							field, ok := soleFencedText(markdownSection(summary, "## Historical Session Log Navigation"))
							if ok && field == navigation {
								matches++
							}
						}
						if matches != 1 {
							t.Errorf("fold %d: exact historical navigation occurs %d times, want once", fold, matches)
						}
						snapshotCheckSources(t, history, []checkpointConversationEntry{{Role: "user", Text: snapshotPartner}}, snapshotEarlier)
						history = snapshotTail(history)
					}
				})
			}
		})
	}
}

func TestSessionLogSnapshotUnchangedNavigationDoesNotGrow(t *testing.T) {
	t.Parallel()
	for _, route := range []string{"session-log", "ooda"} {
		t.Run(route, func(t *testing.T) {
			s, strategy, history := snapshotStrategy(t, route)
			snapshotAppend(t, s, snapshotEntry(1))
			var previous string
			for fold := range 5 {
				text := snapshotFold(t, strategy, &history, s.cm.Meta)
				if fold > 1 && text != previous {
					t.Errorf("fold %d: unchanged navigation grew or changed: previous=%d bytes, current=%d bytes", fold, len(previous), len(text))
				}
				previous = text
				history = snapshotTail(history)
			}
		})
	}
}
