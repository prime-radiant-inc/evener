//go:build serffuzz

package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/internal/jobstore"
	"primeradiant.com/serf/agent/internal/tool"
	"primeradiant.com/serf/agent/provenance"
	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/llm"
)

// FuzzShellNotificationRenderProgram exercises durable-record projection,
// terminal excerpt resolution, and all three notification render shapes. The
// job store and output log live below t.TempDir; no process or provider runs.
func FuzzShellNotificationRenderProgram(f *testing.F) {
	f.Add(uint8(0), "short output\n", "exit_zero", "")
	f.Add(uint8(1), strings.Repeat("x", terminalExcerptBytes+20), "done", "job:custom")
	f.Add(uint8(2), "frame", "output_match: ready", "")
	f.Add(uint8(3), "watch body", "event: ASSISTANT_TEXT_END", "")
	f.Add(uint8(4), "missing", "not_found", "")
	f.Add(uint8(5), "running", "", "")
	f.Add(uint8(6), strings.Repeat("delegate\n", 1200), "completed", "")
	f.Add(uint8(7), "ignored", "watch", "")
	f.Add(uint8(8), "", "empty_output", "")
	f.Add(uint8(9), "default event", "", "")
	f.Add(uint8(16), "lane output", "completed", "")
	f.Add(uint8(22), "lane output", "completed", "")

	f.Fuzz(func(t *testing.T, mode uint8, content, reason, transcriptRef string) {
		if len(content) > terminalExcerptBytes*2 {
			content = content[:terminalExcerptBytes*2]
		}
		jm := newTestJM(t)
		defer func() { _ = jm.close() }()
		s := &Session{id: jm.sessionID, jobManager: jm}

		code := int(mode%7) - 2
		jobProv := provenance.WithWatch(nil, "watch-job", "gen", "delivery-job", jm.sessionID, "caller")
		noteProv := provenance.WithWatch(nil, "watch-note", "gen", "delivery-note", jm.sessionID, "caller")
		rec := &jobstore.JobRecord{
			JobID: "job_render", Type: jobstore.JobShell, Status: jobstore.StatusCompleted,
			Reason: reason, TranscriptRef: transcriptRef, OutputBytes: int64(len(content)),
			ExitCode: &code, Provenance: jobProv,
		}
		if mode&4 != 0 {
			rec.NotificationProvenance = noteProv
		}
		n := jobNotificationFromRecord(rec)
		if n.Provenance == rec.Provenance || (rec.NotificationProvenance != nil && n.Provenance == rec.NotificationProvenance) {
			t.Fatal("notification provenance was not cloned")
		}

		var excerpt notificationExcerpt
		terminalWithOutput := false
		switch mode % 8 {
		case 0, 1:
			writeFinishedJobWithOutput(t, jm, rec.JobID, rec.Type, content)
			terminalWithOutput = true
			excerpt = s.terminalNotificationExcerpt(n)
		case 2:
			n = watchSendTokenNotification("job_render", jobstore.WatchSendState{
				Key:        jobstore.WatchSendKey{ResolvedWatchedIdentity: "job_render", ResolvedSendTo: "caller"},
				DeliveryID: "delivery-1", TriggerReason: reason,
			})
			n.watchSendFrame = content
			excerpt = s.terminalNotificationExcerpt(n)
		case 3:
			n.JobID = ""
			n.Status = jobNotificationEventWatch
			excerpt = s.terminalNotificationExcerpt(n)
		case 4:
			excerpt = s.terminalNotificationExcerpt(n) // no durable record
		case 5:
			running, err := jm.createShell(createShellOpts{Command: "scripted"})
			if err != nil {
				t.Fatalf("create running shell: %v", err)
			}
			n.JobID = running.JobID
			n.Status = string(jobstore.StatusRunning)
			excerpt = s.terminalNotificationExcerpt(n)
		case 6:
			n.JobType = string(jobstore.JobDelegate)
			writeFinishedJobWithOutput(t, jm, rec.JobID, jobstore.JobDelegate, content)
			terminalWithOutput = true
			excerpt = s.terminalNotificationExcerpt(n)
		case 7:
			n.Status = jobNotificationEventWatch
			excerpt = s.terminalNotificationExcerpt(n)
		}
		if mode&8 != 0 {
			n.Status = ""
		}
		if mode&16 != 0 && mode%8 == 6 {
			excerpt.worktree = &delegateWorktreeReport{
				Path: "/tmp/lane", Branch: "serf/lane", Ahead: 2, Dirty: true,
			}
		}

		block := formatJobNotificationBlock(n, excerpt)
		if !utf8.ValidString(block) || !strings.HasPrefix(block, "<job-notification ") || !strings.HasSuffix(block, "</job-notification>") {
			t.Fatalf("malformed notification block: %q", block)
		}
		if terminalWithOutput && content != "" && excerpt.text == "" {
			t.Fatalf("terminal output was not excerpted: mode=%d len=%d", mode, len(content))
		}
		if !terminalWithOutput && excerpt != (notificationExcerpt{}) {
			t.Fatalf("watch notification received terminal excerpt: %+v", excerpt)
		}
		if got := notificationTranscriptRef(n); got == "" && n.JobID != "" {
			t.Fatal("job notification has no transcript reference fallback")
		}
		if mode&8 != 0 && n.WatchSend == nil && !strings.Contains(block, `event="running"`) {
			t.Fatalf("empty status did not render the default running event: %q", block)
		}
		if mode&16 != 0 && mode%8 == 6 {
			for _, want := range []string{`worktree_path="/tmp/lane"`, `worktree_branch="serf/lane"`, `worktree_ahead="2"`, `worktree_dirty="true"`} {
				if !strings.Contains(block, want) {
					t.Fatalf("worktree notification missing %q: %q", want, block)
				}
			}
		}
	})
}

