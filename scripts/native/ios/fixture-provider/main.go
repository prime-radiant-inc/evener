//go:build linux || darwin

// Command fixture-provider serves the scripted loopback LLM boundary for the
// native UI qualification journey: every ordinary session round is answered
// with the fixed qualification fixture through the agent's own result tool,
// so the installed app's rendering, draft and recovery behavior is driven by
// known text instead of a live provider. It writes its endpoint to a private
// readiness file and stops when cancelled. No provider option is added to
// production code; this is qualification tooling only.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"primeradiant.com/evener/test/e2e/fakellm"
)

// qualificationFixture is the fixed answer to every ordinary session round.
// The markers are asserted by the UI test and by this package's own boundary
// test; the emphasis and the fenced code exercise the enriched native
// Markdown renderer.
const qualificationFixture = "QUALIFICATION_prose_a7\n\n**QUALIFICATION_emphasis_b8**\n\n```text\nQUALIFICATION_code_c9\n```\n"

func main() {
	os.Exit(provide())
}

func provide() int {
	readyFile := flag.String("ready-file", "", "absolute path of the private readiness file that names the provider endpoint")
	flag.Usage = func() {
		_, _ = fmt.Fprintln(flag.CommandLine.Output(), "Usage: fixture-provider --ready-file ABSOLUTE_FILE\nServes the scripted loopback LLM boundary for the native UI qualification journey. Writes private readiness JSON naming its endpoint, answers every ordinary session round with the fixed fixture, and stops on cancellation.")
		flag.PrintDefaults()
	}
	flag.Parse()
	if *readyFile == "" || !filepath.IsAbs(*readyFile) {
		flag.Usage()
		return 2
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, *readyFile); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return 0
}

// run serves the provider until ctx is cancelled and then returns nil once the
// listener has stopped. The readiness file is the runner's only way to learn
// the endpoint, so it stays private to the run.
func run(ctx context.Context, readyFile string) error {
	server, err := fakellm.New()
	if err != nil {
		return err
	}
	endpoint, err := json.Marshal(struct {
		BaseURL string `json:"base_url"`
	}{BaseURL: server.BaseURL()})
	if err != nil {
		server.Close()
		return err
	}
	if err := os.WriteFile(readyFile, endpoint, 0o600); err != nil {
		server.Close()
		return err
	}
	for {
		call, err := server.Next(ctx.Done())
		if err != nil {
			// Cancellation is the provider's normal shutdown; every other
			// stop is a failure the runner must see.
			server.Close()
			if ctx.Err() != nil {
				return nil //nolint:nilerr // the runner's cancellation is this provider's clean exit
			}
			return err
		}
		// The fixture answers as the agent's user-facing message: a
		// bare-text reply bypasses the result tool, and the daemon steers
		// each such round ("keep working") until the bare-text budget fails
		// the session mid-journey. The communicate call with end_turn is
		// the product's path for exactly this text, so the turn completes
		// once and the markers render through the ordinary message
		// pipeline.
		call.RespondToolCall("communicate", map[string]any{
			"message":  qualificationFixture,
			"end_turn": true,
		})
	}
}
