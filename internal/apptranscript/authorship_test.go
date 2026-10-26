package apptranscript

import (
	"encoding/json"
	"fmt"
	"reflect"
	"testing"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/llm"
)

func assertAuthorJSON(t *testing.T, item appwire.ThreadItem, want string) {
	t.Helper()
	b, err := json.Marshal(item)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(b, &fields); err != nil {
		t.Fatal(err)
	}
	var got, expected any
	if len(fields["authorship"]) != 0 {
		if err := json.Unmarshal(fields["authorship"], &got); err != nil {
			t.Fatal(err)
		}
	}
	if err := json.Unmarshal([]byte(want), &expected); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, expected) {
		t.Errorf("%s %q authorship=%s, want %s", item.ID, item.Text, fields["authorship"], want)
	}
}

func TestAuthorshipProjection(t *testing.T) {
	author := &schema.TranscriptAuthorship{Author: schema.TranscriptAuthor{Kind: schema.TranscriptAuthorAgent, SessionID: "parent", OwnerSessionID: "root", DelegateID: "dlg_parent", DelegateName: "Parent"}, Inherited: true}
	const want = `{"author":{"kind":"agent","sessionRef":"local:parent","ownerRef":"local:root","delegateId":"dlg_parent","delegateName":"Parent"},"inherited":true}`
	for _, format := range []int{0, schema.TurnFormatIdentity} {
		for _, kind := range []schema.TurnKind{schema.TurnUserInput, schema.TurnAssistant} {
			turn := schema.NewTurn(kind, llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{{Kind: llm.ContentText, Text: "answer"}, {Kind: llm.ContentThinking, Thinking: &llm.ThinkingData{Text: "thought"}}}})
			turn.Format = format
			turn.Authorship = author
			items, parts := ProjectEntryParts("turn", 4, turn, nil, nil, nil)
			if len(items) == 0 || len(parts) != len(items) {
				t.Fatalf("items=%v parts=%v", items, parts)
			}
			for _, item := range items {
				assertAuthorJSON(t, item, want)
				if item.TurnID != "turn" {
					t.Fatal(item)
				}
			}
		}
	}
	for _, tc := range []struct {
		a    *schema.TranscriptAuthorship
		want string
	}{
		{nil, `null`},
		{&schema.TranscriptAuthorship{Author: schema.TranscriptAuthor{Kind: schema.TranscriptAuthorHuman}}, `{"author":{"kind":"human"},"inherited":false}`},
		{&schema.TranscriptAuthorship{Author: schema.TranscriptAuthor{Kind: schema.TranscriptAuthorAgent}, Inherited: true}, `{"author":{"kind":"unknown"},"inherited":true}`},
	} {
		turn := schema.NewTurn(schema.TurnUserInput, llm.User("input"))
		turn.Authorship = tc.a
		items := ProjectTurn("turn", 1, turn, nil, nil, nil)
		assertAuthorJSON(t, items[0], tc.want)
	}
}

func TestAuthorshipCommunicateWindow(t *testing.T) {
	for _, pending := range []bool{false, true} {
		reg := NewToolCallRegistry()
		call := schema.NewTurn(schema.TurnAssistant, llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{ID: "call", Name: "communicate", Arguments: json.RawMessage(`{"message":"delivered"}`)}}}})
		call.Authorship = &schema.TranscriptAuthorship{Author: schema.TranscriptAuthor{Kind: schema.TranscriptAuthorAgent, SessionID: "parent"}, Inherited: true}
		ProjectTurn("turn", 1, call, reg, nil, nil)
		var items []appwire.ThreadItem
		if pending {
			items = flushUnpairedCommunicateItems(reg, "turn")
		} else {
			result := schema.NewTurn(schema.TurnToolResults, llm.Message{Content: []llm.ContentPart{{Kind: llm.ContentToolResult, ToolResult: &llm.ToolResultData{ToolCallID: "call"}}}})
			result.Authorship = &schema.TranscriptAuthorship{Author: schema.TranscriptAuthor{Kind: schema.TranscriptAuthorAgent, SessionID: "child"}}
			items = ProjectTurn("turn", 2, result, reg, nil, nil)
		}
		if len(items) != 1 || items[0].Text != "delivered" || items[0].CallID != "call" {
			t.Fatalf("items=%+v", items)
		}
		assertAuthorJSON(t, items[0], `{"author":{"kind":"agent","sessionRef":"local:parent"},"inherited":true}`)
	}
}

