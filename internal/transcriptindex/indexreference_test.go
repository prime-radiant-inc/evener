package transcriptindex

import (
	"bytes"
	"fmt"
	"os"
	"reflect"
	"strconv"
	"testing"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/llm"
)

// These expectations come from the fixture, not referenceCandidates or another
// invocation of the projector. Removing inherited reconstruction must lose them.
func inheritedCommunicateFixture(name string) fixture {
	source := inRound(inTurn("copied", assistant(call("call_child", "communicate", `{"message":"copied child update"}`))), "round_child")
	source.Authorship = &schema.TranscriptAuthorship{Author: schema.TranscriptAuthor{Kind: schema.TranscriptAuthorAgent, SessionID: "child", OwnerSessionID: "parent", DelegateID: "dlg_child", DelegateName: "Child"}, Inherited: true}
	settled := inRound(inTurn("copied", results(result("call_child", name, "ok"))), "round_child")
	settled.Authorship = &schema.TranscriptAuthorship{Author: schema.TranscriptAuthor{Kind: schema.TranscriptAuthorUnknown}, Inherited: true}
	return fixture{name: "inherited-communicate", header: transcript.Header{SessionID: "leaf"}, lines: []fixtureLine{
		entryLine(opens("copied", schema.TurnSpanExecution, user("before call"))), entryLine(source), entryLine(settled),
	}}
}

func assertInheritedCommunicate(t *testing.T, w Window, pending bool, entry uint64) {
	t.Helper()
	if len(w.Candidates) != 1 || !w.HasOlder {
		t.Fatalf("expected one bounded item with older history: %+v", w)
	}
	c := w.Candidates[0]
	wantAuthor := &appwire.TranscriptAuthorship{Author: appwire.TranscriptAuthor{Kind: appwire.TranscriptAuthorAgent, SessionRef: "local:child", OwnerRef: "local:parent", DelegateID: "dlg_child", DelegateName: "Child"}, Inherited: true}
	if c.Item.Type != "agentMessage" || c.Item.Text != "copied child update" || c.Item.CallID != "call_child" || c.TurnID != "copied" || !reflect.DeepEqual(c.Item.Authorship, wantAuthor) {
		t.Fatalf("inherited message identity/content/author: %+v", c.Item)
	}
	if pending {
		if c.Item.ID != "item_assistant_flushed_call_child" {
			t.Fatalf("pending item identity=%+v", c.Item)
		}
	} else if c.Position != (appwire.ThreadItemPosition{Entry: entry}) || c.Item.ID != fmt.Sprintf("item_assistant_%d_0", entry) || c.Item.RoundID != "round_child" {
		t.Fatalf("paired item identity/position=%+v", c.Item)
	}
}

func TestInheritedCommunicateReopenAndAppend(t *testing.T) {
	for _, name := range []string{"communicate", ""} {
		t.Run("result-name-"+name, func(t *testing.T) {
			fx := inheritedCommunicateFixture(name)
			// The full text run differs from the message, but its final part
			// equals it. Replaying as legacy would wrongly suppress the flush.
			fx.lines[1].turn.Message.Content = append([]llm.ContentPart{text("prefix-"), text("copied child update")}, fx.lines[1].turn.Message.Content...)
			path, lines := writeHeaderOnly(t, fx)
			appendBytes(t, path, lines[0])
			appendBytes(t, path, lines[1])
			dir := t.TempDir()
			x := openIndex(t, path, dir)
			build := x.build
			for range 2 {
				w, err := x.Latest(1)
				if err != nil {
					t.Fatal(err)
				}
				assertInheritedCommunicate(t, w, true, 0)
				if err := x.Close(); err != nil {
					t.Fatal(err)
				}
				x = openIndex(t, path, dir)
				if x.rebuilds != 0 || x.build != build {
					t.Fatal("reopening pending call rebuilt history")
				}
			}
			// The call is in the persisted prefix, not the extension being read.
			appendBytes(t, path, lines[2])
			catchUp(t, x)
			w, err := x.Latest(1)
			if err != nil {
				t.Fatal(err)
			}
			assertInheritedCommunicate(t, w, false, 3)
			if x.rebuilds != 0 || x.build != build || len(x.builder.commCalls) != 0 {
				t.Fatal("appended result rebuilt history or left a pending duplicate")
			}
			if err := x.Close(); err != nil {
				t.Fatal(err)
			}
			x = openIndex(t, path, dir)
			w, err = x.Latest(1)
			if err != nil {
				t.Fatal(err)
			}
			assertInheritedCommunicate(t, w, false, 3)
			if x.rebuilds != 0 || x.build != build {
				t.Fatal("reopening paired result rebuilt history")
			}
			full, err := x.Latest(10)
			if err != nil || len(full.Candidates) != 3 {
				t.Fatalf("paired result duplicated or lost an item: %+v, %v", full, err)
			}
		})
	}
}