type sndWindowProgram struct {
	head, tail jobReadOutputSnapshot
	headErr    error
	tailErr    error
	calls      []bool
}

func (p *sndWindowProgram) read(_ int, fromHead bool) (jobReadOutputSnapshot, error) {
	p.calls = append(p.calls, fromHead)
	if fromHead {
		return p.head, p.headErr
	}
	return p.tail, p.tailErr
}

// FuzzShellDigestReadProgram covers the one-read and two-read digest state
// machine, including both external read failures and overlapping line windows.
func FuzzShellDigestReadProgram(f *testing.F) {
	f.Add(uint8(0), "one line", "", uint8(5), uint8(5))
	f.Add(uint8(1), "a\nb\nc\nd\n", "", uint8(1), uint8(1))
	f.Add(uint8(2), "head-a\nhead-b\n", "tail-a\ntail-b\n", uint8(1), uint8(1))
	f.Add(uint8(3), "head\n", "tail\n", uint8(0), uint8(0))

	f.Fuzz(func(t *testing.T, mode uint8, head, tail string, headN, tailN uint8) {
		if len(head) > 4096 {
			head = head[:4096]
		}
		if len(tail) > 4096 {
			tail = tail[:4096]
		}
		p := &sndWindowProgram{
			head: jobReadOutputSnapshot{Content: head, TotalBytes: int64(len(head))},
			tail: jobReadOutputSnapshot{Content: tail, TotalBytes: int64(len(head) + len(tail)), DroppedBytes: int64(mode >> 5)},
		}
		switch mode % 4 {
		case 1:
			// Whole retained output; line counts decide overlap versus digest.
		case 2:
			p.head.Truncated = true
		case 3:
			p.head.Truncated = true
			p.tailErr = errors.New("scripted tail read")
		default:
			if mode&0x10 != 0 {
				p.headErr = errors.New("scripted head read")
			}
		}
		got, err := readJobOutputDigest(p.read, int(headN%8), int(tailN%8))
		if p.headErr != nil || p.tailErr != nil {
			if err == nil {
				t.Fatal("scripted read failure was swallowed")
			}
			return
		}
		if err != nil {
			t.Fatalf("readJobOutputDigest: %v", err)
		}
		if !utf8.ValidString(head) || !utf8.ValidString(tail) {
			return
		}
		if !utf8.ValidString(got.Content) {
			t.Fatalf("digest invalid UTF-8: %q", got.Content)
		}
		if len(p.calls) < 1 || !p.calls[0] {
			t.Fatalf("first read was not head: %v", p.calls)
		}
		if p.head.Truncated && len(p.calls) != 2 {
			t.Fatalf("truncated head reads = %v, want head+tail", p.calls)
		}
	})
}

