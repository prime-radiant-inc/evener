package rendezvous

import (
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"time"
)

// OwnershipFingerprint returns the opaque, non-secret fingerprint of exact
// daemon ownership, shared by the daemon identity check and the Hub roster.
// It hashes the canonical identity fields — PID, address/endpoint, protocol,
// source/thread/session/instance IDs, workspace/state/working-directory
// identity, and the exact start instant (UTC, RFC3339Nano) — so any ownership
// change yields a different fingerprint. HubToken, provenance (SpawnedBy) and
// model/prompt-adjacent settings (Agent/Model/Provider) never enter the
// preimage: a fingerprint is safe to share with a peer. Fields are
// NUL-separated so boundaries cannot collide. This is the only hashing
// implementation; do not add a second one.
func OwnershipFingerprint(entry Entry) string {
	fields := [...]string{
		strconv.Itoa(entry.PID),
		entry.Address,
		entry.Endpoint,
		entry.Protocol,
		entry.SourceID,
		entry.ThreadID,
		entry.SessionID,
		entry.InstanceID,
		entry.WorkspaceRef,
		entry.WorkingDir,
		entry.StateDir,
		entry.StartedAt.UTC().Format(time.RFC3339Nano),
	}
	h := sha256.New()
	for _, field := range fields {
		_, _ = h.Write([]byte(field))
		_, _ = h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}
