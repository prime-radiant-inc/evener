package fakellm_test

import (
	"context"
	"fmt"
	"time"

	"primeradiant.com/serf/test/e2e/fakellm"
)

// Example is the package's own documentation for holding a turn open: the
// turn stays in flight for exactly as long as the test declines to answer the
// model request. It lives here rather than in a doc comment so the compiler
// checks it — a doc snippet with the wrong signature is wrong at the moment a
// new test author trusts it most (kata cg10).
//
// It has no "Output:" comment, so `go test` compiles it and does not run it:
// there is no session on the other end to make the request it waits for.
func Example() {
	srv, err := fakellm.New()
	if err != nil {
		panic(err)
	}
	defer srv.Close()

	// Point a providers.toml instance at srv.BaseURL() and spawn a session
	// against "<instance>/" + fakellm.ModelID; everything below is that
	// session's own loop, one round at a time.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	call, err := srv.Next(ctx.Done()) // the session's first model request --
	if err != nil {                   // the turn is now genuinely in flight
		panic(err) // until this test responds
	}
	call.RespondToolCall("read_file", map[string]any{"file_path": "AGENTS.md"})

	next, err := srv.Next(ctx.Done()) // the round after the tool ran; its
	if err != nil {                   // messages carry anything steered in
		panic(err)
	}
	fmt.Println(next.Contains("AGENTS.md"))
}