func TestInheritedCommunicateEntryParts(t *testing.T) {
	reg := NewToolCallRegistry()
	call := schema.Turn{Format: schema.TurnFormatIdentity, Kind: schema.TurnAssistant, TurnID: "copied", RoundID: "round_child",
		Authorship: &schema.TranscriptAuthorship{Author: schema.TranscriptAuthor{Kind: schema.TranscriptAuthorAgent, SessionID: "child", OwnerSessionID: "parent", DelegateID: "dlg_child", DelegateName: "Child"}, Inherited: true},
		Message:    llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{ID: "call_child", Name: "communicate", Arguments: json.RawMessage(`{"message":"copied child update"}`)}}}}}
	items, parts := ProjectEntryParts("copied", 4, call, reg, nil, nil)
	if len(items) != 0 || len(parts) != 0 {
		t.Fatalf("call should defer: items=%+v parts=%v", items, parts)
	}
	result := schema.Turn{Format: schema.TurnFormatIdentity, Kind: schema.TurnToolResults, TurnID: "copied", RoundID: "round_child",
		Authorship: &schema.TranscriptAuthorship{Author: schema.TranscriptAuthor{Kind: schema.TranscriptAuthorUnknown}, Inherited: true},
		Message:    llm.Message{Role: llm.RoleTool, Content: []llm.ContentPart{{Kind: llm.ContentToolResult, ToolResult: &llm.ToolResultData{ToolCallID: "call_child", Name: "communicate", Content: "ok"}}}}}
	items, parts = ProjectEntryParts("copied", 5, result, reg, nil, nil)
	if len(items) != 1 {
		t.Fatalf("missing inherited communicate: items=%+v parts=%v", items, parts)
	}
	item := items[0]
	if item.Type != "agentMessage" || item.Text != "copied child update" || item.CallID != "call_child" || item.ID != "item_assistant_5_0" || item.TurnID != "copied" || item.RoundID != "round_child" || !reflect.DeepEqual(parts, []int{0}) {
		t.Fatalf("result identity/content: item=%+v parts=%v", item, parts)
	}
	assertAuthorJSON(t, item, `{"author":{"kind":"agent","sessionRef":"local:child","ownerRef":"local:parent","delegateId":"dlg_child","delegateName":"Child"},"inherited":true}`)
}

func TestInheritedCommunicateOutcomes(t *testing.T) {
	for _, format := range []int{0, schema.TurnFormatIdentity} {
		for _, outcome := range []string{"named", "nameless", "unpaired", "rejected", "runtime-error", "echo", "cross-turn"} {
			t.Run(fmt.Sprintf("format-%d/%s", format, outcome), func(t *testing.T) {
				reg := NewToolCallRegistry()
				call := schema.Turn{Format: format, Kind: schema.TurnAssistant,
					Authorship: &schema.TranscriptAuthorship{Author: schema.TranscriptAuthor{Kind: schema.TranscriptAuthorAgent, SessionID: "origin"}, Inherited: true},
					Message:    llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{ID: "c", Name: "communicate", Arguments: json.RawMessage(`{"message":"payload sentinel"}`)}}}}}
				if outcome == "echo" || outcome == "cross-turn" {
					// Identity text is a maximal run, not the final content part.
					text := []llm.ContentPart{{Kind: llm.ContentText, Text: "payload sentinel"}}
					if format == schema.TurnFormatIdentity {
						text = []llm.ContentPart{{Kind: llm.ContentText, Text: "payload "}, {Kind: llm.ContentText, Text: "sentinel"}}
					}
					prior := schema.Turn{Format: format, Kind: schema.TurnAssistant, Message: llm.Message{Role: llm.RoleAssistant, Content: text}}
					ProjectEntryParts("turn", 1, prior, reg, nil, nil)
				}
				ProjectEntryParts("turn", 2, call, reg, nil, nil)
				// Pending attribution is a snapshot, not an alias of the source.
				call.Authorship.Author.SessionID = "mutated"
				var items []appwire.ThreadItem
				if outcome == "unpaired" {
					items = flushUnpairedCommunicateItems(reg, "turn")
				} else {
					name := "communicate"
					if outcome == "nameless" {
						name = ""
					}
					result := schema.Turn{Format: format, Kind: schema.TurnToolResults,
						Authorship: &schema.TranscriptAuthorship{Author: schema.TranscriptAuthor{Kind: schema.TranscriptAuthorUnknown}, Inherited: true},
						Message:    llm.Message{Role: llm.RoleTool, Content: []llm.ContentPart{{Kind: llm.ContentToolResult, ToolResult: &llm.ToolResultData{ToolCallID: "c", Name: name, Content: "result sentinel", IsError: outcome == "rejected" || outcome == "runtime-error", PrevalOnly: outcome == "rejected"}}}}}
					turnID := "turn"
					if outcome == "cross-turn" {
						turnID = "next"
					}
					items, _ = ProjectEntryParts(turnID, 3, result, reg, nil, nil)
					if len(reg.CommRawArgs) != 0 || len(reg.CommAuthorship) != 0 || len(flushUnpairedCommunicateItems(reg, turnID)) != 0 {
						t.Fatal("paired result left a pending duplicate")
					}
				}
				if outcome == "echo" || outcome == "runtime-error" {
					if len(items) != 0 {
						t.Fatalf("suppressed result projected %+v", items)
					}
					return
				}
				if len(items) != 1 || items[0].CallID != "c" {
					t.Fatalf("items=%+v", items)
				}
				item := items[0]
				assertAuthorJSON(t, item, `{"author":{"kind":"agent","sessionRef":"local:origin"},"inherited":true}`)
				if outcome == "rejected" {
					if item.Type != "commandExecution" || item.Status != appwire.TurnStatusFailed || !item.PrevalOnly || item.Error != "result sentinel" || item.ArgumentsJSON != `{"message":"payload sentinel"}` {
						t.Fatalf("rejected item=%+v", item)
					}
				} else if item.Type != "agentMessage" || item.Text != "payload sentinel" || item.Status != appwire.TurnStatusCompleted {
					t.Fatalf("message=%+v", item)
				}
			})
		}
	}
}