// FuzzShellMarshalProgram drives result marshaling and the start-only context
// adapter. Settlement is a local recorder, so this target cannot create a job.
func FuzzShellMarshalProgram(f *testing.F) {
	f.Add(uint8(0), "ok", 0, 30000)
	f.Add(uint8(1), strings.Repeat("x", shellRideWholeBytes+1), 1, 30000)
	f.Add(uint8(2), "unicode-世界", 2, 800)
	f.Add(uint8(3), "", 3, 0)

	f.Fuzz(func(t *testing.T, flags uint8, output string, exitCode, maxChars int) {
		if len(output) > shellRideWholeBytes*2 {
			output = output[:shellRideWholeBytes*2]
		}
		maxChars %= 40000
		settles := make([]bool, 0, 1)
		res := shellResult{
			Type: string(jobstore.JobShell), Status: string(jobstore.StatusCompleted),
			Reason: "exit_zero", ExitCode: &exitCode, Output: output,
			TotalBytes: int64(len(output)), DroppedBytes: int64(flags >> 5),
		}
		if flags&1 != 0 {
			res.settle = func(keep bool) string {
				settles = append(settles, keep)
				if keep {
					return "job_kept"
				}
				return ""
			}
		} else {
			res.JobID = "job_running"
			res.RunningInBackground = flags&2 != 0
			res.Truncated = flags&4 != 0
			res.TimedOut = flags&8 != 0
		}
		got, err := marshalShellToolResult(res, maxChars)
		if err != nil {
			t.Fatalf("marshalShellToolResult: %v", err)
		}
		state, ok := got.State.(shellToolResult)
		if !ok {
			t.Fatalf("state type = %T", got.State)
		}
		if got.Output != formatShellResult(state) {
			t.Fatalf("render/state mismatch: %q %#v", got.Output, state)
		}
		if utf8.ValidString(output) && !utf8.ValidString(got.Output) {
			t.Fatalf("valid UTF-8 shell output rendered invalid UTF-8: %q", got.Output)
		}
		if res.settle != nil && len(settles) != 1 {
			t.Fatalf("settle calls = %v", settles)
		}

		reg := tool.NewRegistry()
		if err := reg.Register(tool.RegisteredTool{
			Tool:  llm.Tool{Definition: tool.DefShell()},
			Limit: schema.ToolOutputLimit{MaxChars: maxChars},
			Exec:  func(context.Context, execenv.ExecutionEnvironment, map[string]any) (any, error) { return "", nil },
		}); err != nil {
			t.Fatalf("register shell limit probe: %v", err)
		}
		enforceShellToolJSONLimit(reg)
		if got := shellToolResultMaxChars(reg); got < shellToolResultMinJSONChars {
			t.Fatalf("max chars = %d", got)
		}
		enforceShellToolJSONLimit(nil)
		emptyReg := tool.NewRegistry()
		enforceShellToolJSONLimit(emptyReg)
		_ = shellToolResultMaxChars(emptyReg)
		_ = shellToolResultMaxChars(nil)
		_ = jsonCharLen([]byte(output))
		_ = clampShellBlockTimeoutMS(exitCode)
		_ = shellFinalizeBackoff(exitCode & 15)

		key := struct{ name string }{"key"}
		parent := context.WithValue(context.Background(), key, "value")
		ctx, detach := newStartOnlyContext(parent)
		if ctx.Value(key) != "value" {
			t.Fatal("context value not forwarded")
		}
		_, _ = ctx.Deadline()
		_ = ctx.Done()
		_ = ctx.Err()
		if flags&0x10 != 0 {
			ctx.cancel(fmt.Errorf("scripted cancel"))
		} else {
			ctx.DetachAfterStart()
		}
		detach()
		_ = ctx.Err()
	})
}
