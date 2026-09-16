package hub

// Device flows the user abandons are swept when a new device flow starts.
// DeviceStart used to only ever add entries: expiry was checked when a flow was
// polled, so a start nothing polled kept its device code in deviceFlows until
// the process restarted. The fix reaps entries already past hubAuthFlowTTL as
// it records the new flow, mirroring LoginStart's sweep of c.flows. These tests
// pin the sweep and its TTL boundary against the controller's real deviceFlows
// map, moving the same `now` seam the other device-flow tests use.

import (
	"context"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
	authopenai "primeradiant.com/evener/auth/openai"
	"primeradiant.com/evener/auth/openai/oaitest"
	"primeradiant.com/evener/internal/credentials"
)

// newDeviceSweepTestController is the fixture both tests share: the same
// hermetic Codex controller TestAuth_DeviceStart_ReturnsCodeAndStoresFlow
// builds, with the clock exposed so a test can move it, and a device-code
// request that succeeds without a network round trip.
func newDeviceSweepTestController(t *testing.T) (*hubAuthController, *time.Time) {
	t.Helper()
	oaitest.IsolateOpenAIAuth(t)
	dir := t.TempDir()
	store, _ := credentials.LoadStore(filepath.Join(dir, "credentials.toml"))
	c := newHubAuthControllerWithStore(dir, store)
	attachTestRegistry(t, c)
	c.requestDeviceCode = func(context.Context, *http.Client, authopenai.Config) (authopenai.DeviceCode, error) {
		return authopenai.DeviceCode{UserCode: "USER-1", VerificationURL: "https://auth.openai.com/codex/device", DeviceAuthID: "dev-1", Interval: 5 * time.Second}, nil
	}
	now := time.Now()
	c.now = func() time.Time { return now }
	return c, &now
}

// startDeviceFlow starts one Codex device flow and returns its id.
func startDeviceFlow(t *testing.T, c *hubAuthController) string {
	t.Helper()
	start, err := c.DeviceStart(context.Background(), appwire.AuthDeviceStartParams{Provider: "openai-codex"})
	if err != nil {
		t.Fatalf("DeviceStart: %v", err)
	}
	if start.Fallback || start.FlowID == "" {
		t.Fatalf("DeviceStart = %+v, want a real device flow with a flow id", start)
	}
	return start.FlowID
}

// deviceFlowIDs snapshots the controller's live device-flow ids under its lock.
func deviceFlowIDs(c *hubAuthController) map[string]bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	ids := make(map[string]bool, len(c.deviceFlows))
	for id := range c.deviceFlows {
		ids[id] = true
	}
	return ids
}

// TestAuth_DeviceStart_SweepsAnExpiredFlow pins the reaper: a device flow left
// unpolled past hubAuthFlowTTL must be gone once the next start records its
// own. Without the sweep deviceFlows holds A and B after B's start; with it,
// exactly B remains, so abandoned device codes cannot accumulate per start.
func TestAuth_DeviceStart_SweepsAnExpiredFlow(t *testing.T) {
	c, now := newDeviceSweepTestController(t)
	flowA := startDeviceFlow(t, c)

	*now = now.Add(hubAuthFlowTTL + time.Minute)

	flowB := startDeviceFlow(t, c)
	if flowB == flowA {
		t.Fatalf("flow B id = %q, want a fresh flow distinct from A", flowB)
	}

	got := deviceFlowIDs(c)
	if len(got) != 1 {
		t.Fatalf("deviceFlows holds %d entries (%v), want exactly 1: B's start must sweep expired flow A %q", len(got), got, flowA)
	}
	if !got[flowB] {
		t.Fatalf("deviceFlows = %v, want B's flow %q: the sweep must keep the flow it is recording", got, flowB)
	}
}

// TestAuth_DeviceStart_KeepsFlowsInsideTheWindow is the positive control: the
// sweep is bounded by hubAuthFlowTTL, not a blind clear. Two starts inside the
// window leave both device flows recorded, so a flow that is simply young
// stays pollable rather than being reaped by the next start.
func TestAuth_DeviceStart_KeepsFlowsInsideTheWindow(t *testing.T) {
	c, now := newDeviceSweepTestController(t)
	flowA := startDeviceFlow(t, c)

	*now = now.Add(hubAuthFlowTTL - time.Minute)

	flowB := startDeviceFlow(t, c)
	if flowB == flowA {
		t.Fatalf("flow B id = %q, want a fresh flow distinct from A", flowB)
	}

	got := deviceFlowIDs(c)
	if len(got) != 2 {
		t.Fatalf("deviceFlows holds %d entries (%v), want both flows: a start inside the TTL must not reap a live one", len(got), got)
	}
	for _, want := range []string{flowA, flowB} {
		if !got[want] {
			t.Fatalf("deviceFlows = %v, want %q kept: both starts are inside hubAuthFlowTTL", got, want)
		}
	}
}
