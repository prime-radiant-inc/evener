//go:build browserguard

package hub

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fsnotify/fsnotify"
	"golang.org/x/sys/unix"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubedge"
	"primeradiant.com/evener/test/e2e/fakellm"
)

// Capture the explicit subprocess opt-in before TestMain redirects EVENER_*.
var pagingProducerEnabled = os.Getenv("EVENER_JOB_OUTPUT_PAGING_PRODUCER") == "1"

func pagingProducerRows(first, count int) []byte {
	var output bytes.Buffer
	fill := bytes.Repeat([]byte{'x'}, 112)
	for row := first; row < first+count; row++ {
		fmt.Fprintf(&output, "ROW_%08d ", row)
		output.Write(fill)
		output.WriteByte('\n')
	}
	return output.Bytes()
}

func writePagingPhase(stdout, oracle io.Writer, content []byte) error {
	for _, writer := range []io.Writer{oracle, stdout} {
		written, err := writer.Write(content)
		if err != nil {
			return err
		}
		if written != len(content) {
			return io.ErrShortWrite
		}
	}
	return nil
}

func TestJobOutputPagingProducerProcess(t *testing.T) {
	if !pagingProducerEnabled {
		t.Skip("only the real shell fixture opts into this subprocess")
	}
	oracle, err := os.Create(os.Getenv("PAGING_ORACLE"))
	if err != nil {
		t.Fatal(err)
	}
	defer oracle.Close()
	total, phase := 0, 0
	write := func(command string, content []byte) {
		t.Helper()
		if err := writePagingPhase(os.Stdout, oracle, content); err != nil {
			t.Fatal(err)
		}
		if err := oracle.Sync(); err != nil {
			t.Fatal(err)
		}
		total += len(content)
		phase++
		body, err := json.Marshal(map[string]any{"phase": phase, "command": command, "totalBytes": total})
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(os.Getenv("PAGING_ACK_DIR"), fmt.Sprintf("phase-%02d.json", phase)), body, 0600); err != nil {
			t.Fatal(err)
		}
	}
	initial := pagingProducerRows(0, 9000)
	initial = append(initial, []byte("LONG_BEGIN_")...)
	initial = append(initial, bytes.Repeat([]byte{'L'}, 600*1024)...)
	initial = append(initial, []byte("_LONG_END\n")...)
	initial = append(initial, bytes.Repeat([]byte("\x1b[0m"), 32768)...)
	initial = append(initial, pagingProducerRows(9000, 9000)...)
	initial = append(initial, []byte("SPLIT_é_\xf0\x9f")...)
	write("initial", initial)
	pipe, err := os.Open(os.Getenv("PAGING_FIFO"))
	if err != nil {
		t.Fatal(err)
	}
	defer pipe.Close()
	commands := bufio.NewScanner(pipe)
	grown := false
	growRow := 25000
	for commands.Scan() {
		switch command := commands.Text(); command {
		case "grow":
			if !grown {
				write(command, []byte("\x98\x80\n\x1b[31mSGR_RED_é\x1b[0m\n\x1b]0;hidden-title\aOSC_VISIBLE\nMALFORMED_\xff\nGROW_MARKER_1\n"))
				grown = true
			} else {
				write(command, append(pagingProducerRows(growRow, 400), []byte("GROW_MARKER_2\n")...))
				growRow += 400
			}
		case "middle":
			write(command, append(pagingProducerRows(18000, 7000), []byte("MIDDLE_MARKER\n")...))
		case "rollover":
			write(command, append(pagingProducerRows(29000, 80000), []byte("ROLLOVER_MARKER\n")...))
		case "finish":
			write(command, []byte("FINAL_AFTER_PENDING_READ_é_😀\n"))
			if err := oracle.Close(); err != nil {
				t.Fatal(err)
			}
			// The shell output is the oracle alone, without the test runner's PASS.
			os.Exit(0)
		default:
			t.Fatalf("unknown producer command %q", command)
		}
	}
	if err := commands.Err(); err != nil {
		t.Fatal(err)
	}
	t.Fatal("producer command pipe closed before finish")
}