func TestInheritedCommunicateBoundedContext(t *testing.T) {
	fx := inheritedCommunicateFixture("")
	// Legacy context replay would compare only the last part and hide the
	// result. Identity replay compares the entire displayed run instead.
	fx.lines[1].turn.Message.Content = append([]llm.ContentPart{text("prefix-"), text("copied child update")}, fx.lines[1].turn.Message.Content...)
	var prefix []fixtureLine
	for i := range 256 {
		prefix = append(prefix, entryLine(opens(fmt.Sprintf("older_%d", i), schema.TurnSpanGap, user(fmt.Sprintf("opaque-%d", i)))))
	}
	fx.lines = append(prefix, fx.lines...)
	fx.lines = append(fx.lines, entryLine(opens("later", schema.TurnSpanGap, user("after result"))))
	path := writeFixture(t, fx)
	x := openIndex(t, path, t.TempDir())
	w, err := x.Before(appwire.ThreadItemPosition{Entry: 260}, 1)
	if err != nil {
		t.Fatal(err)
	}
	assertInheritedCommunicate(t, w, false, 259)
	slot, found, err := x.findItem(appwire.ThreadItemPosition{Entry: 259})
	if err != nil || !found {
		t.Fatalf("result item not indexed: found=%v err=%v", found, err)
	}
	buf, err := x.items.read(slot, 1)
	if err != nil {
		t.Fatal(err)
	}
	r := newReader(x)
	item, err := r.item(decodeItem(buf), "copied")
	if err != nil || item.Text != "copied child update" {
		t.Fatalf("context replay: item=%+v err=%v", item, err)
	}
	// One call and its result, independent of the 256 unrelated prefix entries.
	if len(r.entries) != 2 {
		t.Fatalf("bounded replay read %d entries, want call and result only", len(r.entries))
	}
}

func TestInheritedCommunicateStaleProjectionRebuild(t *testing.T) {
	path := writeFixture(t, inheritedCommunicateFixture("communicate"))
	original, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	x := openIndex(t, path, dir)
	if err := x.Close(); err != nil {
		t.Fatal(err)
	}
	// Model the v8 omission: covered transcript unchanged, only the user item
	// indexed, and the exact old projection identifier with the current schema.
	rewriteMetaField(t, dir, "items", "1")
	rewriteMetaField(t, dir, "projection", strconv.Quote("transcript-read-model-v8:"+schemaID))
	x = openIndex(t, path, dir)
	if x.rebuilds != 1 {
		t.Fatalf("old sidecar rebuilds=%d, want 1", x.rebuilds)
	}
	w, err := x.Latest(1)
	if err != nil {
		t.Fatal(err)
	}
	assertInheritedCommunicate(t, w, false, 3)
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(original, after) {
		t.Fatalf("sidecar repair changed original transcript: err=%v", err)
	}
}

