package transcriptindex

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"reflect"
	"slices"
	"strconv"
	"testing"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/internal/appitempaging"
	"primeradiant.com/evener/llm"
)

func isCursorStale(err error) bool {
	var wire appwire.WireError
	return errors.As(err, &wire) && reflect.DeepEqual(wire, appwire.TranscriptItemCursorStale())
}

// assertWindow requires window to hold exactly the reference items [end-limit,
// end), and HasOlder to say whether items remain before them.
func assertWindow(t testing.TB, label string, window Window, want []appitempaging.TranscriptItemCandidate, end, limit int) {
	t.Helper()
	// Independent of the reference: a flushed item positioned the same way
	// the reference derives it can still collide with a real item's
	// position (roborev finding on PR #2303) and pass a diff against a
	// reference that collides identically. ValidateCandidates catches that
	// regardless of what the reference says.
	if err := appitempaging.ValidateCandidates(window.Candidates); err != nil {
		t.Fatalf("%s (end %d, limit %d): %v", label, end, limit, err)
	}
	start := max(0, end-limit)
	if expected := want[start:end]; !reflect.DeepEqual(window.Candidates, expected) {
		t.Fatalf("%s (end %d, limit %d): window diverges from the reference\n got: %s\nwant: %s", label, end, limit, dump(window.Candidates), dump(expected))
	}
	if window.HasOlder != (start > 0) {
		t.Fatalf("%s (end %d, limit %d): HasOlder = %v, want %v", label, end, limit, window.HasOlder, start > 0)
	}
}

// assertAllWindows compares the latest window at every limit, and every
// Before boundary at a spread of limits, with the reference of the transcript
// at path.
func assertAllWindows(t testing.TB, x *Index, path string) {
	t.Helper()
	want := referenceCandidates(t, path)
	for limit := 1; limit <= appwire.TranscriptItemPageLimit; limit++ {
		window, err := x.Latest(limit)
		if err != nil {
			t.Fatal(err)
		}
		assertWindow(t, "latest", window, want, len(want), limit)
	}
	for _, limit := range []int{1, 2, 3, 7, appwire.TranscriptItemPageLimit} {
		for end := range len(want) {
			window, err := x.Before(want[end].Position, limit)
			if err != nil {
				t.Fatalf("before %v: %v", want[end].Position, err)
			}
			assertWindow(t, fmt.Sprintf("before %v", want[end].Position), window, want, end, limit)
		}
	}
}

// assertSampledWindows is assertAllWindows's cheaper sibling: it proves the
// same thing a caller of assertAllWindows wants — a rebuilt index reads back
// correctly — without the O(items x limits) walk of every Before boundary at
// every limit, a walk that adds up fast when a test repeats it (see
// TestAppendEntryByEntryMatchesTheReference). assertAllCandidates already
// proves every item, full stop, by paging the whole transcript once; this
// adds only the specific boundaries a pagination bug is likeliest to break:
// the first and last items, a couple next to them, and the midpoint, each at
// a small and a full-page limit.
func assertSampledWindows(t testing.TB, x *Index, path string) {
	t.Helper()
	assertAllCandidates(t, x, path)
	want := referenceCandidates(t, path)
	if len(want) == 0 {
		return
	}
	limits := []int{1, appwire.TranscriptItemPageLimit}
	for _, limit := range limits {
		window, err := x.Latest(limit)
		if err != nil {
			t.Fatal(err)
		}
		assertWindow(t, "latest", window, want, len(want), limit)
	}
	endSet := map[int]bool{0: true, len(want) - 1: true, len(want) / 2: true}
	if len(want) > 3 {
		endSet[1], endSet[len(want)-2] = true, true
	}
	ends := slices.Sorted(maps.Keys(endSet))
	for _, end := range ends {
		for _, limit := range limits {
			window, err := x.Before(want[end].Position, limit)
			if err != nil {
				t.Fatalf("before %v: %v", want[end].Position, err)
			}
			assertWindow(t, fmt.Sprintf("before %v", want[end].Position), window, want, end, limit)
		}
	}
}

