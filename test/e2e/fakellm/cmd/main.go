// Command fakellm stands up test/e2e/fakellm as a standalone provider so a
// disposable hub can hold a turn "running" for as long as a human or a
// browser-driving agent needs. Point a providers.toml instance at it and
// every session-loop round pauses for --hold seconds before answering with a
// tool call, up to --rounds rounds, then ends the turn with
// communicate(end_turn=true).
//
// Rounds are counted per session AND per turn, and held concurrently, so any
// number of sessions can share one fake and each gets the same behaviour:
// every turn it runs lasts --rounds rounds of its own. The count is tracked
// against the tool-call ids the fake mints, which is what keeps it right
// across a context compaction (a count derived from the replayed history
// rewinds when compaction folds that history away) and across a Stop (whose
// cancelled round leaves no trace, so the turn after it starts at one).
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
	rounds := flag.Int("rounds", 20, "tool-call rounds in each session's turn before ending it with communicate(end_turn=true)")
	jobRelease := flag.String("background-job-until", "", "answer each session's first round with a background shell job that waits for this file, then end the turn; creating the file wakes each idle session with a job-completion notification turn, which ends after one held round rather than launching another job. Give a name relative to the session's working directory -- the shell runs there, so a bare name needs no quoting and cannot be broken by a path with a space in it")
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

	live := newSessions()
	for {
		call, err := srv.Next(ctx.Done())
		if err != nil {
			log.Printf("fakellm: %v", err)
			return nil
		}
		answering.Go(func() { answer(ctx, live, call, hold, rounds, jobRelease, notesDir) })
	}
}

// sessionState is what the driver remembers about one session between its
// rounds. Both numbers the flags promise are per session AND per turn:
// --rounds ends each turn of each session after that many of ITS rounds, and
// --background-job-until gives each session one background job, on its first
// turn, rather than one per turn or one per process.
type sessionState struct {
	// lastAnswered is the tool-call id of the most recent round this driver
	// answered. A session replays the id of its last RECORDED round, so a
	// request arriving under an older id says the answer after it never
	// reached the transcript — see sessions.begin.
	lastAnswered string
	keys         []string // ids this session is filed under, oldest first
	turnRound    int      // rounds answered in the current turn, this one included
	launchedJob  bool     // --background-job-until: one job per session, on its first turn
}

// sessions tracks the live sessions by the tool-call ids the fake has given
// them.
//
// Nothing else in a chat-completions request identifies a session: the
// standalone driver's whole job is to serve several at once, and the script
// that ships it hands the operator ONE spawn call to paste repeatedly, so
// prompt, model and working directory are routinely identical. The ids are
// unique per call and a session replays its own, which makes them the one
// handle that is both stable and distinguishing.
//
// Deriving the round from the replayed history instead looks simpler until
// the history changes shape under it. Measured against a live hub and daemon:
// a session 15 rounds in, compacted from the browser, comes back replaying 3
// assistant turns — a derived count rewinds by 11 and --rounds silently stops
// meaning what the flag help says. The ids survive that compaction.
type sessions struct {
	mu   sync.Mutex
	byID map[string]*sessionState
}

func newSessions() *sessions { return &sessions{byID: map[string]*sessionState{}} }

// keysKept is how many of a session's recent ids stay in the map. More than
// one, because a round whose request is cancelled leaves no trace in the
// transcript and the session then comes back under an older id; three lets
// two rounds in a row be abandoned before the driver loses track of a session
// (and loses nothing worse than its round count).
const keysKept = 3

