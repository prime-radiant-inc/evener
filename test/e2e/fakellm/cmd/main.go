// Command fakellm stands up test/e2e/fakellm as a standalone provider so a
// disposable hub can hold a turn "running" for as long as a human or a
// browser-driving agent needs. Point a providers.toml instance at it and
// every session-loop round pauses for --hold seconds before answering with a
// tool call, up to --rounds rounds, then ends the turn with
// communicate(end_turn=true).
//
// WHY: the mid-turn controls (Steer, Send-while-busy, Stop) can only be
// exercised while a turn is genuinely in flight. Against a real provider that
// window is a few seconds and needs an AGENTS.md pacing prompt to widen (see
// docs/agentic-testing.md); here it is a flag, costs nothing, and needs no
// credential.
//
// Every round's messages are printed to stderr, so the operator can see
// exactly what reached the model — a steer that arrived shows up as a user
// message in the following round.
//
// Usage:
//
//	fakellm [--hold 15s] [--rounds 20] <listen-addr>
//
// <listen-addr> is host:port. Use 127.0.0.1:0 to let the kernel assign a free
// port (kata 68fm) and read the real one back from the "fakellm listening on
// ..." line printed to stderr.
//
// See scripts/e2e-webui-turn-controls.sh for the full HOME-isolated hub
// launch recipe this fixture is built for.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"primeradiant.com/serf/test/e2e/fakellm"
)

func main() {
	hold := flag.Duration("hold", 15*time.Second, "how long to hold each model round before answering")
	rounds := flag.Int("rounds", 20, "tool-call rounds per turn before ending it with communicate(end_turn=true)")
	jobRelease := flag.String("background-job-until", "", "answer the first round with a background shell job that waits for this file, then end the turn; creating the file wakes the idle session with a job-completion notification. Give a name relative to the session's working directory -- the shell runs there, so a bare name needs no quoting and cannot be broken by a path with a space in it")
	flag.Usage = func() {
		// Flags first: Go's flag package stops parsing at the first non-flag
		// argument, so "fakellm 127.0.0.1:0 --hold 30s" silently runs with the
		// defaults. Say the working order here rather than the wrong one.
		fmt.Fprintln(os.Stderr, "usage: fakellm [--hold 15s] [--rounds 20] <listen-addr>")
		flag.PrintDefaults()
	}
	flag.Parse()
	if flag.NArg() != 1 {
		flag.Usage()
		os.Exit(2)
	}
	if *rounds < 1 {
		fmt.Fprintf(os.Stderr, "fakellm: --rounds must be at least 1, got %d\n", *rounds)
		os.Exit(2)
	}
	// run owns every defer; main only reports. log.Fatalf here would skip
	// them (gocritic exitAfterDefer).
	if err := run(flag.Arg(0), *hold, *rounds, *jobRelease); err != nil {
		log.Printf("fakellm: %v", err)
		os.Exit(1)
	}
}

func run(addr string, hold time.Duration, rounds int, jobRelease string) error {
	srv, err := fakellm.NewOn(addr)
	if err != nil {
		return err
	}
	defer srv.Close()
	// Bind first, THEN log the address — the caller may have passed
	// "127.0.0.1:0" and needs the port the kernel actually handed back.
	log.Printf("fakellm listening on %s (base_url %s)", srv.Addr(), srv.BaseURL())

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	return serve(ctx, srv, hold, rounds, jobRelease)
}

// serve answers every round the fake receives until ctx is cancelled or the
// server closes. It is separate from run so a test can drive it on a server
// it holds itself.
//
// Each round is answered on its own goroutine. A round is HELD before it is
// answered, so a loop that held them one at a time would leave every other
// session's request unread — not merely unanswered — for the whole of the
// current hold, and --hold is measured in tens of seconds. Buffering the
// server's call channel would not help: the wait is the driver's, not the
// channel's.
func serve(ctx context.Context, srv *fakellm.Server, hold time.Duration, rounds int, jobRelease string) error {
	notesDir, err := os.MkdirTemp("", "fakellm-notes-")
	if err != nil {
		return fmt.Errorf("create notes dir: %w", err)
	}
	defer os.RemoveAll(notesDir) //nolint:errcheck // throwaway fixture directory

	// Declared after the RemoveAll so it runs BEFORE it: no round may still be
	// staging a note in a directory that is being deleted.
	var answering sync.WaitGroup
	defer answering.Wait()

	for {
		call, err := srv.Next(ctx.Done())
		if err != nil {
			log.Printf("fakellm: %v", err)
			return nil
		}
		answering.Go(func() { answer(ctx, call, hold, rounds, jobRelease, notesDir) })
	}
}

// answer holds one round for --hold and then replies to it. The round number
// is the session's own (fakellm.Call.Round), never a count of everything this
// process has served: --rounds is a promise about how long ONE session stays
// in a turn, and --background-job-until a promise about what EACH session's
// first two rounds do.
func answer(ctx context.Context, call *fakellm.Call, hold time.Duration, rounds int, jobRelease, notesDir string) {
	round := call.Round()
	log.Printf("--- round %d: holding %s ---\n%s", round, hold, strings.Join(call.Texts(), "\n"))

	// Round 1 launches a background job that blocks on a file, round 2 ends
	// the turn. The session then sits idle until someone creates that file,
	// and the job's completion wakes it with a notification -- the turn kind
	// that has no input of its own.
	if jobRelease != "" {
		switch round {
		case 1:
			log.Printf("round 1: launching a background job that waits for %s", jobRelease)
			call.RespondToolCall("shell", map[string]any{
				"command": "while [ ! -f " + jobRelease + " ]; do sleep 0.5; done",
				"mode":    "background",
			})
			return
		case 2:
			log.Printf("round 2: ending the turn so the session goes idle")
			call.RespondToolCall("communicate", map[string]any{
				"message":  "background job launched; idle until it finishes",
				"end_turn": true,
				"output":   map[string]any{"message": "", "data": map[string]any{}, "artifacts": []any{}},
			})
			return
		}
	}

	select {
	case <-time.After(hold):
	case <-ctx.Done():
		return
	}

	if round%rounds == 0 {
		log.Printf("round %d: ending the turn", round)
		call.RespondToolCall("communicate", map[string]any{
			"message":  fmt.Sprintf("fake provider ended the turn after %d rounds", round),
			"end_turn": true,
			"output":   map[string]any{"message": "", "data": map[string]any{}, "artifacts": []any{}},
		})
		return
	}
	// Keep the loop going with a harmless read. The path varies per round so
	// serf's repeated-identical-failure breaker never trips and the transcript
	// stays readable.
	path, err := stageNote(notesDir)
	if err != nil {
		// Answering with text ends this session's turn. Leaving the round
		// unanswered would hang it instead, with the reason only in this log.
		log.Printf("round %d: stage note: %v", round, err)
		call.RespondText(fmt.Sprintf("fake provider could not stage a note for round %d: %v", round, err))
		return
	}
	call.RespondToolCall("read_file", map[string]any{"file_path": path})
}

// noteSeq numbers the staged notes. Two sessions can be on the same round at
// the same time, so the round number alone would have them writing one file
// from two goroutines.
var noteSeq atomic.Int64

// stageNote writes the small text file this round's read_file call will read.
func stageNote(dir string) (string, error) {
	note := noteSeq.Add(1)
	path := filepath.Join(dir, fmt.Sprintf("note-%d.txt", note))
	return path, os.WriteFile(path, fmt.Appendf(nil, "note %d\n", note), 0o600)
}
