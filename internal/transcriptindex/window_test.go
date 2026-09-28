package transcriptindex

import (
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
// every limit, a walk that runs to about ten minutes under -race on CI for
// TestReplacedTruncatedOrRewrittenTranscriptRebuilds's four subtests over the
// "everything" fixture. assertAllCandidates already proves every item, full
// stop, by paging the whole transcript once; this adds only the specific
// boundaries a pagination bug is likeliest to break: the first and last
// items, a couple next to them, and the midpoint, each at a small and a
// full-page limit.
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