func TestInheritedCommunicateFreshAndFoldExclusion(t *testing.T) {
	for _, kind := range []string{"fresh", "absent-author", "fold"} {
		t.Run(kind, func(t *testing.T) {
			reg := NewToolCallRegistry()
			call := schema.Turn{Format: schema.TurnFormatIdentity, Kind: schema.TurnAssistant,
				Authorship: &schema.TranscriptAuthorship{Author: schema.TranscriptAuthor{Kind: schema.TranscriptAuthorAgent, SessionID: "origin"}},
				Message:    llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{ID: "c", Name: "communicate", Arguments: json.RawMessage(`{"message":"fresh sentinel"}`)}}}}}
			if kind == "absent-author" {
				call.Authorship = nil
			} else if kind == "fold" {
				original := uint64(0)
				call.OriginalOrdinal = &original
				call.Authorship.Inherited = true
			}
			items, _ := ProjectEntryParts("turn", 1, call, reg, nil, nil)
			if len(items) != 0 || len(reg.CommRawArgs) != 0 || len(flushUnpairedCommunicateItems(reg, "turn")) != 0 {
				t.Fatalf("non-reconstructable call projected/deferred: items=%+v registry=%+v", items, reg)
			}
			result := schema.Turn{Format: schema.TurnFormatIdentity, Kind: schema.TurnToolResults, Message: llm.Message{Content: []llm.ContentPart{{Kind: llm.ContentToolResult, ToolResult: &llm.ToolResultData{ToolCallID: "c", Name: "communicate"}}}}}
			items, _ = ProjectEntryParts("turn", 2, result, reg, nil, nil)
			if len(items) != 0 {
				t.Fatalf("result duplicated dedicated message: %+v", items)
			}
			dedicated := schema.Turn{Format: schema.TurnFormatIdentity, Kind: schema.TurnCommunicate, Communicate: &schema.CommunicateInfo{CallID: "c", Message: "fresh sentinel"}}
			items, _ = ProjectEntryParts("turn", 3, dedicated, reg, nil, nil)
			if len(items) != 1 || items[0].Text != "fresh sentinel" || items[0].CallID != "c" {
				t.Fatalf("dedicated message=%+v", items)
			}
		})
	}
}

func TestAuthorshipProjectionInvalidShapes(t *testing.T) {
	for _, author := range []schema.TranscriptAuthor{
		{Kind: "future"}, {Kind: schema.TranscriptAuthorHuman, SessionID: "parent"},
		{Kind: schema.TranscriptAuthorAgent, SessionID: "remote:parent"},
		{Kind: schema.TranscriptAuthorAgent, SessionID: "../parent"},
		{Kind: schema.TranscriptAuthorAgent, SessionID: "parent", DelegateID: "dlg_parent"},
		{Kind: schema.TranscriptAuthorAgent, SessionID: "parent", OwnerSessionID: "root"},
		{Kind: schema.TranscriptAuthorAgent, SessionID: "parent", OwnerSessionID: "root", DelegateID: "dlg_"},
		{Kind: schema.TranscriptAuthorAgent, SessionID: "parent", DelegateName: "invented"},
	} {
		got := ProjectAuthorship(&schema.TranscriptAuthorship{Author: author, Inherited: true})
		want := &appwire.TranscriptAuthorship{Author: appwire.TranscriptAuthor{Kind: appwire.TranscriptAuthorUnknown}, Inherited: true}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%+v projects %+v", author, got)
		}
	}
}
