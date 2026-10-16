package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"primeradiant.com/evener/agent/internal/delegatestore"
	"primeradiant.com/evener/agent/internal/tool"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
)

// Removing the presentation bound must fail on the final registry output, even
// when the structured side channel still carries the complete page.
func TestJobListLabelPageBudget(t *testing.T) {
	t.Parallel()
	s := newTestSession(t)
	ids := seedJobListLabels(t, s, 120)
	for _, tc := range []struct {
		name       string
		args       map[string]any
		start, end int
	}{
		{"default", nil, 0, 50},
		{"maximum", map[string]any{"limit": 100}, 0, 100},
		{"offset", map[string]any{"offset": 50}, 50, 100},
		{"last", map[string]any{"offset": 100}, 100, 120},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for repeat := range 2 {
				call, state := executeJobListLabels(t, s, tc.args)
				// The registry appends repeat-call guidance after its limiter.
				// Check the first final reply's budget and both replies' rows.
				budget := 20_000
				if repeat > 0 {
					budget += utf8.RuneCountInString(call.Output) - utf8.RuneCountInString(call.RecoverableOutput)
				}
				assertJobListLabelPage(t, call, state, ids[tc.start:tc.end], tc.start, 120, budget)
			}
		})
	}
}

func TestJobListLabelConfiguredBudget(t *testing.T) {
	t.Parallel()
	s := newTestSession(t)
	ids := seedJobListLabels(t, s, 120)
	for _, budget := range []int{12_000, 1} {
		t.Run(strconv.Itoa(budget), func(t *testing.T) {
			s.reg.OverrideLimits(map[string]schema.ToolOutputLimit{"job_list": {MaxChars: budget}})
			if got := s.reg.Get("job_list").Limit.MaxChars; got != budget {
				t.Fatalf("effective limit = %d, want %d", got, budget)
			}
			call, state := executeJobListLabels(t, s, map[string]any{"limit": 100})
			if budget == 1 {
				if !call.Truncated || call.RecoverableOutput == "" || call.RecoverableOutput == call.Output {
					t.Fatal("impossible limit lost truncation recovery")
				}
				if got := jobListLabelIDs(call.RecoverableOutput); !slices.Equal(got, ids[:100]) {
					t.Fatalf("recoverable identities = %v", got)
				}
				if strings.ContainsAny(call.RecoverableOutput, "界🙂") {
					t.Fatal("impossible budget retained labels instead of fixed content only")
				}
			} else {
				assertJobListLabelPage(t, call, state, ids[:100], 0, 120, budget)
			}
			s.reg.OverrideLimits(map[string]schema.ToolOutputLimit{"job_list": {MaxChars: -1}})
			if got := s.reg.Get("job_list").Limit.MaxChars; got != budget {
				t.Fatalf("negative override changed limit to %d", got)
			}
		})
	}
	t.Run("line limit", func(t *testing.T) {
		s.reg.OverrideLimits(map[string]schema.ToolOutputLimit{"job_list": {MaxChars: 20_000, MaxLines: 3}})
		if got := s.reg.Get("job_list").Limit; got.MaxLines != 3 || got.MaxChars != 20_000 {
			t.Fatalf("effective limits = %+v", got)
		}
		call, state := executeJobListLabels(t, s, nil)
		if !call.Truncated || state.Count != 50 || !slices.Equal(jobListLabelIDs(call.RecoverableOutput), ids[:50]) {
			t.Fatal("line override changed page selection or lost recovery")
		}
	})
}

func TestJobListLabelsPreserveDurableBriefs(t *testing.T) {
	t.Parallel()
	s := newTestSession(t)
	id := "dlg_preserved"
	d := stableToolDescriptor(s, id, "")
	d.Name = strings.Repeat("界", 201)
	d.Task = strings.Repeat("🙂\n\t", 1<<19)
	d.Description = d.Task
	seedJobListLabelDelegate(t, s, id, d, 1)
	path := filepath.Join(jobsDir(s.stateDir, s.ID()), "delegates.jsonl")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// The existing detail renderer rejects JSON larger than its configured cap.
	s.reg.OverrideLimits(map[string]schema.ToolOutputLimit{"job_status": {MaxChars: 32 << 20}})
	for range 2 {
		call, state := executeJobListLabels(t, s, nil)
		if call.Truncated || utf8.RuneCountInString(call.Output) > 1000 {
			t.Errorf("single-row preview unbounded: %d runes, truncated %t", utf8.RuneCountInString(call.Output), call.Truncated)
		}
		if len(state.Items) != 1 || state.Items[0].Description != d.Description || state.Items[0].Task != d.Task || state.Items[0].Name != d.Name {
			t.Fatal("list mutated authoritative fields")
		}
		args, err := json.Marshal(map[string]any{"target": id})
		if err != nil {
			t.Fatal(err)
		}
		status := s.reg.ExecuteCall(context.Background(), s.env, llm.ToolCallData{ID: "detail", Name: "job_status", Arguments: args})
		var detail stableDelegateStatusResult
		if status.IsError {
			t.Fatalf("detail read: %v", status.Err)
		}
		if err := json.Unmarshal(status.ToolState, &detail); err != nil {
			t.Fatal(err)
		}
		if detail.Task != d.Task || detail.Description != d.Description || detail.Name != d.Name || detail.TranscriptRef != d.TranscriptRef {
			t.Fatal("detail read lost durable input")
		}
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("read mutated journal bytes")
	}
	aggregate := delegateAggregateSnapshot(t, s.delegateController, id)
	if !reflect.DeepEqual(aggregate.Descriptor, d) {
		t.Fatal("read mutated descriptor")
	}
}

