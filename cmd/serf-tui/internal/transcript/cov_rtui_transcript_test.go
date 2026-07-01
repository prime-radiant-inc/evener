package transcript

import (
	"testing"

	"primeradiant.com/serf/appwire"
)

func TestParseJobNotificationHeadline(t *testing.T) {
	t.Run("not a job notification", func(t *testing.T) {
		if _, _, _, ok := ParseJobNotificationHeadline("just some text"); ok {
			t.Fatal("ok = true, want false for non-notification text")
		}
	})

	t.Run("failure via status attribute", func(t *testing.T) {
		text := `<job-notification job_id="j1" status="failed"></job-notification>`
		jobID, headline, isErr, ok := ParseJobNotificationHeadline(text)
		if !ok {
			t.Fatal("ok = false, want true")
		}
		if jobID != "j1" {
			t.Fatalf("jobID = %q, want j1", jobID)
		}
		if !isErr {
			t.Fatal("isError = false, want true for failed status")
		}
		if headline != "failed" {
			t.Fatalf("headline = %q, want failed (status fallback)", headline)
		}
	})

	t.Run("failure via nonzero exit code", func(t *testing.T) {
		text := `<job-notification job_id="j2" event="done" exit_code="2"></job-notification>`
		_, headline, isErr, ok := ParseJobNotificationHeadline(text)
		if !ok || !isErr {
			t.Fatalf("ok=%v isError=%v, want true true", ok, isErr)
		}
		if headline != "done" {
			t.Fatalf("headline = %q, want done (event fallback)", headline)
		}
	})

	t.Run("success with communicate excerpt", func(t *testing.T) {
		text := `<job-notification job_id="j3" status="completed" exit_code="0">` +
			`excerpt: {"data":{"test_summary":"12 passed","commit_hashes":["abcdef1234567890"],"concerns":["c1","c2"]}}` +
			`</job-notification>`
		_, headline, isErr, ok := ParseJobNotificationHeadline(text)
		if !ok {
			t.Fatal("ok = false, want true")
		}
		if isErr {
			t.Fatal("isError = true, want false for exit 0 completed")
		}
		want := "12 passed · abcdef12 · 2 concerns"
		if headline != want {
			t.Fatalf("headline = %q, want %q", headline, want)
		}
	})
}

func TestCommunicateHeadline(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{"no excerpt marker", "nothing here", ""},
		{"malformed json after excerpt", "excerpt: {not json", ""},
		{"status only", `excerpt: {"data":{"status":"running"}}`, "running"},
		{"single concern is singular", `excerpt: {"data":{"status":"ok","concerns":["only"]}}`, "ok · 1 concern"},
		{"test summary preferred over status", `excerpt: {"data":{"status":"ok","test_summary":"all green"}}`, "all green"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := communicateHeadline(tc.body); got != tc.want {
				t.Fatalf("communicateHeadline(%q) = %q, want %q", tc.body, got, tc.want)
			}
		})
	}
}

func TestClipStrAndShortHash(t *testing.T) {
	if got := clipStr("short", 60); got != "short" {
		t.Fatalf("clipStr(short) = %q, want short", got)
	}
	if got := clipStr("abcdefghij", 5); got != "abcd…" {
		t.Fatalf("clipStr truncation = %q, want abcd…", got)
	}
	if got := shortHash("abc123"); got != "abc123" {
		t.Fatalf("shortHash(short) = %q, want abc123", got)
	}
	if got := shortHash("0123456789abcdef"); got != "01234567" {
		t.Fatalf("shortHash(long) = %q, want 01234567", got)
	}
}

func TestFirstNonEmptyStr(t *testing.T) {
	if got := firstNonEmptyStr("", "  ", "hit", "later"); got != "hit" {
		t.Fatalf("firstNonEmptyStr = %q, want hit", got)
	}
	if got := firstNonEmptyStr("", "   "); got != "" {
		t.Fatalf("firstNonEmptyStr(all empty) = %q, want empty", got)
	}
}

func TestItemDuration(t *testing.T) {
	ms := func(v int64) *int64 { return &v }
	if d := ItemDuration(appwire.ThreadItem{}); d != 0 {
		t.Fatalf("nil timestamps duration = %v, want 0", d)
	}
	if d := ItemDuration(appwire.ThreadItem{StartedAt: ms(100)}); d != 0 {
		t.Fatalf("nil CompletedAt duration = %v, want 0", d)
	}
	if d := ItemDuration(appwire.ThreadItem{StartedAt: ms(200), CompletedAt: ms(100)}); d != 0 {
		t.Fatalf("completed<started duration = %v, want 0", d)
	}
	if d := ItemDuration(appwire.ThreadItem{StartedAt: ms(100), CompletedAt: ms(1100)}); d.Milliseconds() != 1000 {
		t.Fatalf("duration = %v, want 1s", d)
	}
}

