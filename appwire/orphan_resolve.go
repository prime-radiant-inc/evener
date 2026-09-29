package appwire

// Crash-fencing spec 08c §5/§8/§9's wire shapes for `evener/host/orphan-resolve`
// and the fence vocabulary: the method's params (with the operator
// attestation), §9's `orphanBoundary` / `BoundaryEntry[]` five-variant union,
// and the fence discriminators (`fencing-failure`, `fencing-helper-absent`,
// `fencing-helper-untrusted`, `orphan-fenced-busy`) with their data shapes.
//
// §9: "`BoundaryEntry` is a five-variant union whose `kind` discriminator
// selects the boundary the record's open state requires" — so each variant is
// its own named Go struct, and the union marshals as exactly the arm it
// carries, never as a merged shape with optional-ified fields the
// absent-when-unknown rule cannot distinguish.

import (
	"encoding/json"
	"errors"
	"fmt"
)

// The union's two construction errors: a boundary entry carrying no arm or
// more than one is not a value any response may hide.
var (
	errBoundaryEntryNoArm        = errors.New("appwire: boundary entry carries no arm")
	errBoundaryEntryMultipleArms = errors.New("appwire: boundary entry carries more than one arm")
)

// HostOrphanResolveAttestation is §5's operator attestation as the params carry
// it: the operator identity, the one accepted statement
// (`orphan-verified-absent`), the record the attestation names, the custody
// boundary reference a `boundary-unavailable` record is matched through, and
// the RFC3339 instant the absence was observed.
type HostOrphanResolveAttestation struct {
	Operator    string `json:"operator"`
	Statement   string `json:"statement"`
	RecordID    string `json:"recordId"`
	BoundaryRef string `json:"boundaryRef,omitempty"`
	ObservedAt  string `json:"observedAt"`
}

// HostOrphanResolveParams is the evener/host/orphan-resolve payload (§5/§9):
// the controller-assigned record id of an `orphan-unverified`-class record,
// plus the optional operator attestation — required on a `boundary-unavailable`
// record, accepted-but-unneeded elsewhere. It carries no idempotency key: the
// id is the operation, and a retry of an already-resolved id replays the
// persisted resolution.
type HostOrphanResolveParams struct {
	ID          string                        `json:"id"`
	Attestation *HostOrphanResolveAttestation `json:"attestation,omitempty"`
}

// The five `kind` discriminator values §9's BoundaryEntry union carries.
const (
	BoundaryKindLocalLinux      = "local-linux"
	BoundaryKindLocalDarwin     = "local-darwin"
	BoundaryKindLocalMarkerless = "local-markerless"
	BoundaryKindRemoteFencing   = "remote-fencing"
	BoundaryKindUnavailable     = "boundary-unavailable"
)

// BoundaryEntryLocalLinux is §9's marked local Linux arm: the kernel-enforced
// cgroup identity plus the launcher-observed `pid`/`startTime` instance marker
// bound to the pre-spawn nonce. The pair is carried verbatim — cgroupfs hosts
// no app-written marker, and the pair is not derivable from `cgroupId` plus
// `nonce`, so the verifier never reconstructs it.
type BoundaryEntryLocalLinux struct {
	Kind      string `json:"kind"`
	CgroupID  string `json:"cgroupId"`
	Nonce     string `json:"nonce"`
	PID       int    `json:"pid"`
	StartTime string `json:"startTime"`
}

// BoundaryEntryLocalDarwin is §9's marked local Darwin arm: the
// (pgid, session id) boundary pair plus the launcher-observed (pid, start time)
// instance marker bound to the pre-spawn nonce.
type BoundaryEntryLocalDarwin struct {
	Kind      string `json:"kind"`
	PGID      int    `json:"pgid"`
	SessionID int    `json:"sessionId"`
	PID       int    `json:"pid"`
	StartTime string `json:"startTime"`
	Nonce     string `json:"nonce"`
}

// BoundaryEntryLocalMarkerless is §9's markerless local arm: the persisted
// pre-spawn boundary only — platform plus the cgroup/pgid identity and the
// nonce, never a launcher-observed pair, because the crash landed before the
// marker persist.
type BoundaryEntryLocalMarkerless struct {
	Kind      string `json:"kind"`
	Platform  string `json:"platform"`
	CgroupID  string `json:"cgroupId,omitempty"`
	PGID      *int   `json:"pgid,omitempty"`
	SessionID *int   `json:"sessionId,omitempty"`
	Nonce     string `json:"nonce"`
}

