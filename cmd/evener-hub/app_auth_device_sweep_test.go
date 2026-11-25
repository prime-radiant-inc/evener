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

// TestAuth_CopilotDeviceStartSweepsAnExpiredFlow is the sweep's Copilot half:
// the same reaper bounds Copilot device codes too.
func TestAuth_CopilotDeviceStartSweepsAnExpiredFlow(t *testing.T) {
	ctrl, _, now := newCopilotDeviceTestController(t, copilotDeviceInstanceToml)
	startA := startCopilotDeviceFlow(t, ctrl, "work")
	*now = now.Add(hubAuthFlowTTL + time.Minute)
	startB := startCopilotDeviceFlow(t, ctrl, "work")

	got := deviceFlowIDs(ctrl)
	if len(got) != 1 || !got[startB] {
		t.Fatalf("deviceFlows = %v, want only the new flow %q: the expired Copilot flow %q must be swept",
			got, startB, startA)
	}
}

// TestAuth_CopilotDevicePollHonorsNextPollAt pins the local pace: before
// NextPollAt a poll answers pending without touching GitHub, and once the
// deadline passes it polls once.
func TestAuth_CopilotDevicePollHonorsNextPollAt(t *testing.T) {
	ctrl, transport, now := newCopilotDeviceTestController(t, copilotDeviceInstanceToml)
	flowID := startCopilotDeviceFlow(t, ctrl, "work")

	poll := func() appwire.AuthDevicePollResponse {
		t.Helper()
		resp, err := ctrl.DevicePoll(context.Background(), appwire.AuthDevicePollParams{Provider: "work", FlowID: flowID})
		if err != nil {
			t.Fatalf("DevicePoll: %v", err)
		}
		return resp
	}
	if got := poll(); got.State != "pending" {
		t.Fatalf("poll at start = %+v, want pending", got)
	}
	*now = now.Add(4 * time.Second)
	if got := poll(); got.State != "pending" {
		t.Fatalf("poll before NextPollAt = %+v, want pending", got)
	}
	if calls := transport.callCount("/login/oauth/access_token"); calls != 0 {
		t.Fatalf("local polls made %d GitHub calls, want 0", calls)
	}
	*now = now.Add(time.Second)
	if got := poll(); got.State != "pending" {
		t.Fatalf("poll at NextPollAt = %+v, want pending", got)
	}
	if calls := transport.callCount("/login/oauth/access_token"); calls != 1 {
		t.Fatalf("poll at NextPollAt made %d GitHub calls, want 1", calls)
	}
}

// TestAuth_CopilotDevicePollSlowDownAdvancesNextPollAt pins slow_down: the
// interval GitHub returns moves the next outbound poll out, so the hub stops
// hammering the endpoint at a pace GitHub asked it to leave.
func TestAuth_CopilotDevicePollSlowDownAdvancesNextPollAt(t *testing.T) {
	ctrl, transport, now := newCopilotDeviceTestController(t, copilotDeviceInstanceToml)
	transport.polls = []string{`{"error":"slow_down"}`, `{"error":"authorization_pending"}`}
	flowID := startCopilotDeviceFlow(t, ctrl, "work")

	*now = now.Add(5 * time.Second)
	if resp, err := ctrl.DevicePoll(context.Background(), appwire.AuthDevicePollParams{Provider: "work", FlowID: flowID}); err != nil || resp.State != "pending" {
		t.Fatalf("first poll = %+v err=%v, want pending", resp, err)
	}
	*now = now.Add(5 * time.Second)
	if resp, err := ctrl.DevicePoll(context.Background(), appwire.AuthDevicePollParams{Provider: "work", FlowID: flowID}); err != nil || resp.State != "pending" {
		t.Fatalf("poll inside slow_down's interval = %+v err=%v, want pending", resp, err)
	}
	if calls := transport.callCount("/login/oauth/access_token"); calls != 1 {
		t.Fatalf("GitHub calls after slow_down = %d, want 1: the next poll must wait out the returned interval", calls)
	}
	*now = now.Add(5 * time.Second)
	if resp, err := ctrl.DevicePoll(context.Background(), appwire.AuthDevicePollParams{Provider: "work", FlowID: flowID}); err != nil || resp.State != "pending" {
		t.Fatalf("poll at slow_down's deadline = %+v err=%v, want pending", resp, err)
	}
	if calls := transport.callCount("/login/oauth/access_token"); calls != 2 {
		t.Fatalf("GitHub calls at slow_down's deadline = %d, want 2", calls)
	}
}

// startCopilotDeviceFlow starts one Copilot device flow and returns its id.
func startCopilotDeviceFlow(t *testing.T, ctrl *hubAuthController, provider string) string {
	t.Helper()
	start, err := ctrl.DeviceStart(context.Background(), appwire.AuthDeviceStartParams{Provider: provider})
	if err != nil {
		t.Fatalf("DeviceStart(%s): %v", provider, err)
	}
	if start.Fallback || start.FlowID == "" {
		t.Fatalf("DeviceStart(%s) = %+v, want a device flow", provider, start)
	}
	return start.FlowID
}
