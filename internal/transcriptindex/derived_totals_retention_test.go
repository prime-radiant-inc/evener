package transcriptindex

import (
	"testing"

	"primeradiant.com/evener/agent/transcript"
)

// TestBuilderReleasesFailureNamesAfterABuild pins the bounded-memory rule the
// builder already applies to builder.global: once a build completes, the
// failure counter retains no call names, so a long-lived index does not hold one
// entry per tool call of the whole session.
func TestBuilderReleasesFailureNamesAfterABuild(t *testing.T) {
	header := transcript.Header{SessionID: "s", CreatedAt: fixtureClock, ProfileID: "p", Model: "m"}
	lines := []fixtureLine{entryLine(user("q"))}
	for i := range 50 {
		id := string(rune('a'+i%26)) + string(rune('0'+i/26))
		lines = append(lines,
			entryLine(assistant(text("run"), call(id, "shell", `{}`))),
			entryLine(results(result(id, "shell", "ok"))),
		)
	}
	fx := fixture{name: "many-calls", header: header, lines: lines}

	path := writeFixture(t, fx)
	x := openIndex(t, path, t.TempDir())
	defer x.Close() //nolint:errcheck // read-only test handle

	if n := x.builder.failures.Len(); n != 0 {
		t.Fatalf("builder retained %d call names after a build, want 0", n)
	}
}
