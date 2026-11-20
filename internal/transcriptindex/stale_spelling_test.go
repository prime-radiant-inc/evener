package transcriptindex

import (
	"bytes"
	"os"
	"strconv"
	"testing"

	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/llm"
)

// TestStaleSidecarRebuildsForDamagedArgumentsSpelling pins the stale-index
// half: a sidecar an earlier reader persisted, after rejecting the object-form
// arguments_spelling, must be rebuilt rather than served once this reader can
// read the record, and the rebuilt index must serve the intact structured
// arguments.
func TestStaleSidecarRebuildsForDamagedArgumentsSpelling(t *testing.T) {
	const placeholder = "DAMAGED_SPELLING_PLACEHOLDER"
	const arguments = `{"path":"a.png","intent":"read a.png"}`
	const spelling = `{"sha256":17,"edits":[{"at":1.5,"bytes":"!!not-base64!!"}]}`
	turn := assistant(call("call_spelling", "read_file", arguments))
	turn.Message.Content[0].ToolCall.ArgumentsSpelling = llm.Spelling(placeholder)
	path := writeFixture(t, fixture{
		name:   "damaged spelling",
		header: transcript.Header{SessionID: "damaged-spelling", CreatedAt: fixtureClock, ProfileID: "p", Model: "m"},
		lines:  []fixtureLine{entryLine(user("read it")), entryLine(turn)},
	})

	// Swap the writer's string spelling for an object-form value with every
	// optional field damaged: digest, edit offset and edit payload.
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	token := []byte(`"arguments_spelling":"` + placeholder + `"`)
	if n := bytes.Count(data, token); n != 1 {
		t.Fatalf("fixture carries %d spelling placeholders, want 1", n)
	}
	data = bytes.Replace(data, token, []byte(`"arguments_spelling":`+spelling), 1)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	x := openIndex(t, path, dir)
	if err := x.Close(); err != nil {
		t.Fatal(err)
	}
	// Relabel the sidecar as a build older than the schema fold would have
	// persisted: the bare projection name, with no schema fingerprint and no
	// layout suffix. A reader that rejected the record cannot have written the
	// current identity, and this is the identity the package's own stale-sidecar
	// table uses for "written by an older build".
	rewriteMetaField(t, dir, "projection", strconv.Quote(projectionID))

	reopened := openIndex(t, path, dir)
	if reopened.rebuilds != 1 {
		t.Fatalf("builds = %d, want the stale sidecar rebuilt", reopened.rebuilds)
	}
	assertAllWindows(t, reopened, path)

	// The rebuilt index serves the call with its intact structured arguments.
	window, err := reopened.Latest(2)
	if err != nil {
		t.Fatal(err)
	}
	if len(window.Candidates) != 2 || window.Candidates[1].Item.CallID != "call_spelling" || window.Candidates[1].Item.ArgumentsJSON != arguments {
		t.Fatalf("latest = %v, want the spelled call served with %s", window.Candidates, arguments)
	}
}
