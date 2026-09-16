package rendezvous

import (
	"regexp"
	"testing"
	"time"
)

// ownershipFixture is a fully populated registration. The protocol literal is
// opaque identity data here; rendezvous deliberately stays free of an appwire
// dependency, so tests do not import the version constant.
func ownershipFixture() Entry {
	return Entry{
		PID:          4242,
		Address:      "127.0.0.1:7890",
		Protocol:     "evener-appwire-v5",
		Endpoint:     "ws://127.0.0.1:7890/appwire",
		SourceID:     "local",
		ThreadID:     "thread_1",
		SessionID:    "sess_1",
		WorkspaceRef: "local:root",
		InstanceID:   "inst_1",
		WorkingDir:   "/home/user/project",
		StateDir:     "/home/user/.local/state/evener/project",
		Agent:        "evener",
		Model:        "gpt-5.2",
		Provider:     "openai",
		HubToken:     "secret-hub-token",
		StartedAt:    time.Date(2026, 9, 12, 10, 0, 0, 123456789, time.UTC),
		SpawnedBy:    "evener-hub",
	}
}

func TestOwnershipFingerprintStable(t *testing.T) {
	entry := ownershipFixture()
	first := OwnershipFingerprint(entry)
	if first != OwnershipFingerprint(entry) {
		t.Fatal("fingerprint is not deterministic")
	}
	if matched := regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(first); !matched {
		t.Fatalf("fingerprint %q is not a hex SHA-256", first)
	}
}

// TestOwnershipFingerprintChangesWithIdentity proves every canonical
// ownership field enters the fingerprint: a daemon that differs in any one of
// them is not the same process, no matter how similar the rest looks.
func TestOwnershipFingerprintChangesWithIdentity(t *testing.T) {
	base := ownershipFixture()
	want := OwnershipFingerprint(base)
	cases := map[string]func(*Entry){
		"pid":           func(e *Entry) { e.PID++ },
		"address":       func(e *Entry) { e.Address = "127.0.0.1:7891" },
		"endpoint":      func(e *Entry) { e.Endpoint = "ws://127.0.0.1:7891/appwire" },
		"protocol":      func(e *Entry) { e.Protocol = "evener-appwire-v4" },
		"source":        func(e *Entry) { e.SourceID = "worktree" },
		"thread":        func(e *Entry) { e.ThreadID = "thread_2" },
		"session":       func(e *Entry) { e.SessionID = "sess_2" },
		"instance":      func(e *Entry) { e.InstanceID = "inst_2" },
		"workspace":     func(e *Entry) { e.WorkspaceRef = "local:other" },
		"working dir":   func(e *Entry) { e.WorkingDir = "/home/user/other" },
		"state dir":     func(e *Entry) { e.StateDir = "/home/user/.local/state/evener/other" },
		"started at":    func(e *Entry) { e.StartedAt = e.StartedAt.Add(time.Second) },
		"started nanos": func(e *Entry) { e.StartedAt = e.StartedAt.Add(time.Nanosecond) },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			entry := ownershipFixture()
			mutate(&entry)
			if got := OwnershipFingerprint(entry); got == want {
				t.Fatalf("fingerprint unchanged after %s mutation", name)
			}
		})
	}
}

// TestOwnershipFingerprintPreservesExactInstant proves the timestamp enters
// the fingerprint as the exact instant (UTC/RFC3339Nano): the same instant in
// a different zone fingerprints identically, while a nanosecond of skew does
// not.
func TestOwnershipFingerprintPreservesExactInstant(t *testing.T) {
	base := ownershipFixture()
	zoned := ownershipFixture()
	zoned.StartedAt = base.StartedAt.In(time.FixedZone("UTC-7", -7*60*60))
	if OwnershipFingerprint(base) != OwnershipFingerprint(zoned) {
		t.Fatal("same instant in a different zone changed the fingerprint")
	}
}

// TestOwnershipFingerprintExcludesSecretsAndSettings is the direct
// nondisclosure proof: the hub token, provenance, and model/prompt-adjacent
// settings never enter the fingerprint preimage, so a fingerprint can be
// shared with a peer without leaking them.
func TestOwnershipFingerprintExcludesSecretsAndSettings(t *testing.T) {
	base := ownershipFixture()
	want := OwnershipFingerprint(base)
	cases := map[string]func(*Entry){
		"hub token":  func(e *Entry) { e.HubToken = "different-secret" },
		"agent":      func(e *Entry) { e.Agent = "other-agent" },
		"model":      func(e *Entry) { e.Model = "gpt-5.1" },
		"provider":   func(e *Entry) { e.Provider = "anthropic" },
		"spawned by": func(e *Entry) { e.SpawnedBy = "evener-tui" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			entry := ownershipFixture()
			mutate(&entry)
			if got := OwnershipFingerprint(entry); got != want {
				t.Fatalf("fingerprint disclosed %s: %q != %q", name, got, want)
			}
		})
	}
}

// TestOwnershipFingerprintFieldBoundariesDisambiguate pins the canonical
// encoding: field boundaries are unambiguous, so concatenated values from
// different field splits cannot collide.
func TestOwnershipFingerprintFieldBoundariesDisambiguate(t *testing.T) {
	a := ownershipFixture()
	a.SourceID, a.ThreadID = "ab", "c"
	b := ownershipFixture()
	b.SourceID, b.ThreadID = "a", "bc"
	if OwnershipFingerprint(a) == OwnershipFingerprint(b) {
		t.Fatal("field-split collision: canonical encoding is ambiguous")
	}
}
