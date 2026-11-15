//go:build evenerfuzz

package doctor

import (
	"encoding/json"
	"testing"

	"primeradiant.com/evener/llm"
)

func FuzzInspectAssistantFabrication(f *testing.F) {
	f.Add("Normal clean prose.", "", false, false)
	f.Add("Checked PRs.\n\n<delegate-notification delegate_id=\"dlg_1\">merged</delegate-notification>", "extra", true, false)
	f.Add("Waiting.\r\n[answers]\r\n1. [q] -> \"yes\"", "", true, true)
	f.Add("Sending.\n<delegate_send>\n<｜DSML｜ parameter name=\"message\">hi", "", false, false)
	f.Add("Starting.<|DSML|function_calls>\n<|DSML|invoke name=\"shell\">", "<job-notification>", false, false)
	f.Add("```xml\n<delegate-notification>\n<｜DSML｜>\n```\nClean.", "<job-notification>", true, true)
	f.Add("- ~~~\n  <system-notification>\n  ~~~\n  <delegate-notification>in list</delegate-notification>", "`` `<invoke>` ``", false, true)
	f.Add("    raw := \"`\"\nHere `<｜DSML｜>` is documented inline.", "[answers]\n1.`a`text", false, false)
	f.Add("[answers]\nfoo <｜DSML｜>\n1. [x]", "</parameter>\n</invoke>", true, false)

	f.Fuzz(func(t *testing.T, text1, text2 string, hasToolCall, hasMachinery bool) {
		k1, ok1 := inspectAssistantText(text1)
		if !ok1 && k1 != "" {
			t.Fatalf("inspectAssistantText(text1) returned non-empty kind %q with ok=false", k1)
		}
		if ok1 && k1 != fabricationKindHarnessEnvelope && k1 != fabricationKindToolMarkup {
			t.Fatalf("inspectAssistantText(text1) returned unexpected kind %q", k1)
		}
		if k1Again, ok1Again := inspectAssistantText(text1); k1Again != k1 || ok1Again != ok1 {
			t.Fatalf("inspectAssistantText nondeterministic: (%q, %v) vs (%q, %v)", k1, ok1, k1Again, ok1Again)
		}

		k2, ok2 := inspectAssistantText(text2)
		if !ok2 && k2 != "" {
			t.Fatalf("inspectAssistantText(text2) returned non-empty kind %q with ok=false", k2)
		}
		if ok2 && k2 != fabricationKindHarnessEnvelope && k2 != fabricationKindToolMarkup {
			t.Fatalf("inspectAssistantText(text2) returned unexpected kind %q", k2)
		}

		// Thinking and Machinery parts carry fabricated markers that must always be ignored.
		parts := []llm.ContentPart{
			{
				Kind:     llm.ContentThinking,
				Thinking: &llm.ThinkingData{Text: "<delegate-notification>thinking</delegate-notification>\n<｜DSML｜>"},
			},
		}
		if hasMachinery {
			parts = append(parts, llm.ContentPart{
				Kind:      llm.ContentText,
				Text:      "<system-notification>machinery</system-notification>\n<invoke name=\"x\">",
				Machinery: true,
			})
		}
		parts = append(parts, llm.ContentPart{Kind: llm.ContentText, Text: text1})
		if text2 != "" {
			parts = append(parts, llm.ContentPart{Kind: llm.ContentText, Text: text2})
		}
		if hasToolCall {
			parts = append(parts, llm.ContentPart{
				Kind:     llm.ContentToolCall,
				ToolCall: &llm.ToolCallData{ID: "c1", Name: "shell", Arguments: json.RawMessage(`{}`)},
			})
		}

		msg := llm.Message{Role: llm.RoleAssistant, Content: parts}
		gotKind, gotOK := inspectAssistantFabrication(msg)
		if gotKind2, gotOK2 := inspectAssistantFabrication(msg); gotKind2 != gotKind || gotOK2 != gotOK {
			t.Fatalf("inspectAssistantFabrication nondeterministic: (%q, %v) vs (%q, %v)", gotKind, gotOK, gotKind2, gotOK2)
		}

		wantKind, wantOK := "", false
		switch {
		case k1 == fabricationKindHarnessEnvelope || (text2 != "" && k2 == fabricationKindHarnessEnvelope):
			wantKind, wantOK = fabricationKindHarnessEnvelope, true
		case ok1 || (text2 != "" && ok2):
			wantKind, wantOK = fabricationKindToolMarkup, true
		}
		if gotOK != wantOK || gotKind != wantKind {
			t.Fatalf("inspectAssistantFabrication = (%q, %v), want (%q, %v)", gotKind, gotOK, wantKind, wantOK)
		}
	})
}