// BoundaryLeaseOwnership is §9's required ownership identity for one persisted
// lease entry, a three-variant value union — `{pid, pidStartTime}` |
// `{nonce}` | `{cgroupId}` — of which exactly one variant is present. An entry
// persisted without its identity fails closed at verify time: no kill, no clear.
type BoundaryLeaseOwnership struct {
	PID          *int   `json:"pid,omitempty"`
	PIDStartTime string `json:"pidStartTime,omitempty"`
	Nonce        string `json:"nonce,omitempty"`
	CgroupID     string `json:"cgroupId,omitempty"`
}

// BoundaryLeaseEntry is §9's persisted lease-tracked command: the command, its
// RFC3339 registration time, and its required ownership identity.
type BoundaryLeaseEntry struct {
	Command      string                 `json:"command"`
	RegisteredAt string                 `json:"registeredAt"`
	Ownership    BoundaryLeaseOwnership `json:"ownership"`
}

// BoundaryEntryRemoteFencing is §9's remote-fencing arm: the timed-out
// operation's fencing epoch, the guard-file epoch, and the superseded epoch's
// lease-tracked entries with their ownership identities — everything
// `orphan-resolve` needs to enumerate and exit-confirm each entry against the
// live lease state.
type BoundaryEntryRemoteFencing struct {
	Kind         string               `json:"kind"`
	FencingEpoch FencingEpoch         `json:"fencingEpoch"`
	GuardEpoch   uint64               `json:"guardEpoch"`
	LeaseEntries []BoundaryLeaseEntry `json:"leaseEntries"`
}

// BoundaryEntryUnavailable is §9's custody sentinel: a boundary the corruption
// destroyed, naming the custody file it came from. It is never an empty array
// (which is verified empty) and resolves only with the operator attestation §5
// requires.
type BoundaryEntryUnavailable struct {
	Kind       string `json:"kind"`
	Reason     string `json:"reason"`
	CustodyRef string `json:"custodyRef"`
}

// BoundaryEntry is §9's five-variant union: exactly one arm is set, and the
// union marshals as the arm it carries so a client branches on the `kind`
// discriminator rather than on a flattened shape. Each arm is registered in
// FieldUnions, so the generated TypeScript spells this type as the union over
// the arms.
type BoundaryEntry struct {
	*BoundaryEntryLocalLinux
	*BoundaryEntryLocalDarwin
	*BoundaryEntryLocalMarkerless
	*BoundaryEntryRemoteFencing
	*BoundaryEntryUnavailable
}

// MarshalJSON renders the one arm the union carries. Exactly one arm must be
// set; nothing is a programming error no response may hide.
func (u BoundaryEntry) MarshalJSON() ([]byte, error) {
	arms := make([]any, 0, 5)
	for _, arm := range []any{
		u.BoundaryEntryLocalLinux, u.BoundaryEntryLocalDarwin, u.BoundaryEntryLocalMarkerless,
		u.BoundaryEntryRemoteFencing, u.BoundaryEntryUnavailable,
	} {
		if arm != nil && !isNilArm(arm) {
			arms = append(arms, arm)
		}
	}
	if len(arms) == 0 {
		return nil, errBoundaryEntryNoArm
	}
	if len(arms) > 1 {
		return nil, errBoundaryEntryMultipleArms
	}
	return json.Marshal(arms[0])
}

