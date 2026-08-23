package tui

import (
	"testing"

	"primeradiant.com/evener/appwire"
)

// TestHubFrameFeedNilNotifications covers the nil-receiver guard.
func TestHubFrameFeedNilNotifications(t *testing.T) {
	var f *hubFrameFeed
	if ch := f.Notifications(); ch != nil {
		t.Fatalf("nil Notifications = %v, want nil", ch)
	}
}

// TestHubFrameFeedNilSetTransportCloser covers the nil-receiver guard.
func TestHubFrameFeedNilSetTransportCloser(t *testing.T) {
	var f *hubFrameFeed
	f.SetTransportCloser(func() error { return nil })
	// Should not panic.
}

// TestHubFrameFeedNilObserve covers the nil-receiver guard.
func TestHubFrameFeedNilObserve(t *testing.T) {
	var f *hubFrameFeed
	f.Observe(appwire.Message{}, nil)
	// Should not panic.
}

// TestHubFrameFeedNilBeginCapture covers the nil-receiver guard.
func TestHubFrameFeedNilBeginCapture(t *testing.T) {
	var f *hubFrameFeed
	if c := f.BeginCapture(); c != nil {
		t.Fatalf("nil BeginCapture = %v, want nil", c)
	}
}

// TestHubReadCaptureNilCutOn covers the nil-receiver guard.
func TestHubReadCaptureNilCutOn(t *testing.T) {
	var c *hubReadCapture
	c.CutOn(appwire.ID{})
	// Should not panic.
}

// TestHubReadCaptureNilBeforeCut covers the nil-receiver guard.
func TestHubReadCaptureNilBeforeCut(t *testing.T) {
	var c *hubReadCapture
	if frames := c.BeforeCut(); frames != nil {
		t.Fatalf("nil BeforeCut = %v, want nil", frames)
	}
}

// TestHubReadCaptureNilRelease covers the nil-receiver guard.
func TestHubReadCaptureNilRelease(t *testing.T) {
	var c *hubReadCapture
	c.Release()
	// Should not panic.
}