// begin returns the state of the session making this request, advanced to
// this round, and files it under the id this round's answer will carry. The
// round number and job flag are returned as values read under the lock:
// answer runs on its own goroutine and a Stop puts two of a session's rounds
// in flight at once, so reading state fields after the lock is released would
// race with the next round's begin.
//
// Two boundaries are read out of the request rather than assumed:
//
//   - No known id at all means a session this driver has not served: a new
//     one, or (only where compaction is configured to preserve no recent
//     turns) one whose entire history was folded away. Either way it starts a
//     fresh count, which is what the driver did for every session before it
//     tracked anything.
//   - A known id that is NOT the one this driver answered last means the
//     answer in between never reached the transcript. That is what a Stop
//     looks like from here: the operator cancels the in-flight model request,
//     the round is discarded, and the session's next request replays the last
//     round that was recorded. The turn it belonged to is over, so this
//     request opens a new one. (Measured against a live hub: a turn stopped
//     during round 2 comes back replaying round 1's id.)
func (s *sessions) begin(call *fakellm.Call) (state *sessionState, round int, launchedJob bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	previous := call.PreviousToolCallID()
	state = s.byID[previous] // a session's first round has no previous id
	switch {
	case state == nil:
		state = &sessionState{}
	case previous != state.lastAnswered:
		state.turnRound = 0 // the round after `previous` was abandoned
	}

	state.turnRound++
	state.lastAnswered = call.ToolCallID()
	s.byID[call.ToolCallID()] = state
	state.keys = append(state.keys, call.ToolCallID())
	for len(state.keys) > keysKept {
		delete(s.byID, state.keys[0])
		state.keys = state.keys[1:]
	}
	return state, state.turnRound, state.launchedJob
}

// endedTurn records that the answer to `call` ends the session's turn, so its
// next round is round 1 of a new turn.
//
// It applies nothing unless `call` is still the round the session will replay.
// A cancelled round's goroutine can reach here after the session's next turn
// has already begun (its answer was discarded, so the session did not wait for
// it); zeroing the count then would hand that turn extra rounds. By the time
// that happens the new turn's begin has moved lastAnswered on, which is how
// the stale write is recognised.
func (s *sessions) endedTurn(state *sessionState, call *fakellm.Call) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if state.lastAnswered != call.ToolCallID() {
		return
	}
	state.turnRound = 0
}

// launchedJob records that the answer to `call` carries the session's one
// background job. Guarded like endedTurn: a round the session has already
// abandoned must not mark a job it will never see as launched.
func (s *sessions) launchedJob(state *sessionState, call *fakellm.Call) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if state.lastAnswered != call.ToolCallID() {
		return
	}
	state.launchedJob = true
}

// answer holds one round for --hold and then replies to it. A round whose
// request is cancelled while it is held — what a Stop does — is dropped
// without touching the session's state: the session never records it, so the
// turn that follows must find its count exactly where the last RECORDED round
// left it.
func answer(ctx context.Context, live *sessions, call *fakellm.Call, hold time.Duration, rounds int, jobRelease, notesDir string) {
	state, round, launchedJob := live.begin(call)
	log.Printf("--- round %d: holding %s ---\n%s", round, hold, strings.Join(call.Texts(), "\n"))

	endTurn := func(message string) {
		live.endedTurn(state, call)
		call.RespondToolCall("communicate", map[string]any{
			"message":  message,
			"end_turn": true,
			"output":   map[string]any{"message": "", "data": map[string]any{}, "artifacts": []any{}},
		})
	}

	// The session's first turn launches a background job that blocks on a file
	// and then ends, so the session sits idle holding the job. Creating the
	// file completes it, and the completion wakes the session with a
	// notification turn -- the turn kind that has no input of its own, and the
	// whole reason this mode exists. That turn ends on its first round: it is
	// there to be watched arriving, and a second background job would only
	// complete instantly against the file that is now there and wake the
	// session again, forever.
	if jobRelease != "" {
		switch {
		case !launchedJob && round == 1:
			log.Printf("round 1: launching a background job that waits for %s", jobRelease)
			live.launchedJob(state, call)
			call.RespondToolCall("shell", map[string]any{
				"command": "while [ ! -f " + jobRelease + " ]; do sleep 0.5; done",
				"mode":    "background",
			})
			return
		case launchedJob && round == 2:
			log.Printf("round 2: ending the turn so the session goes idle holding the job")
			endTurn("background job launched; idle until it finishes")
			return
		case launchedJob && round == 1:
			select {
			case <-time.After(hold):
			case <-ctx.Done():
				return
			case <-call.Cancelled():
				return
			}
			log.Printf("round 1 of a later turn: ending it so the session goes back to idle")
			endTurn("job-completion notification seen; going back to idle")
			return
		}
	}

	select {
	case <-time.After(hold):
	case <-ctx.Done():
		return
	case <-call.Cancelled():
		return
	}

	if round%rounds == 0 {
		log.Printf("round %d: ending the turn", round)
		endTurn(fmt.Sprintf("fake provider ended the turn after %d rounds", round))
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
		live.endedTurn(state, call)
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
