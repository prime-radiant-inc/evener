package main

import (
	"bytes"
	"testing"
)

func TestSplitSSEEvents(t *testing.T) {
	body := []byte("data: a\n\ndata: b\n\ndata: c\n\n")
	events := splitSSEEvents(body)
	if len(events) != 3 {
		t.Fatalf("got %d events, want 3: %q", len(events), events)
	}
	// Reassembly is lossless.
	if got := bytes.Join(events, nil); !bytes.Equal(got, body) {
		t.Errorf("reassembled = %q, want %q", got, body)
	}
	// An unterminated trailing event is kept.
	tail := splitSSEEvents([]byte("data: a\n\ndata: tail"))
	if len(tail) != 2 || string(tail[1]) != "data: tail" {
		t.Errorf("trailing event = %q, want the unterminated remainder", tail)
	}
}

func TestSSESeedWindows(t *testing.T) {
	// A stream within the cap is a single window, unchanged.
	small := []byte("data: a\n\ndata: b\n\n")
	if w := sseSeedWindows(small, 1000); len(w) != 1 || !bytes.Equal(w[0], small) {
		t.Fatalf("small stream: got %d windows, want 1 unchanged", len(w))
	}

	// A large stream is packed into multiple windows of whole events, each within
	// the cap, and concatenating the windows reproduces the original.
	var big bytes.Buffer
	for i := 0; i < 50; i++ {
		big.WriteString("data: ")
		big.Write(bytes.Repeat([]byte("x"), 40))
		big.WriteString("\n\n")
	}
	windows := sseSeedWindows(big.Bytes(), 200)
	if len(windows) < 2 {
		t.Fatalf("expected the big stream to split into >=2 windows, got %d", len(windows))
	}
	var reassembled []byte
	for _, w := range windows {
		if len(w) > 200 {
			t.Errorf("window of %d bytes exceeds the 200 cap", len(w))
		}
		// Each window ends on the event boundary.
		if !bytes.HasSuffix(w, []byte("\n\n")) {
			t.Errorf("window does not end on an event boundary: %q", w)
		}
		reassembled = append(reassembled, w...)
	}
	if !bytes.Equal(reassembled, big.Bytes()) {
		t.Error("windows do not reassemble to the original stream")
	}
}

func TestSSESeedWindows_SkipsOversizedEvent(t *testing.T) {
	// A single event larger than the cap is skipped (can't fit any window), while
	// the surrounding fitting events are still emitted.
	body := []byte("data: ok1\n\ndata: " + string(bytes.Repeat([]byte("x"), 500)) + "\n\ndata: ok2\n\n")
	windows := sseSeedWindows(body, 100)
	joined := string(bytes.Join(windows, nil))
	if !bytes.Contains([]byte(joined), []byte("ok1")) || !bytes.Contains([]byte(joined), []byte("ok2")) {
		t.Errorf("fitting events dropped: %q", joined)
	}
	if bytes.Contains([]byte(joined), bytes.Repeat([]byte("x"), 500)) {
		t.Error("oversized event was not skipped")
	}
}