func TestImageItemsPlaceholder(t *testing.T) {
	if got := ImageItemsPlaceholder(nil); got != "" {
		t.Fatalf("empty = %q, want empty", got)
	}
	one := []appwire.InputItem{{Type: "image"}}
	if got := ImageItemsPlaceholder(one); got != "[image]" {
		t.Fatalf("single = %q, want [image]", got)
	}
	many := []appwire.InputItem{{Type: "image"}, {Type: "image"}, {Type: "image"}}
	if got := ImageItemsPlaceholder(many); got != "[3 images]" {
		t.Fatalf("multi = %q, want [3 images]", got)
	}
}

// toolMsgWithSubagent builds a delegate tool message carrying a subagent run,
// the shape ApplyTieHeadline / ApplyChildActivity match against.
func toolMsgWithSubagent(run SubagentRunInfo) ChatMessage {
	return ChatMessage{Kind: MsgTool, Tool: &ToolCallInfo{Name: "delegate", Subagent: &run}}
}

func TestApplyTieHeadline(t *testing.T) {
	r := NewTranscriptReducer([]ChatMessage{
		toolMsgWithSubagent(SubagentRunInfo{JobID: "job-1"}),
	}, nil, nil)

	if r.ApplyTieHeadline("", "hi", false) {
		t.Fatal("empty jobID returned true, want false")
	}
	if r.ApplyTieHeadline("job-1", "", false) {
		t.Fatal("empty headline returned true, want false")
	}
	if r.ApplyTieHeadline("no-such-job", "hi", false) {
		t.Fatal("unmatched jobID returned true, want false")
	}
	if !r.ApplyTieHeadline("job-1", "tests failed", true) {
		t.Fatal("matching tie returned false, want true")
	}
	got := r.Messages()[0].Tool.Subagent
	if got.Headline != "tests failed" || !got.HeadlineError {
		t.Fatalf("tie result = %+v, want headline+error set", got)
	}
}

func TestApplyChildActivity(t *testing.T) {
	r := NewTranscriptReducer([]ChatMessage{
		toolMsgWithSubagent(SubagentRunInfo{TranscriptRef: "ref-1", Status: "running"}),
	}, nil, nil)

	if r.ApplyChildActivity("", "step") {
		t.Fatal("empty ref returned true, want false")
	}
	if r.ApplyChildActivity("nope", "step") {
		t.Fatal("unmatched ref returned true, want false")
	}
	if !r.ApplyChildActivity("ref-1", "reading file") {
		t.Fatal("matched activity returned false, want true")
	}
	run := r.Messages()[0].Tool.Subagent
	if run.Steps != 1 || run.Activity != "reading file" {
		t.Fatalf("after first activity: steps=%d activity=%q, want 1 'reading file'", run.Steps, run.Activity)
	}
	// Same activity again does not advance the honest step counter.
	if !r.ApplyChildActivity("ref-1", "reading file") {
		t.Fatal("repeat activity returned false, want true")
	}
	if run = r.Messages()[0].Tool.Subagent; run.Steps != 1 {
		t.Fatalf("steps advanced on unchanged activity: %d, want 1", run.Steps)
	}
}

func TestApplyUserMessageEcho_Empty(t *testing.T) {
	r := NewTranscriptReducer(nil, nil, nil)
	r.ApplyUserMessageEcho("   ")
	if len(r.Messages()) != 0 {
		t.Fatalf("blank echo appended a message: %+v", r.Messages())
	}
	r.ApplyUserMessageEcho("  hello  ")
	if len(r.Messages()) != 1 || r.Messages()[0].Text != "hello" {
		t.Fatalf("echo = %+v, want single trimmed 'hello'", r.Messages())
	}
	r.RemoveUserMessageEcho("   ")
	if len(r.Messages()) != 1 {
		t.Fatalf("blank remove changed messages: %+v", r.Messages())
	}
	r.RemoveUserMessageEcho("hello")
	if len(r.Messages()) != 0 {
		t.Fatalf("remove did not drop the echo: %+v", r.Messages())
	}
}

func TestResetAgentMessage_ShiftsActiveIndices(t *testing.T) {
	r := NewTranscriptReducer(nil, nil, nil)
	r.ApplyAgentMessageDelta("turn_1", "item_a", "first")
	r.ApplyAgentMessageDelta("turn_2", "item_b", "second")
	if len(r.Messages()) != 2 {
		t.Fatalf("setup messages = %d, want 2", len(r.Messages()))
	}

	// Reset the first message; the second must shift down and stay reachable.
	r.ResetAgentMessage("turn_1", "item_a")
	if len(r.Messages()) != 1 {
		t.Fatalf("after reset messages = %d, want 1", len(r.Messages()))
	}
	if idx, ok := r.ActiveMessages()["item_b"]; !ok || idx != 0 {
		t.Fatalf("item_b active index = %d ok=%v, want 0 true", idx, ok)
	}
	// The surviving message keeps streaming into the shifted index.
	r.ApplyAgentMessageDelta("turn_2", "item_b", " more")
	if got := r.Messages()[0].Text; got != "second more" {
		t.Fatalf("shifted message text = %q, want 'second more'", got)
	}

	// Unknown / empty item ids are no-ops.
	r.ResetAgentMessage("turn_2", "")
	r.ResetAgentMessage("turn_2", "missing")
	if len(r.Messages()) != 1 {
		t.Fatalf("no-op reset changed messages: %d, want 1", len(r.Messages()))
	}
}