func pagingControlCommand(command string) (string, bool) {
	switch command {
	case "output-grow":
		return "grow", true
	case "output-middle":
		return "middle", true
	case "output-rollover":
		return "rollover", true
	case "output-finish":
		return "finish", true
	default:
		return "", false
	}
}

// The watcher is armed before the initial scan, so an acknowledgement cannot
// fall into a scan/subscribe gap. The context is only a hang tripwire.
func pagingAwaitFile(ctx context.Context, watcher *fsnotify.Watcher, name string) ([]byte, error) {
	for {
		body, err := os.ReadFile(name)
		if err == nil && len(body) > 0 && json.Valid(body) {
			return body, nil
		}
		if err != nil && !os.IsNotExist(err) {
			return nil, err
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case err := <-watcher.Errors:
			return nil, err
		case <-watcher.Events:
		}
	}
}

func runJobOutputPagingJourney(t *testing.T, ctx context.Context, stack *hubStack, provider *fakellm.Server) {
	t.Helper()
	artifacts, err := os.MkdirTemp(backgroundJobsArtifactRoot, "joboutputpagingguard-")
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("job output paging evidence: %s", artifacts)
	for _, name := range []string{"control.jsonl", "milestones.jsonl"} {
		if err := os.WriteFile(filepath.Join(artifacts, name), nil, 0600); err != nil {
			t.Fatal(err)
		}
	}
	fifoPath := filepath.Join(artifacts, "producer.fifo")
	if err := unix.Mkfifo(fifoPath, 0600); err != nil {
		t.Fatal(err)
	}
	fifo, err := os.OpenFile(fifoPath, os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer fifo.Close()
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		t.Fatal(err)
	}
	defer watcher.Close()
	if err := watcher.Add(artifacts); err != nil {
		t.Fatal(err)
	}
	client := stack.dialRPC(ctx, t)
	rounds := 20
	started, err := client.ThreadStart(ctx, appwire.ThreadStartParams{
		Harness: "evener", CWD: stack.workDir, Model: stack.model,
		Input:           []appwire.InputItem{{Type: "text", Text: "JOB_OUTPUT_PAGING_PRODUCER"}},
		LaunchOverrides: &appwire.LaunchConfigLayer{Sandbox: "off", MaxRounds: &rounds},
	})
	if err != nil {
		t.Fatal(err)
	}
	ref := threadRef(started.Thread)
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = client.ThreadShutdown(cleanup, appwire.ThreadShutdownParams{Ref: ref})
	})
	if _, err := client.ThreadRead(ctx, appwire.ThreadReadParams{Ref: ref, Subscribe: true}); err != nil {
		t.Fatal(err)
	}
	call, err := provider.Next(ctx.Done())
	if err != nil {
		t.Fatal(err)
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	oracle := filepath.Join(artifacts, "oracle.bin")
	call.RespondToolCall("shell", map[string]any{
		"command": fmt.Sprintf("EVENER_JOB_OUTPUT_PAGING_PRODUCER=1 PAGING_ORACLE=%q PAGING_ACK_DIR=%q PAGING_FIFO=%q %q -test.run='^TestJobOutputPagingProducerProcess$'", oracle, artifacts, fifoPath, exe),
		"mode":    "background", "description": "Byte paging producer", "intent": "Producing literal bytes through the real shell and owner route",
	})
	call, err = provider.Next(ctx.Done())
	if err != nil {
		t.Fatal(err)
	}
	call.RespondToolCall("communicate", communicateArgs("JOB_OUTPUT_PAGING_READY"))
	noticeCtx, stopNotices := context.WithCancel(ctx)
	defer stopNotices()
	noticeDone := make(chan struct{})
	go func() {
		defer close(noticeDone)
		for {
			call, err := provider.Next(noticeCtx.Done())
			if err != nil {
				return
			}
			call.RespondToolCall("communicate", communicateArgs("JOB_OUTPUT_PAGING_NOTICE"))
		}
	}()
	defer func() { stopNotices(); <-noticeDone }()
	if _, err := pagingAwaitFile(ctx, watcher, filepath.Join(artifacts, "phase-01.json")); err != nil {
		t.Fatal(err)
	}
	jobs := backgroundJobsAwait(ctx, t, client, ref, func(rows []appwire.JobActivityJob) bool { return len(rows) == 1 })
	jobID := jobs[0].JobID
	controlCtx, stopControls := context.WithCancel(ctx)
	defer stopControls()
	controlDone := make(chan error, 1)
	go func() {
		seen, phase := 0, 1
		for {
			body, err := os.ReadFile(filepath.Join(artifacts, "control.jsonl"))
			if err != nil {
				controlDone <- err
				return
			}
			if end := bytes.LastIndexByte(body, '\n'); end >= 0 {
				lines := bytes.Split(body[:end], []byte{'\n'})
				for ; seen < len(lines); seen++ {
					var record struct {
						Command string `json:"command"`
					}
					if err := json.Unmarshal(lines[seen], &record); err != nil {
						controlDone <- err
						return
					}
					command, ok := pagingControlCommand(record.Command)
					if !ok {
						controlDone <- fmt.Errorf("unknown paging control %q", record.Command)
						return
					}
					if _, err := fmt.Fprintln(fifo, command); err != nil {
						controlDone <- err
						return
					}
					phase++
					ack, err := pagingAwaitFile(controlCtx, watcher, filepath.Join(artifacts, fmt.Sprintf("phase-%02d.json", phase)))
					if err != nil {
						controlDone <- err
						return
					}
					var result struct {
						Command string `json:"command"`
					}
					if json.Unmarshal(ack, &result) != nil || result.Command != command {
						controlDone <- fmt.Errorf("producer acknowledgement mismatch: %s", ack)
						return
					}
				}
			}
			select {
			case <-controlCtx.Done():
				controlDone <- nil
				return
			case err := <-watcher.Errors:
				controlDone <- err
				return
			case <-watcher.Events:
			}
		}
	}()
	defer func() {
		stopControls()
		if err := <-controlDone; err != nil {
			t.Error(err)
		}
	}()
	fixture := map[string]any{
		"journey": "output-paging", "url": hubedge.AuthURLFor("http://"+stack.addr, stack.token),
		"artifactDir": artifacts, "controlPath": filepath.Join(artifacts, "control.jsonl"), "milestonePath": filepath.Join(artifacts, "milestones.jsonl"),
		"rootRef": ref, "outputOwnerRef": ref, "outputJobId": jobID, "outputOraclePath": oracle,
	}
	body, err := json.Marshal(fixture)
	if err != nil {
		t.Fatal(err)
	}
	log, err := os.Create(filepath.Join(artifacts, "driver.log"))
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close()
	driver := exec.CommandContext(ctx, "node", "frontend/scripts/backgroundjobsguard/run.mjs")
	driver.Stdin, driver.Stdout, driver.Stderr = bytes.NewReader(body), log, log
	driver.WaitDelay = 15 * time.Second
	if err := driver.Run(); err != nil {
		skillGuardLogDriverTail(t, log.Name())
		t.Fatalf("real output paging journey: %v, evidence: %s", err, artifacts)
	}
	milestones, err := os.ReadFile(filepath.Join(artifacts, "milestones.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	seenMilestones := make(map[string]bool)
	for _, line := range strings.Split(string(milestones), "\n") {
		var entry struct {
			Milestone string `json:"milestone"`
		}
		if json.Unmarshal([]byte(line), &entry) == nil {
			seenMilestones[entry.Milestone] = true
		}
	}
	for _, want := range []string{"paging-bottom", "paging-history", "paging-refetch", "paging-gap", "paging-text", "paging-live-budget", "paging-refresh", "paging-reconnect", "paging-pruned", "paging-final-drain", "paging-post-errors"} {
		if !seenMilestones[want] {
			t.Errorf("missing paging milestone %s", want)
		}
	}
}
