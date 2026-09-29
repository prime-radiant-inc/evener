package server

import (
	"testing"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
)

// The root's thread snapshot carries the sandbox mode and network setting its
// meta persists (S15). A session is sandboxed once, when it starts, so the
// seed at identity install is the sample that matters.
func TestThreadSnapshotsCarryTheSessionsAccess(t *testing.T) {
	networkOff := false
	srv := NewServer(ServerConfig{})
	srv.SetAppIdentity("local", "root")
	publishEnvelope(srv, &stubThreadEnvelopeSource{meta: schema.SessionMeta{
		ID:     "root",
		Config: schema.ConfigSnapshot{Sandbox: "workspace-write", SandboxNet: &networkOff},
	}})
	want := appwire.ThreadAccess{Sandbox: "workspace-write", Network: false}
	if got := readThreadOverWire(t, srv, "local:root").Evener.Access; got == nil || *got != want {
		t.Fatalf("read access = %+v, want %+v", got, want)
	}
}

// An unsandboxed session persists no mode, and still says so: "off", with the
// network on.
func TestThreadSnapshotsSayAnUnsandboxedSessionIsOff(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetAppIdentity("local", "root")
	publishEnvelope(srv, &stubThreadEnvelopeSource{meta: schema.SessionMeta{ID: "root"}})
	want := appwire.ThreadAccess{Sandbox: "off", Network: true}
	if got := readThreadOverWire(t, srv, "local:root").Evener.Access; got == nil || *got != want {
		t.Fatalf("read access = %+v, want %+v", got, want)
	}
}

// A subagent can run in a narrower sandbox than its coordinator, so its own
// thread carries its own access, from its session's start (S15).
func TestDescendantThreadsCarryTheirOwnAccess(t *testing.T) {
	networkOff := false
	srv := NewServer(ServerConfig{})
	srv.SetAppIdentity("local", "root")
	publishEnvelope(srv, &stubThreadEnvelopeSource{meta: schema.SessionMeta{ID: "root", Config: schema.ConfigSnapshot{Sandbox: "workspace-write"}}})
	srv.RecordDescendantAppEvent("root", events.SessionEvent{Kind: events.EventSessionStart, SessionID: "child", Data: events.SessionStartData{
		Sandbox: "read-only", SandboxNet: &networkOff,
	}})
	want := appwire.ThreadAccess{Sandbox: "read-only", Network: false}
	if got := readThreadOverWire(t, srv, "local:child").Evener.Access; got == nil || *got != want {
		t.Fatalf("descendant access = %+v, want %+v", got, want)
	}
	if got := readThreadOverWire(t, srv, "local:root").Evener.Access; got == nil || got.Sandbox != "workspace-write" {
		t.Fatalf("root access = %+v, want the root's own workspace-write", got)
	}
}

// A snapshot must not hand out the cached envelope's Access pointer: the
// envelope is shared across every read, so a caller mutating its copy would
// corrupt the cache and race concurrent readers. Access is a value type, so
// the snapshot gets its own copy.
func TestThreadSnapshotsDoNotAliasTheEnvelopeAccess(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetAppIdentity("local", "root")
	publishEnvelope(srv, &stubThreadEnvelopeSource{meta: schema.SessionMeta{
		ID:     "root",
		Config: schema.ConfigSnapshot{Sandbox: "workspace-write"},
	}})
	snap := readThreadOverWire(t, srv, "local:root")
	if snap.Evener.Access == nil {
		t.Fatal("access missing from snapshot")
	}
	snap.Evener.Access.Sandbox = "off"
	if got := srv.appEnvelope.Access; got == nil || got.Sandbox != "workspace-write" {
		t.Fatalf("envelope access = %+v after mutating the snapshot, want workspace-write", got)
	}
}

// A descendant snapshot reads through appThreadForID, which shallow-copies the
// cached projection; Access must be cloned there too, or a caller mutating the
// child's snapshot corrupts the projection for every later reader (S15).
func TestDescendantSnapshotsDoNotAliasTheProjectionAccess(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetAppIdentity("local", "root")
	publishEnvelope(srv, &stubThreadEnvelopeSource{meta: schema.SessionMeta{ID: "root"}})
	srv.RecordDescendantAppEvent("root", events.SessionEvent{Kind: events.EventSessionStart, SessionID: "child", Data: events.SessionStartData{
		Sandbox: "read-only",
	}})
	snap := readThreadOverWire(t, srv, "local:child")
	if snap.Evener.Access == nil {
		t.Fatal("access missing from descendant snapshot")
	}
	snap.Evener.Access.Sandbox = "off"
	projection := srv.appDescendants["child"]
	if projection == nil {
		t.Fatal("child projection missing")
	}
	if got := projection.thread.Evener.Access; got == nil || got.Sandbox != "read-only" {
		t.Fatalf("projection access = %+v after mutating the snapshot, want read-only", got)
	}
}