func TestJobListLabelScope(t *testing.T) {
	t.Parallel()
	s := newTestSession(t)
	for i, id := range []string{"dlg_parent", "dlg_child"} {
		parent := ""
		if i == 1 {
			parent = "dlg_parent"
		}
		d := stableToolDescriptor(s, id, parent)
		d.Description = strings.Repeat("界", 300)
		seedJobListLabelDelegate(t, s, id, d, i)
	}
	seedStableToolShell(t, s.jobManager, "job_shell", time.Unix(100, 0).UTC(), "completed")
	for _, tc := range []struct {
		args   map[string]any
		ids    []string
		depths []int
	}{
		{map[string]any{"type": []string{"delegate"}}, []string{"dlg_parent"}, []int{0}},
		{map[string]any{"include_descendants": true, "type": []string{"delegate"}, "status": []string{"idle"}}, []string{"dlg_parent", "dlg_child"}, []int{0, 1}},
		{map[string]any{"include_nested": true, "type": []string{"delegate"}}, []string{"dlg_parent", "dlg_child"}, []int{0, 1}},
		{map[string]any{"status": []string{"completed"}}, []string{"job_shell"}, []int{0}},
	} {
		call, state := executeJobListLabels(t, s, tc.args)
		assertJobListLabelPage(t, call, state, tc.ids, 0, len(tc.ids), 20_000)
		for i, row := range state.Items {
			if row.Depth != tc.depths[i] {
				t.Fatalf("depth = %d, want %d", row.Depth, tc.depths[i])
			}
		}
	}
}

func TestJobListLabelPreviewBoundaries(t *testing.T) {
	t.Parallel()
	fallback := strings.Repeat("λ", 201)
	for _, tc := range []struct {
		name, description         string
		command                   *string
		wantName, wantDescription string
	}{
		{strings.Repeat("界", 199), strings.Repeat("🙂", 199), nil, strings.Repeat("界", 199), strings.Repeat("🙂", 199)},
		{strings.Repeat("界", 200), strings.Repeat("🙂", 200), nil, strings.Repeat("界", 200), strings.Repeat("🙂", 200)},
		{strings.Repeat("界", 201), strings.Repeat("🙂", 201), nil, strings.Repeat("界", 199) + "…", strings.Repeat("🙂", 199) + "…"},
		{"\tα\nβ  ", "\rγ\tδ\n", nil, "α β", "γ δ"},
		{"", "", &fallback, "", strings.Repeat("λ", 199) + "…"},
		{"", "δ", &fallback, "", "δ"},
		{"", strings.Repeat(" ", 200) + "hidden", &fallback, "", "…"},
		{"", "", nil, "", ""},
	} {
		out := jobListResult{Items: []jobListEntry{{ID: "job_boundary", Type: "shell", Status: "completed", Name: tc.name, Description: tc.description, Command: tc.command}}, Count: 1, Total: 1}
		text := formatJobList(out, jobToolResultDefaultMaxChar)
		lines := strings.Split(text, "\n")
		if !utf8.ValidString(text) || len(lines) != 4 {
			t.Errorf("preview broke row structure: %d lines", len(lines))
			continue
		}
		parts := strings.Split(lines[1], "  ")
		want := strings.Join(slices.DeleteFunc([]string{tc.wantName, tc.wantDescription}, func(s string) bool { return s == "" }), " — ")
		if want == "" {
			if len(parts) != 4 {
				t.Errorf("empty labels left %d columns", len(parts))
			}
		} else if len(parts) != 5 || parts[3] != want {
			t.Errorf("bounded preview mismatch for source lengths %d/%d", utf8.RuneCountInString(tc.name), utf8.RuneCountInString(tc.description))
		}
	}
}