// UnmarshalJSON reads the arm the `kind` discriminator names, and refuses
// anything else: a member whose kind is none of the five values is not a
// boundary entry this protocol defines, so a client fails loudly instead of
// reading a zero-valued arm.
func (u *BoundaryEntry) UnmarshalJSON(raw []byte) error {
	var probe struct {
		Kind string `json:"kind"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		return err
	}
	*u = BoundaryEntry{}
	switch probe.Kind {
	case BoundaryKindLocalLinux:
		arm := BoundaryEntryLocalLinux{}
		if err := json.Unmarshal(raw, &arm); err != nil {
			return err
		}
		u.BoundaryEntryLocalLinux = &arm
	case BoundaryKindLocalDarwin:
		arm := BoundaryEntryLocalDarwin{}
		if err := json.Unmarshal(raw, &arm); err != nil {
			return err
		}
		u.BoundaryEntryLocalDarwin = &arm
	case BoundaryKindLocalMarkerless:
		arm := BoundaryEntryLocalMarkerless{}
		if err := json.Unmarshal(raw, &arm); err != nil {
			return err
		}
		u.BoundaryEntryLocalMarkerless = &arm
	case BoundaryKindRemoteFencing:
		arm := BoundaryEntryRemoteFencing{}
		if err := json.Unmarshal(raw, &arm); err != nil {
			return err
		}
		u.BoundaryEntryRemoteFencing = &arm
	case BoundaryKindUnavailable:
		arm := BoundaryEntryUnavailable{}
		if err := json.Unmarshal(raw, &arm); err != nil {
			return err
		}
		u.BoundaryEntryUnavailable = &arm
	default:
		return &UnknownBoundaryKindError{Kind: probe.Kind}
	}
	return nil
}

// UnknownBoundaryKindError reports a boundary member whose kind is outside §9's
// closed five-variant set.
type UnknownBoundaryKindError struct {
	Kind string
}

func (e *UnknownBoundaryKindError) Error() string {
	return fmt.Sprintf("appwire: boundary entry carries unknown kind %q", e.Kind)
}

// ---------------------------------------------------------------------------
// §8's fence discriminators
// ---------------------------------------------------------------------------

// OrphanFenceRefusalMethods names the two recovery mutations whose orphan-fence
// refusal carries `orphan-fenced-busy` (crash-fencing §8/§10:205). Every other
// fenced call keeps `host-busy-transient`, and a call on a host whose
// fencing-quarantine marker is open refuses with `fencing-failure` instead
// (precedence: the quarantine refusal wins wherever the marker is present).
var OrphanFenceRefusalMethods = []string{MethodEvenerHostTeardownRetry, MethodEvenerHostTeardownRecover}

// FencingFailureErrorData is §8's fencing-failure data: the quarantined host.
type FencingFailureErrorData struct {
	ErrorData
	Host string `json:"host"`
}

// FencingFailure is the quarantine refusal (conflict class): the host is closed
// by an open fencing-quarantine marker until `orphan-resolve` clears it.
func FencingFailure(host, message string) WireError {
	return WireError{
		Code:    CodeConflict,
		Message: message,
		Data: FencingFailureErrorData{
			ErrorData: ErrorData{EvenerErrorInfo: ErrorFencingFailure},
			Host:      host,
		},
	}
}

// FencingHelperErrorData is §8's helper-gate data: the host plus the pinned
// helper version the operator must act on out-of-band — the version to install
// for the absent arm, the distrusted version for the untrusted arm.
type FencingHelperErrorData struct {
	ErrorData
	Host    string `json:"host"`
	Version string `json:"version"`
}

// FencingHelperAbsent is §8's absent-helper refusal (conflict class): no helper
// is installed at the pinned path, the remote cannot run the helper, or the
// bootstrap-guard claim was lost or unverifiable. Never `probe-failed`, so the
// client never mistakes the gate for a retryable probe failure.
func FencingHelperAbsent(host, version, message string) WireError {
	return WireError{
		Code:    CodeConflict,
		Message: message,
		Data: FencingHelperErrorData{
			ErrorData: ErrorData{EvenerErrorInfo: ErrorFencingHelperAbsent},
			Host:      host,
			Version:   version,
		},
	}
}

// FencingHelperUntrusted is §8's untrusted-helper refusal (conflict class): an
// older, incompatible, or explicitly untrusted helper. Same data shape as the
// absent arm, naming the distrusted version in place of the absent one.
func FencingHelperUntrusted(host, version, message string) WireError {
	return WireError{
		Code:    CodeConflict,
		Message: message,
		Data: FencingHelperErrorData{
			ErrorData: ErrorData{EvenerErrorInfo: ErrorFencingHelperUntrusted},
			Host:      host,
			Version:   version,
		},
	}
}

// OrphanFencedBusyErrorData is §8's `orphan-fenced-busy` data: the blocking
// `orphan-unverified` record's controller-assigned id.
type OrphanFencedBusyErrorData struct {
	ErrorData
	RecordID string `json:"recordId"`
}

// OrphanFencedBusy is the teardown-retry/teardown-recover orphan-fence refusal
// (conflict class): the host holds an open `orphan-unverified` record, and the
// repair call degrades to an orphan-must-resolve-first refusal naming the
// blocking record — never the generic transient-busy form.
func OrphanFencedBusy(recordID, message string) WireError {
	return WireError{
		Code:    CodeConflict,
		Message: message,
		Data: OrphanFencedBusyErrorData{
			ErrorData: ErrorData{EvenerErrorInfo: ErrorOrphanFencedBusy},
			RecordID:  recordID,
		},
	}
}
