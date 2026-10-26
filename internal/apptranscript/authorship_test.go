package apptranscript

import (
	"encoding/json"
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