func TestJobListLabelNonpositiveBudget(t *testing.T) {
	t.Parallel()
	out := jobListResult{Count: 100, Total: 100}
	name, description := strings.Repeat("界", 200), strings.Repeat("🙂", 200)
	var ids []string
	for i := range 100 {
		id := fmt.Sprintf("dlg_unlimited_%03d", i)
		ids = append(ids, id)
		out.Items = append(out.Items, jobListEntry{ID: id, Type: "delegate", Status: "idle", Name: name, Description: description})
	}
	for _, budget := range []int{0, -1} {
		text := formatJobList(out, budget)
		if utf8.RuneCountInString(text) <= 20_000 || !slices.Equal(jobListLabelIDs(text), ids) {
			t.Fatalf("nonpositive budget %d lost the full bounded page", budget)
		}
		if strings.Count(text, name) != 100 || strings.Count(text, description) != 100 {
			t.Fatalf("nonpositive budget %d shortened previews", budget)
		}
	}
}

func TestJobListLabelFixedContentBudget(t *testing.T) {
	t.Parallel()
	reason := strings.Repeat("R", 600)
	exit := 17
	out := jobListResult{
		Items: []jobListEntry{{ID: "job_fixed", Type: "shell", Status: "command_exited_nonzero", Depth: 2,
			Name: strings.Repeat("界", 201), Description: strings.Repeat("🙂", 201), Reason: &reason, ExitCode: &exit, TotalBytes: 123}},
		Count: 1, Total: 1, TurnSlots: &turnSlotOccupancy{InUse: 2, Cap: 3, Jobs: 1, Drives: 1}, DelegationAllowance: 4,
		Watches:       []watchListEntry{{ID: "watch_active", Source: "job_fixed", Condition: strings.Repeat("C", 100)}},
		RecentWatches: []recentWatchEntry{{ID: "watch_recent", Source: "self", EndReason: strings.Repeat("E", 100), Deliveries: 5}},
	}
	for _, budget := range []int{1200, 500} {
		text := formatJobList(out, budget)
		if !strings.Contains(text, reason) || !strings.Contains(text, out.Watches[0].Condition) || !strings.Contains(text, out.RecentWatches[0].EndReason) {
			t.Fatal("fixed evidence shortened to fit labels")
		}
		if !slices.Equal(jobListLabelIDs(text), []string{"job_fixed"}) {
			t.Fatal("fixed identity lost")
		}
		if budget == 1200 {
			if utf8.RuneCountInString(text) > budget || !strings.ContainsAny(text, "界🙂") {
				t.Fatal("fitting budget did not retain bounded previews")
			}
		} else if utf8.RuneCountInString(text) <= budget || strings.ContainsAny(text, "界🙂") {
			t.Fatal("impossible budget did not leave fixed evidence for registry recovery")
		}
	}
}

func TestJobListLabelEffectiveLimit(t *testing.T) {
	t.Parallel()
	if jobListOutputMaxChars(nil) != 20_000 || jobListOutputMaxChars(tool.NewRegistry()) != 20_000 {
		t.Fatal("missing tool lost default limit")
	}
	s := newTestSession(t)
	for _, budget := range []int{0, -1, 1, 12_000} {
		reg := tool.NewRegistry()
		registered := *s.reg.Get("job_list")
		registered.Limit.MaxChars = budget
		if err := reg.Register(registered); err != nil {
			t.Fatal(err)
		}
		want := budget
		if budget == 0 {
			want = 20_000 // Register resolves zero to the tool's default.
		}
		if got := jobListOutputMaxChars(reg); got != want {
			t.Fatalf("effective budget = %d, want %d", got, want)
		}
	}
}

func seedJobListLabels(t *testing.T, s *Session, count int) []string {
	t.Helper()
	ids := make([]string, count)
	for i := range ids {
		ids[i] = fmt.Sprintf("dlg_preview_%03d", i)
		d := stableToolDescriptor(s, ids[i], "")
		d.Name = strings.Repeat("界", 250)
		d.Description = strings.Repeat("🙂", 250) + "\n\tEND"
		if i == 0 {
			d.Description += strings.Repeat("x", 4<<20)
		}
		d.Task = d.Description
		seedJobListLabelDelegate(t, s, ids[i], d, i)
	}
	return ids
}