func TestInheritedCommunicateLaterAssistantContext(t *testing.T) {
	fx := inheritedCommunicateFixture("")
	fx.lines[1].turn.Message.Content = append([]llm.ContentPart{text("copied child update")}, fx.lines[1].turn.Message.Content...)
	latest := inTurn("copied", assistant(text("prefix-"), text("copied child update")))
	latest.Authorship = &schema.TranscriptAuthorship{Author: schema.TranscriptAuthor{Kind: schema.TranscriptAuthorUnknown}, Inherited: true}
	fx.lines = append(fx.lines[:2], entryLine(latest), fx.lines[2])
	path, lines := writeHeaderOnly(t, fx)
	for _, line := range lines[:3] {
		appendBytes(t, path, line)
	}
	dir := t.TempDir()
	x := openIndex(t, path, dir)
	if err := x.Close(); err != nil {
		t.Fatal(err)
	}
	x = openIndex(t, path, dir)
	appendBytes(t, path, lines[3])
	catchUp(t, x)
	w, err := x.Latest(1)
	if err != nil {
		t.Fatal(err)
	}
	assertInheritedCommunicate(t, w, false, 4)
	if x.rebuilds != 0 {
		t.Fatal("restoring the later echo context rebuilt history")
	}
	slot, found, err := x.findItem(appwire.ThreadItemPosition{Entry: 4})
	if err != nil || !found {
		t.Fatalf("result item not indexed: found=%v err=%v", found, err)
	}
	buf, err := x.items.read(slot, 1)
	if err != nil {
		t.Fatal(err)
	}
	r := newReader(x)
	item, err := r.item(decodeItem(buf), "copied")
	if err != nil || item.Text != "copied child update" || !reflect.DeepEqual(item.Authorship, w.Candidates[0].Item.Authorship) {
		t.Fatalf("later context replay: item=%+v err=%v", item, err)
	}
	if len(r.entries) != 3 {
		t.Fatalf("read %d entries, want call, later text, and result", len(r.entries))
	}
}

func TestInheritedCommunicateIndexedNeighbors(t *testing.T) {
	for _, outcome := range []string{"success", "echo", "echo-empty-tail", "unpaired-echo", "rejected", "runtime-error", "fresh", "fold"} {
		t.Run(outcome, func(t *testing.T) {
			fx := inheritedCommunicateFixture("")
			source := &fx.lines[1].turn
			source.Message.Content = append([]llm.ContentPart{thinking("thought-a"), text("answer-"), text("sentinel"), thinking("thought-b")}, source.Message.Content...)
			result := fx.lines[2].turn.Message.Content[0].ToolResult
			switch outcome {
			case "echo", "echo-empty-tail", "unpaired-echo":
				source.Message.Content[4] = call("call_child", "communicate", `{"message":"answer-sentinel"}`)
				if outcome == "echo-empty-tail" {
					source.Message.Content = append(source.Message.Content, text(""))
				}
				if outcome == "unpaired-echo" {
					fx.lines = fx.lines[:2]
				}
			case "rejected":
				result.IsError, result.PrevalOnly = true, true
			case "runtime-error":
				result.IsError = true
			case "fresh":
				source.Authorship.Inherited = false
				fx.lines = append(fx.lines, entryLine(communicated("copied", "call_child", "copied child update")))
			case "fold":
				// A copied original projects once. Its compaction fold copy must
				// not register the consumed call again and create a flush duplicate.
				fx.lines = append(fx.lines, entryLine(foldCopy(*source, 1)))
			}
			path := writeFixture(t, fx)
			dir := t.TempDir()
			x := openIndex(t, path, dir)
			for pass := range 2 {
				w, err := x.Latest(20)
				if err != nil {
					t.Fatal(err)
				}
				wantCount := 4
				if outcome == "echo" || outcome == "echo-empty-tail" || outcome == "unpaired-echo" || outcome == "runtime-error" {
					wantCount = 3
				}
				if len(w.Candidates) != wantCount {
					t.Fatalf("%s read %d items, want %d: %+v", outcome, len(w.Candidates), wantCount, w.Candidates)
				}
				reasoning, answer := w.Candidates[1].Item, w.Candidates[2].Item
				if reasoning.Type != "reasoning" || reasoning.Text != "thought-a\n\nthought-b" || reasoning.ID != "item_reasoning_2_0" || reasoning.RoundID != "round_child" || answer.Type != "agentMessage" || answer.Text != "answer-sentinel" || answer.ID != "item_assistant_2_1" || answer.RoundID != "round_child" {
					t.Fatalf("identity grouping changed: reasoning=%+v answer=%+v", reasoning, answer)
				}
				if wantCount == 4 {
					item := w.Candidates[3].Item
					if item.CallID != "call_child" {
						t.Fatalf("call identity changed: %+v", item)
					}
					if outcome == "rejected" {
						if item.Type != "commandExecution" || item.Status != appwire.TurnStatusFailed || !item.PrevalOnly || item.Error != "ok" || item.ArgumentsJSON != `{"message":"copied child update"}` || item.Authorship == nil || item.Authorship.Author.SessionRef != "local:child" || !item.Authorship.Inherited {
							t.Fatalf("rejected result=%+v", item)
						}
					} else if item.Type != "agentMessage" || item.Text != "copied child update" {
						t.Fatalf("delivered result=%+v", item)
					}
				}
				if pass == 0 {
					if err := x.Close(); err != nil {
						t.Fatal(err)
					}
					x = openIndex(t, path, dir)
					if x.rebuilds != 0 {
						t.Fatal("unchanged sidecar rebuilt on reopen")
					}
				}
			}
		})
	}
}

