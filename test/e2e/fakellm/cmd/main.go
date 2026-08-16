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
	"syscall"
	"time"

	"primeradiant.com/serf/test/e2e/fakellm"
)

func main() {
	hold := flag.Duration("hold", 15*time.Second, "how long to hold each model round before answering")
	rounds := flag.Int("rounds", 20, "tool-call rounds per turn before ending it with communicate(end_turn=true)")
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
	if err := run(flag.Arg(0), *hold, *rounds); err != nil {
		log.Printf("fakellm: %v", err)
		os.Exit(1)
	}
}

func run(addr string, hold time.Duration, rounds int) error {
	srv, err := fakellm.NewOn(addr)
	if err != nil {
		return err
	}
	defer srv.Close()
	// Bind first, THEN log the address — the caller may have passed
	// "127.0.0.1:0" and needs the port the kernel actually handed back.
	log.Printf("fakellm listening on %s (base_url %s)", srv.Addr(), srv.BaseURL())

	notesDir, err := os.MkdirTemp("", "fakellm-notes-")
	if err != nil {
		return fmt.Errorf("create notes dir: %w", err)
	}
	defer os.RemoveAll(notesDir) //nolint:errcheck // throwaway fixture directory

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	round := 0
	for {
		call, err := srv.Next(ctx.Done())
		if err != nil {
			log.Printf("fakellm: %v", err)
			return nil
		}
		round++
		log.Printf("--- round %d: holding %s ---\n%s", round, hold, strings.Join(call.Texts(), "\n"))

		select {
		case <-time.After(hold):
		case <-ctx.Done():
			return nil
		}

		if round%rounds == 0 {
			log.Printf("round %d: ending the turn", round)
			call.RespondToolCall("communicate", map[string]any{
				"message":  fmt.Sprintf("fake provider ended the turn after %d rounds", round),
				"end_turn": true,
				"output":   map[string]any{"message": "", "data": map[string]any{}, "artifacts": []any{}},
			})
			continue
		}
		// Keep the loop going with a harmless read. The path varies per round
		// so serf's repeated-identical-failure breaker never trips and the
		// transcript stays readable.
		path, err := stageNote(notesDir, round)
		if err != nil {
			return fmt.Errorf("stage note for round %d: %w", round, err)
		}
		call.RespondToolCall("read_file", map[string]any{"file_path": path})
	}
}

// stageNote writes the small text file this round's read_file call will read.
func stageNote(dir string, round int) (string, error) {
	path := filepath.Join(dir, fmt.Sprintf("round-%d.txt", round))
	return path, os.WriteFile(path, fmt.Appendf(nil, "round %d notes\n", round), 0o600)
}