// assertAllCandidates pages through every item, newest page first, and
// requires the whole list to equal the reference of the transcript at path.
func assertAllCandidates(t testing.TB, x *Index, path string) {
	t.Helper()
	want := referenceCandidates(t, path)
	var got []appitempaging.TranscriptItemCandidate
	window, err := x.Latest(appwire.TranscriptItemPageLimit)
	for {
		if err != nil {
			t.Fatal(err)
		}
		got = append(append([]appitempaging.TranscriptItemCandidate(nil), window.Candidates...), got...)
		if !window.HasOlder {
			break
		}
		window, err = x.Before(window.Candidates[0].Position, appwire.TranscriptItemPageLimit)
	}
	if len(want) == 0 {
		want = nil
	}
	if !reflect.DeepEqual(got, want) {
		for i := range min(len(got), len(want)) {
			if !reflect.DeepEqual(got[i], want[i]) {
				t.Fatalf("candidate %d of %d/%d diverges from the reference\n got: %s\nwant: %s", i, len(got), len(want), dump(got[i]), dump(want[i]))
			}
		}
		t.Fatalf("items diverge from the reference: %d candidates, want %d", len(got), len(want))
	}
}

func TestWindowsEqualTheReferenceOnEveryBoundary(t *testing.T) {
	for _, fx := range fixtures() {
		t.Run(fx.name, func(t *testing.T) {
			path := writeFixture(t, fx)
			x, err := Open(path, t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer x.Close() //nolint:errcheck // test cleanup
			assertAllWindows(t, x, path)
			for _, missing := range []appwire.ThreadItemPosition{{Entry: 1 << 40}, {Entry: 0, Item: 1 << 20}, {Entry: 1, Item: 999}} {
				if _, err := x.Before(missing, 5); !isCursorStale(err) {
					t.Fatalf("Before(%v): err = %v, want a stale cursor", missing, err)
				}
			}
		})
	}
}

func TestEmptyTranscriptHasNoItems(t *testing.T) {
	path := writeFixture(t, fixture{name: "empty"})
	x, err := Open(path, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer x.Close() //nolint:errcheck // test cleanup
	window, err := x.Latest(40)
	if err != nil {
		t.Fatal(err)
	}
	if len(window.Candidates) != 0 || window.HasOlder {
		t.Fatalf("empty transcript window = %+v", window)
	}
}

// TestInputImagesCarryTheirContentAddress requires a user image to project
// with the sha and size the hub serves its bytes back by
// (/s/<session>/images/<sha>): the index strips the bytes, so without them
// nothing can fetch the image again.
func TestInputImagesCarryTheirContentAddress(t *testing.T) {
	pasted := schema.NewTurn(schema.TurnUserInput, llm.Message{Role: llm.RoleUser, Content: []llm.ContentPart{
		{Kind: llm.ContentText, Text: "look"},
		{Kind: llm.ContentImage, Image: &llm.ImageData{Data: pngBytes, MediaType: "image/png"}},
	}})
	for name, turn := range map[string]schema.Turn{"legacy": pasted, "new format": opens("turn_m1", schema.TurnSpanExecution, pasted)} {
		fx := fixture{name: name, header: transcript.Header{SessionID: "s"}, lines: []fixtureLine{entryLine(turn)}}
		x := openIndex(t, writeFixture(t, fx), t.TempDir())
		window, err := x.Latest(10)
		if err != nil {
			t.Fatal(err)
		}
		var images []appwire.InputItem
		for _, candidate := range window.Candidates {
			images = append(images, candidate.Item.Images...)
		}
		want := map[string]string{"sha": events.ImageSHA(pngBytes), "size": strconv.Itoa(len(pngBytes))}
		if len(images) != 1 || !reflect.DeepEqual(images[0].Metadata, want) {
			t.Fatalf("%s: images = %+v, want one carrying %v", name, images, want)
		}
	}
}

func TestAuthorshipCommunicateWindow(t *testing.T) {
	for _, pending := range []bool{false, true} {
		source := assistant(call("call", "communicate", `{"message":"delivered"}`))
		source.Authorship = &schema.TranscriptAuthorship{Author: schema.TranscriptAuthor{Kind: schema.TranscriptAuthorAgent, SessionID: "parent"}, Inherited: true}
		lines := []fixtureLine{entryLine(user("input")), entryLine(source)}
		if !pending {
			lines = append(lines, entryLine(results(result("call", "", "ok"))))
		}
		path := writeFixture(t, fixture{name: "authorship", header: transcript.Header{SessionID: "child"}, lines: lines})
		x := openIndex(t, path, t.TempDir())
		w, err := x.Latest(1)
		if err != nil {
			t.Fatal(err)
		}
		if len(w.Candidates) != 1 || !w.HasOlder || w.Candidates[0].Item.Text != "delivered" {
			t.Fatalf("window=%+v", w)
		}
		b, err := json.Marshal(w.Candidates[0].Item)
		if err != nil {
			t.Fatal(err)
		}
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(b, &fields); err != nil {
			t.Fatal(err)
		}
		if string(fields["authorship"]) != `{"author":{"kind":"agent","sessionRef":"local:parent"},"inherited":true}` {
			t.Errorf("pending=%v authorship=%s", pending, fields["authorship"])
		}
		if w.Candidates[0].Item.CallID != "call" || w.Candidates[0].Item.TranscriptKey == "" {
			t.Fatal(w.Candidates[0])
		}
	}
}

func TestInheritedCommunicateIndexedWindow(t *testing.T) {
	source := inRound(inTurn("copied", assistant(call("call_child", "communicate", `{"message":"copied child update"}`))), "round_child")
	source.Authorship = &schema.TranscriptAuthorship{Author: schema.TranscriptAuthor{Kind: schema.TranscriptAuthorAgent, SessionID: "child", OwnerSessionID: "parent", DelegateID: "dlg_child", DelegateName: "Child"}, Inherited: true}
	settled := inRound(inTurn("copied", results(result("call_child", "communicate", "ok"))), "round_child")
	settled.Authorship = &schema.TranscriptAuthorship{Author: schema.TranscriptAuthor{Kind: schema.TranscriptAuthorUnknown}, Inherited: true}
	path := writeFixture(t, fixture{name: "inherited-communicate", header: transcript.Header{SessionID: "leaf"}, lines: []fixtureLine{
		entryLine(opens("copied", schema.TurnSpanExecution, user("before call"))), entryLine(source), entryLine(settled),
	}})
	x := openIndex(t, path, t.TempDir())
	w, err := x.Latest(1)
	if err != nil {
		t.Fatal(err)
	}
	if len(w.Candidates) != 1 || w.Candidates[0].Item.Text != "copied child update" {
		t.Fatalf("missing inherited communicate at result boundary: window=%+v", w)
	}
	c := w.Candidates[0]
	wantAuthor := &appwire.TranscriptAuthorship{Author: appwire.TranscriptAuthor{Kind: appwire.TranscriptAuthorAgent, SessionRef: "local:child", OwnerRef: "local:parent", DelegateID: "dlg_child", DelegateName: "Child"}, Inherited: true}
	if !w.HasOlder || c.Position != (appwire.ThreadItemPosition{Entry: 3, Item: 0}) || c.Item.ID != "item_assistant_3_0" || c.Item.CallID != "call_child" || c.TurnID != "copied" || !reflect.DeepEqual(c.Item.Authorship, wantAuthor) {
		t.Fatalf("result identity/author: candidate=%+v item=%+v", c, c.Item)
	}
}

// A limit beyond the public page maximum is refused before any read runs,
// on every item-window entry point.
func TestWindowReadsRefuseLimitsBeyondThePageMaximum(t *testing.T) {
	t.Parallel()
	fx := fixture{name: "limits", header: everything().header, lines: []fixtureLine{entryLine(user("hello"))}}
	x := openIndex(t, writeFixture(t, fx), t.TempDir())
	over := appwire.TranscriptItemPageLimit + 1
	if _, err := x.Latest(over); err == nil {
		t.Fatal("Latest accepted a limit beyond the page maximum")
	}
	if _, _, err := x.LatestSince(over, appwire.SnapshotIdentity{}); err == nil {
		t.Fatal("LatestSince accepted a limit beyond the page maximum")
	}
	if _, err := x.Before(appwire.ThreadItemPosition{}, over); err == nil {
		t.Fatal("Before accepted a limit beyond the page maximum")
	}
}