func TestInheritedCommunicateDedicatedDelivery(t *testing.T) {
	for _, echo := range []bool{false, true} {
		t.Run(fmt.Sprintf("echo-%t", echo), func(t *testing.T) {
			fx := inheritedCommunicateFixture("")
			delivery := communicated("copied", "call_child", "copied child update")
			delivery.Authorship = fx.lines[1].turn.Authorship.Clone()
			fx.lines = append(fx.lines[:2:2], entryLine(delivery), fx.lines[2])
			if echo {
				fx.lines[1].turn.Message.Content = append([]llm.ContentPart{text("copied child update")}, fx.lines[1].turn.Message.Content...)
			}
			path, lines := writeHeaderOnly(t, fx)
			for _, line := range lines[:3] {
				appendBytes(t, path, line)
			}
			dir := t.TempDir()
			x := openIndex(t, path, dir)
			for pass := range 3 {
				full, err := x.Latest(10)
				if err != nil || len(full.Candidates) != 2 || len(x.builder.commCalls) != 0 {
					t.Fatalf("pass %d: delivery duplicated or left pending: window=%+v pending=%v err=%v", pass, full, x.builder.commCalls, err)
				}
				latest, err := x.Latest(1)
				if err != nil || len(latest.Candidates) != 1 || !latest.HasOlder {
					t.Fatalf("bounded delivery=%+v err=%v", latest, err)
				}
				item := latest.Candidates[0].Item
				wantID := "item_assistant_3_0"
				if echo {
					wantID = "item_assistant_2_0"
				}
				if item.ID != wantID || item.Text != "copied child update" || item.Authorship == nil || !item.Authorship.Inherited || item.Authorship.Author.SessionRef != "local:child" {
					t.Fatalf("dedicated/echo identity or author changed: %+v", item)
				}
				if err := x.Close(); err != nil {
					t.Fatal(err)
				}
				x = openIndex(t, path, dir)
				if x.rebuilds != 0 {
					t.Fatal("pending-state recovery rebuilt unchanged history")
				}
				if pass == 1 {
					appendBytes(t, path, lines[3])
					catchUp(t, x)
				}
			}
		})
	}
}

func TestInheritedCommunicateDuplicateProjectionRebuild(t *testing.T) {
	fx := inheritedCommunicateFixture("")
	delivery := communicated("copied", "call_child", "copied child update")
	delivery.Authorship = fx.lines[1].turn.Authorship.Clone()
	fx.lines = append(fx.lines[:2:2], entryLine(delivery), fx.lines[2])
	path := writeFixture(t, fx)
	original, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	x := openIndex(t, path, dir)
	if err := x.Close(); err != nil {
		t.Fatal(err)
	}
	rewriteMetaField(t, dir, "projection", strconv.Quote("transcript-read-model-v9:"+schemaID))
	x = openIndex(t, path, dir)
	if x.rebuilds != 1 {
		t.Fatalf("duplicate-prone projection rebuilds=%d want=1", x.rebuilds)
	}
	w, err := x.Latest(10)
	if err != nil || len(w.Candidates) != 2 || w.Candidates[1].Item.ID != "item_assistant_3_0" {
		t.Fatalf("rebuilt delivery=%+v err=%v", w, err)
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(original, after) {
		t.Fatalf("sidecar repair changed original transcript: err=%v", err)
	}
}
