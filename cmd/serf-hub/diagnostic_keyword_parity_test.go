package main

import (
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"

	"primeradiant.com/serf/agent/diagnostic"
)

// turnFailurePath is the web client's copy of the hub-failure vocabulary. It is
// a second implementation of the same decision, not a second consumer of one:
// Go classifies a message to stamp source/title/hint on the wire, and this file
// classifies the same message to choose between "Retry" and "Reconnect & retry"
// on the failed turn's end-cap.
const turnFailurePath = "frontend/src/panes/session/transcript/turnFailure.ts"

// reconnectKeywordsRE lifts the RECONNECT_KEYWORDS array literal out of
// turnFailure.ts. Parsing beats generating here: the list is ten short lowercase
// strings, and a generator would cost a command, a go:generate directive, a
// committed artifact and a Makefile entry to keep them in step — while turning a
// file a frontend engineer can read and edit into one they may not touch.
var reconnectKeywordsRE = regexp.MustCompile(`(?s)const RECONNECT_KEYWORDS = \[(.*?)\]`)

var reconnectKeywordRE = regexp.MustCompile(`"([^"]*)"`)

// TestHubFailureKeywordsMatchWebClient fails when the Go and TypeScript
// hub-failure vocabularies drift apart.
//
// They had drifted, in both directions, and the TS-only half was doing visible
// damage: "local daemon unavailable" (cmd/serf-hub/internal/appsource's dial
// failure, seven sites) matched no Go keyword, so Go titled it "Serf error" and
// hinted "Check the Serf session log and daemon state" — while the web client
// matched it, chipped it "connection" and offered "Reconnect & retry". One card
// carried both, and the hint was the wrong advice: the Serf daemon may never
// have started, which is why the dial failed.
func TestHubFailureKeywordsMatchWebClient(t *testing.T) {
	web := readReconnectKeywords(t)
	got := slices.Sorted(slices.Values(web))
	want := slices.Sorted(slices.Values(diagnostic.HubFailureKeywords))
	if !slices.Equal(got, want) {
		t.Errorf("hub-failure vocabulary drifted.\n  Go only: %q\n  %s only: %q\nAdd the missing terms to both, or delete them from both.",
			missing(want, got), turnFailurePath, missing(got, want))
	}
}

// TestHubFailureKeywordsAreMatchable guards the two properties each side's
// matcher silently depends on: Go lowercases the message before its
// strings.Contains sweep (an uppercase keyword would never fire), and the web
// client joins the list into a RegExp with "|" (a keyword carrying a regex
// metacharacter would quietly mean something else, or throw).
func TestHubFailureKeywordsAreMatchable(t *testing.T) {
	for _, keyword := range diagnostic.HubFailureKeywords {
		if keyword != strings.ToLower(keyword) {
			t.Errorf("keyword %q is not lowercase; isHubFailure matches an already-lowercased message and would never fire", keyword)
		}
		if regexp.QuoteMeta(keyword) != keyword {
			t.Errorf("keyword %q carries a regex metacharacter; turnFailure.ts joins these into a RegExp with %q", keyword, "|")
		}
	}
}

// readReconnectKeywords parses turnFailure.ts's RECONNECT_KEYWORDS array. A
// parse that finds nothing is a failure, never a silent pass: the array being
// unfindable is exactly the refactor this test exists to notice.
func readReconnectKeywords(t *testing.T) []string {
	t.Helper()
	source, err := os.ReadFile(turnFailurePath)
	if err != nil {
		t.Fatalf("read %s: %v", turnFailurePath, err)
	}
	body := reconnectKeywordsRE.FindSubmatch(source)
	if body == nil {
		t.Fatalf("%s: no `const RECONNECT_KEYWORDS = [...]` array found — if it was renamed or reshaped, update this test so the two vocabularies stay tied together", turnFailurePath)
	}
	var keywords []string
	for _, match := range reconnectKeywordRE.FindAllSubmatch(body[1], -1) {
		keywords = append(keywords, string(match[1]))
	}
	if len(keywords) == 0 {
		t.Fatalf("%s: RECONNECT_KEYWORDS parsed as empty", turnFailurePath)
	}
	return keywords
}

// missing returns the entries of a that are absent from b.
func missing(a, b []string) []string {
	var out []string
	for _, item := range a {
		if !slices.Contains(b, item) {
			out = append(out, item)
		}
	}
	return out
}