func seedJobListLabelDelegate(t *testing.T, s *Session, id string, d delegatestore.Descriptor, index int) {
	t.Helper()
	started := time.Unix(int64(1000-index*2), 0).UTC()
	packet := delegatestore.TerminalPacket{Kind: delegatestore.PacketReported, Message: json.RawMessage(`"opaque-packet"`)}
	c := s.delegateController
	c.mu.Lock()
	_, err := c.appendLocked(
		delegatestore.Event{Kind: delegatestore.EventDelegateCreated, DelegateID: id, Created: &delegatestore.DelegateCreated{Descriptor: d}},
		delegateControllerRunStartedEvent(id, 1, delegatestore.TriggerInitial, started),
		delegateRunFinishedEvent(delegateLease{delegateID: id, generation: 1}, delegatestore.OutcomeCompleted, delegatestore.DispositionReported, "", started.Add(time.Second), delegateDeliveryID(id, 1), &packet),
	)
	c.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
}

func executeJobListLabels(t *testing.T, s *Session, args map[string]any) (tool.ExecResult, jobListResult) {
	t.Helper()
	if args == nil {
		args = map[string]any{}
	}
	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	call := s.reg.ExecuteCall(context.Background(), s.env, llm.ToolCallData{ID: "list", Name: "job_list", Arguments: raw})
	if call.IsError || call.Err != nil {
		t.Fatalf("job_list: %v", call.Err)
	}
	var state jobListResult
	if err := json.Unmarshal(call.ToolState, &state); err != nil {
		t.Fatal(err)
	}
	return call, state
}

func jobListLabelIDs(text string) []string {
	var ids []string
	for line := range strings.SplitSeq(text, "\n") {
		fields := strings.Fields(line)
		if len(fields) > 0 && (strings.HasPrefix(fields[0], "dlg_") || strings.HasPrefix(fields[0], "job_")) {
			ids = append(ids, fields[0])
		}
	}
	return ids
}

func assertJobListLabelPage(t *testing.T, call tool.ExecResult, state jobListResult, ids []string, offset, total, budget int) {
	t.Helper()
	if call.Truncated || !utf8.ValidString(call.Output) || utf8.RuneCountInString(call.Output) > budget {
		t.Errorf("page output: %d runes, truncated %t", utf8.RuneCountInString(call.Output), call.Truncated)
	}
	if got := jobListLabelIDs(call.Output); !slices.Equal(got, ids) {
		t.Errorf("page identities = %v, want %v", got, ids)
	}
	if state.Total != total || state.Offset != offset || state.Count != len(ids) || len(state.Items) != len(ids) {
		t.Fatalf("page metadata: total %d offset %d count %d items %d", state.Total, state.Offset, state.Count, len(state.Items))
	}
	for i, row := range state.Items {
		if row.ID != ids[i] || row.TranscriptRef == nil {
			t.Fatalf("row %d identity/reference lost", i)
		}
		if row.Type == "delegate" && (row.Status != "idle" || *row.TranscriptRef != "local:child-"+ids[i] || row.DurationMS == nil || *row.DurationMS != 1000 || row.EndedAt == nil) {
			t.Fatalf("row %d lifecycle/reference changed", i)
		}
	}
	// The page footer's numbers are data, independent of its surrounding copy.
	_, footer, ok := strings.Cut(call.Output, "\n\n")
	if !ok {
		t.Error("page footer missing")
		return
	}
	footer, _, _ = strings.Cut(footer, "\n")
	got := regexp.MustCompile(`[0-9]+`).FindAllString(footer, -1)
	want := []string{strconv.Itoa(len(ids))}
	if offset > 0 || total > len(ids) {
		want = []string{strconv.Itoa(offset + 1), strconv.Itoa(offset + len(ids)), strconv.Itoa(total)}
	}
	if !slices.Equal(got, want) {
		t.Errorf("footer numbers = %v, want %v", got, want)
	}
}

func BenchmarkFormatJobListLabels(b *testing.B) {
	for _, count := range []int{50, 100} {
		for _, size := range []int{200, 4 << 20} {
			b.Run(fmt.Sprintf("rows%d/bytes%d", count, size), func(b *testing.B) {
				description := strings.Repeat("x", size)
				out := jobListResult{Count: count, Total: count}
				for i := range count {
					out.Items = append(out.Items, jobListEntry{ID: fmt.Sprintf("dlg_%03d", i), Type: "delegate", Status: "idle", Name: strings.Repeat("n", 200), Description: description})
				}
				b.ReportAllocs()
				b.ResetTimer()
				for b.Loop() {
					text := formatJobList(out, jobToolResultDefaultMaxChar)
					b.ReportMetric(float64(utf8.RuneCountInString(text)), "output-runes")
				}
			})
		}
	}
}
