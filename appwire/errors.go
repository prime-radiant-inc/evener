package appwire

import (
	"errors"
	"fmt"
	"reflect"
)

const (
	CodeParseError     = -32700
	CodeInvalidRequest = -32600
	CodeMethodNotFound = -32601
	CodeInvalidParams  = -32602
	CodeInternalError  = -32603
	CodeConflict       = -32013
	CodeUnavailable    = -32014
)

type ErrorInfo string

const (
	ErrorInvalidParams ErrorInfo = "invalidParams"
	// ErrorInvalidHostField marks a host mutation's validation refusal, whose
	// data names the input that failed (HostFieldErrorData.Field, spelled the
	// way HostEntry's json tags spell it). It shares CodeInvalidParams with
	// plain validation refusals, so a client that wants to place a message on
	// an input must match this discriminant, never the code.
	ErrorInvalidHostField          ErrorInfo = "invalidHostField"
	ErrorResourceNotFound          ErrorInfo = "resourceNotFound"
	ErrorMethodNotFound            ErrorInfo = "methodNotFound"
	ErrorProviderUnavailable       ErrorInfo = "providerUnavailable"
	ErrorSessionUnavailable        ErrorInfo = "sessionUnavailable"
	ErrorConflict                  ErrorInfo = "conflict"
	ErrorActionUnavailable         ErrorInfo = "actionUnavailable"
	ErrorHubLaunch                 ErrorInfo = "hubLaunch"
	ErrorQueuedDrainPartial        ErrorInfo = "queuedDrainPartial"
	ErrorMutationOutcomeUnknown    ErrorInfo = "mutationOutcomeUnknown"
	ErrorTranscriptItemCursorStale ErrorInfo = "transcriptItemCursorStale"
	ErrorInternal                  ErrorInfo = "internal"
	// ErrorTranscriptHistoryFailed marks a read of a thread whose history
	// entered its failed state: its projection or rebuild failed for a reason
	// outside the entries (I/O, a corrupt index) three times in a row, and a
	// rebuild the read attempted failed too. The message names the last entry
	// the history had recorded when it failed. An entry that does not decode
	// is not a failure: the index shows it as one unreadable-entry item. Its
	// data is HistoryReadErrorData.
	ErrorTranscriptHistoryFailed ErrorInfo = "transcriptHistoryFailed"
	// ErrorUpgradeRequired marks initialize refusing a client that announced
	// an older AppWire protocol than the server speaks; the message names both
	// versions.
	ErrorUpgradeRequired ErrorInfo = "upgradeRequired"
	// ErrorPathOutsideSession marks a file read whose path, or a symlink along
	// it, leads outside the session's working directory. It shares
	// CodeInvalidParams; the controller's /doc/file proxy maps it to 403, the
	// status the local route answers.
	ErrorPathOutsideSession ErrorInfo = "pathOutsideSession"
	// ErrorKeybindingsPostRename marks a keybindings patch that APPLIED (the
	// rename published the new revision) before a follow-up durable step
	// failed; the error's data carries the applied canonical state.
	ErrorKeybindingsPostRename ErrorInfo = "keybindingsPostRename"
	// ErrorInstanceRenamePersisted marks a provider-instance rename that
	// APPLIED (providers.toml carries the new name) before the follow-up
	// credential move or reload failed; the message names what was left behind.
	// The rename is on disk, so clients must report it as the standing write it
	// was and steer to the new name rather than report a failed save - the old
	// name is gone and re-issuing the rename can only fail on a missing
	// instance.
	ErrorInstanceRenamePersisted ErrorInfo = "instanceRenamePersisted"
	// ErrorInstanceRemoveApplied marks a provider-instance removal whose
	// credential deletion APPLIED (the instance's stored key or OAuth record is
	// gone, or its config entry is) before a later step failed; the message
	// names what was left behind. The removal stands, so clients reconcile it
	// - dropping the confirmation and any state retained for the name - rather
	// than report a failed remove whose retry targets a missing instance.
	ErrorInstanceRemoveApplied ErrorInfo = "instanceRemoveApplied"
	// ErrorEndpointConflict marks the hub's refusal of an asserted destination:
	// the name no longer resolves to the endpoint the client showed the user (an
	// edit or removal assertion, a credential write or test, or a sign-in flow's
	// captured endpoint). It shares CodeConflict with genuine conflicts (a name
	// collision, an expired flow), so clients must match this discriminant, never
	// the code - otherwise a create collision reads as a moved endpoint.
	ErrorEndpointConflict ErrorInfo = "endpointConflict"
	// ErrorTranscriptDisplayPostApply marks a transcript-display patch that
	// APPLIED (the rename published the new revision) before a follow-up
	// durable step failed; the error's data carries the applied canonical
	// state. Same rule as ErrorKeybindingsPostRename, for the other store.
	ErrorTranscriptDisplayPostApply ErrorInfo = "transcriptDisplayPostApply"
	// ErrorMarketplaceUnregisteredCloneRemains marks a marketplace removal
	// that APPLIED (the unregister save landed; Applied carries the updated
	// marketplace list) before its clone's removal from disk failed. A
	// client should reconcile from Applied instead of treating the removal
	// as rejected - same rule as ErrorKeybindingsPostRename, for a delete
	// instead of a patch, and a retry finds ErrMarketplaceNotFound rather
	// than repeating this error.
	ErrorMarketplaceUnregisteredCloneRemains ErrorInfo = "marketplaceUnregisteredCloneRemains"
	// ErrorMarketplaceRemoveApplied marks a marketplace removal that APPLIED
	// (the unregister save landed and its clone cleanup, if any, completed)
	// but whose response could not carry the updated list, because the fresh
	// read that builds it failed. Same rule as
	// ErrorMarketplaceUnregisteredCloneRemains: a consumer reconciling a
	// removal treats this as the removal standing, never retries it (a retry
	// finds ErrMarketplaceNotFound), and never reads the missing list as
	// "every marketplace gone". Distinct from the litter discriminant so a
	// consumer can tell no-litter from litter - nothing was left on disk here,
	// so this one carries no leftover-files warning. The consumer half of that
	// rule is deliberately deferred to the marketplace reconciliation
	// successors (#1954 SDK, #1960 web); until one binds this discriminant, a
	// client has no binding for it and still surfaces the removal as a failed
	// mutation.
	ErrorMarketplaceRemoveApplied ErrorInfo = "marketplaceRemoveApplied"
	// ErrorStaleEntry marks a deploy-pipeline refusal whose subject drifted out
	// from under the request: the resolved host entry, the resolved deploy
	// target, the registry's (generation, incarnation id) pair, the host's own
	// hub.toml entry fingerprint, the re-probed running revision or health, the
	// token-bound facts age, or a concurrent terminal operation on the host
	// (deploy pipeline 08b §11). It shares CodeConflict with genuine conflicts,
	// so a client must match this discriminant and its Binding, never the code.
	ErrorStaleEntry ErrorInfo = "stale-entry"
	// ErrorCursorTooLarge marks deploy pipeline 08b's over-cap first-page
	// refusal (§§8, 11): an operations read whose boundary map would exceed the
	// 8 KiB encoded cursor cap, so no cursor was minted and the client re-lists
	// with a narrower query. Its data is CursorTooLargeErrorData — never a
	// compacting `compactSeq`, because there is no cursor to name one. Shares
	// CodeConflict with the other refusals, so a client matches this
	// discriminant, never the code.
	ErrorCursorTooLarge ErrorInfo = "cursor-too-large"
	// ErrorCursorInvalidated marks deploy pipeline 08b's mid-pagination
	// compaction refusal (§§8, 11): a compaction removed rows at or before the
	// cursor's `pos` since the cursor was minted, so the continuation cannot
	// describe the record set it resumes into and the client restarts from the
	// first page. Its data is CursorInvalidatedErrorData — the compacting
	// `compactSeq` (the envelope-global value, never the live one when a later
	// compaction advanced it) plus the affected host's bounds entry as stored at
	// mint. Distinct from stale-entry's generation-mismatch re-list refusal;
	// shares CodeConflict with the other refusals, so a client matches this
	// discriminant, never the code.
	ErrorCursorInvalidated ErrorInfo = "cursor-invalidated"
	// ErrorHostBusyOperation marks a deploy-pipeline refusal because the host's
	// per-host gate is held by a deploy/restart operation — including an
	// Ensure-triggered deploy, which holds its own operation-store record
	// (deploy pipeline 08b §5). The data names the controller-assigned record id
	// the UI's open/wait-able reference resolves through; the UI shows
	// open/wait, never a bare retry.
	ErrorHostBusyOperation ErrorInfo = "host-busy-operation"
	// ErrorHostBusyTransient marks the same busy class with no operation
	// reference: `plan`'s validation-plus-mint window. The UI retries with
	// backoff and shows no open/wait affordance.
	ErrorHostBusyTransient ErrorInfo = "host-busy-transient"
	// ErrorTokenMissing marks a deploy presenting a token the store holds no
	// row for: nothing was minted, or the row is gone (consumed, superseded,
	// revoked, or reaped). A consumed token presented again reads as this — a
	// consumed row is deleted, and gone rows never validate (deploy pipeline
	// 08b §§3, 6 step 4, 11).
	ErrorTokenMissing ErrorInfo = "token-missing"
	// ErrorTokenMismatched marks a presented value that is not a confirmation
	// token of this store at all: outside the wire's token shape, or a live
	// token bound to another host (deploy pipeline 08b §11).
	ErrorTokenMismatched ErrorInfo = "token-mismatched"
	// ErrorTokenSuperseded marks a value that was a token for the host but is
	// no longer the host's current one: a later mint replaced the nonce, and
	// the supersede-on-mint write deleted the row (deploy pipeline 08b §§3,
	// 11). Shares CodeConflict with the other token refusals.
	ErrorTokenSuperseded ErrorInfo = "token-superseded"
	// ErrorTokenExpired marks a token whose deadline the store has observed
	// passing, or a corrupt row whose own capture timestamp postdates the
	// durable wall-clock mark (deploy pipeline 08b §§3, 6 step 4, 11).
	ErrorTokenExpired ErrorInfo = "token-expired"
	// ErrorConflictingOperationID marks a client operation ID already used up
	// by a current-generation record of a different host or kind, or by a
	// current-generation `host-removed` record (deploy pipeline 08b §§4, 11).
	ErrorConflictingOperationID ErrorInfo = "conflicting-operation-id"
	// ErrorConflictingMutationID marks a mutationId already used up by a
	// current-generation receipt of a different host name or mutation kind
	// (registry spec 08 §5, §11; the operation store's cross-name rule applied
	// to host mutations). It rides the same AppWire error envelope as
	// conflicting-operation-id, so a client matches this discriminant, never
	// the code.
	ErrorConflictingMutationID ErrorInfo = "conflicting-mutation-id"
	// ErrorHostDetached marks a deploy refused because the host's channel is
	// gone — at the gated probe, or at a revalidation under the same gate.
	// Token unconsumed, no record; the client Connects and re-plans (deploy
	// pipeline 08b §§6, 11). Unavailable class.
	ErrorHostDetached ErrorInfo = "host-detached"
	// ErrorProbeFailed marks a deploy step-(3) running re-probe whose read
	// failed, timed out, or was unauthenticated. Token unconsumed, no record
	// (deploy pipeline 08b §§6 step 3, 11). Unavailable class. Distinct from
	// plan's `probe-failed` no-token reason, which is never an envelope.
	ErrorProbeFailed ErrorInfo = "probe-failed"
	// ErrorRemnantOpen marks a deploy/restart/Ensure refusal because an open
	// teardown remnant fences the name (deploy pipeline 08b §6 step 2, §11;
	// remnant semantics are the registry spec's §6). Conflict class, with the
	// blocking remnant's id in the data.
	ErrorRemnantOpen ErrorInfo = "remnant-open"
	// ErrorTombstoneCapacity marks a tombstone persist that fits only by
	// evicting a remnant-gated tombstone (registry spec 08 §12, §15). Conflict
	// class, with the exceeded bound and the blocking remnant-gated names in
	// the data: the operator resolves a remnant through teardown-retry first,
	// then retries the removal.
	ErrorTombstoneCapacity ErrorInfo = "tombstone-capacity"
	// ErrorTeardownUnknownKey marks `evener/host/teardown-retry` naming a
	// remnant id the store does not carry — never minted, or purged by the
	// cleared/recovery marker retention (registry spec 08 §6/§11). Not-found
	// class, with the unknown id in the data. A cleared remnant whose resolved
	// record still survives is NOT this arm: it returns `already-cleared`.
	ErrorTeardownUnknownKey ErrorInfo = "teardown-unknown-key"
	// ErrorConcurrentEdit marks a hub.toml commit whose final fingerprint check
	// found the file moved between the validation read and the check, after
	// bounded retries (registry spec 08 §6/§11): no window's edit is erased and
	// nothing committed. Conflict class, with both fingerprints in the data.
	// The live-external reconcile validation (§15) rides the same discriminator
	// with `{source, hostCount}` data on its own path.
	ErrorConcurrentEdit ErrorInfo = "concurrent-edit"
)

// StaleEntryBinding names which binding a stale-entry refusal fired on, exactly
// as deploy pipeline 08b §11 spells each value.
type StaleEntryBinding string

const (
	StaleEntryBindingEntry                StaleEntryBinding = "entry"
	StaleEntryBindingTarget               StaleEntryBinding = "target"
	StaleEntryBindingGeneration           StaleEntryBinding = "generation"
	StaleEntryBindingHubTOMLFingerprint   StaleEntryBinding = "hub.toml-fingerprint"
	StaleEntryBindingRunningVersion       StaleEntryBinding = "running-version"
	StaleEntryBindingRunningHealth        StaleEntryBinding = "running-health"
	StaleEntryBindingFactsAge             StaleEntryBinding = "facts-age"
	StaleEntryBindingConcurrentTerminalOp StaleEntryBinding = "concurrent-terminal-op"
	StaleEntryBindingPrunedGeneration     StaleEntryBinding = "pruned-generation"
)

// StaleEntryErrorData is a stale-entry refusal's data: the standard ErrorData
// plus the binding that drifted, so a client re-plans or re-lists on the value
// it reads instead of parsing prose.
type StaleEntryErrorData struct {
	ErrorData
	Binding StaleEntryBinding `json:"binding"`
}

// StaleEntry is the refusal a deploy-pipeline path emits when a binding drifted.
// binding is the half that moved; message is the hub's own prose, unchanged.
func StaleEntry(binding StaleEntryBinding, message string) WireError {
	return WireError{
		Code:    CodeConflict,
		Message: message,
		Data: StaleEntryErrorData{
			ErrorData: ErrorData{EvenerErrorInfo: ErrorStaleEntry},
			Binding:   binding,
		},
	}
}

// CursorTooLargeErrorData is the over-cap first-page refusal's data (deploy
// pipeline 08b §11): the standard ErrorData plus the encoded cursor cap the
// boundary map exceeded. It never carries a compacting `compactSeq` — no
// cursor was minted, so there is none to name (§8).
type CursorTooLargeErrorData struct {
	ErrorData
	CapBytes int `json:"capBytes"`
}

// CursorTooLarge is deploy pipeline 08b's `cursor-too-large` refusal: a first
// page whose boundary map would exceed the 8 KiB encoded cursor cap. The
// client re-lists with a narrower query, never a truncated cursor.
func CursorTooLarge(capBytes int, message string) WireError {
	return WireError{
		Code:    CodeConflict,
		Message: message,
		Data: CursorTooLargeErrorData{
			ErrorData: ErrorData{EvenerErrorInfo: ErrorCursorTooLarge},
			CapBytes:  capBytes,
		},
	}
}

// CursorInvalidatedErrorData is the mid-pagination compaction refusal's data
// (deploy pipeline 08b §11): the standard ErrorData plus the compacting
// `compactSeq` (the envelope-global value), the affected host, and that host's
// `bounds` entry as stored at the cursor's mint — the {generation,
// incarnationId, presenceEpoch} object, or the literal "absent"
// (HostBoundaryAbsent), the same value union hostBoundaries carries.
type CursorInvalidatedErrorData struct {
	ErrorData
	CompactSeq uint64 `json:"compactSeq"`
	Host       string `json:"host"`
	Bounds     any    `json:"bounds"`
}

// CursorInvalidated is deploy pipeline 08b's `cursor-invalidated` refusal: a
// mid-pagination compaction removed rows at or before the cursor's position,
// so the client restarts from the first page. bounds carries the affected
// host's stored bounds entry — a HostBoundary, or HostBoundaryAbsent.
func CursorInvalidated(compactSeq uint64, host string, bounds any, message string) WireError {
	return WireError{
		Code:    CodeConflict,
		Message: message,
		Data: CursorInvalidatedErrorData{
			ErrorData:  ErrorData{EvenerErrorInfo: ErrorCursorInvalidated},
			CompactSeq: compactSeq,
			Host:       host,
			Bounds:     bounds,
		},
	}
}

// HostBusyOperationErrorData is the operation-held busy refusal's data: the
// standard ErrorData plus the controller-assigned operation record id the
// caller's open/wait-able reference resolves through (`operations`' detail
// filter `id`). It is present exactly on the operation class.
type HostBusyOperationErrorData struct {
	ErrorData
	OperationID string `json:"operationId"`
}

// HostBusyOperation is the refusal a deploy-pipeline path emits while a
// deploy/restart operation holds the host's gate. operationID is the
// controller-assigned record id; message is the hub's own prose, unchanged.
func HostBusyOperation(operationID, message string) WireError {
	return WireError{
		Code:    CodeConflict,
		Message: message,
		Data: HostBusyOperationErrorData{
			ErrorData:   ErrorData{EvenerErrorInfo: ErrorHostBusyOperation},
			OperationID: operationID,
		},
	}
}

// HostBusyTransient is the refusal a deploy-pipeline path emits while the
// host's gate is held by a holder with no operation record — `plan`'s
// validation-plus-mint window. It carries no operation reference, so a client
// retries with backoff instead of offering open/wait.
func HostBusyTransient(message string) WireError {
	return WireError{
		Code:    CodeConflict,
		Message: message,
		Data:    ErrorData{EvenerErrorInfo: ErrorHostBusyTransient},
	}
}

// tokenRefusal renders one of the four token refusals §11 pins in the conflict
// class. They carry no data beyond the discriminator: the client branches on
// the info value, and the hub's prose names what it saw.
func tokenRefusal(info ErrorInfo, message string) WireError {
	return WireError{
		Code:    CodeConflict,
		Message: message,
		Data:    ErrorData{EvenerErrorInfo: info},
	}
}

// TokenMissing is §11's `token-missing` refusal: no row for the host, or the
// row is gone.
func TokenMissing(message string) WireError { return tokenRefusal(ErrorTokenMissing, message) }

// TokenMismatched is §11's `token-mismatched` refusal.
func TokenMismatched(message string) WireError { return tokenRefusal(ErrorTokenMismatched, message) }

// TokenSuperseded is §11's `token-superseded` refusal.
func TokenSuperseded(message string) WireError { return tokenRefusal(ErrorTokenSuperseded, message) }

// TokenExpired is §11's `token-expired` refusal.
func TokenExpired(message string) WireError { return tokenRefusal(ErrorTokenExpired, message) }

// ConflictingOperationID is §11's `conflicting-operation-id` refusal: the
// client operation ID is already used up by a current-generation record of a
// different host or kind, or by a current-generation host-removed record.
func ConflictingOperationID(message string) WireError {
	return WireError{
		Code:    CodeConflict,
		Message: message,
		Data:    ErrorData{EvenerErrorInfo: ErrorConflictingOperationID},
	}
}

// ConflictingMutationID is §11's `conflicting-mutation-id` refusal: the
// client's mutationId is already used up by a current-generation receipt of a
// different host name or mutation kind. It carries the discriminator only —
// no data — exactly like its operation-id sibling.
func ConflictingMutationID(message string) WireError {
	return WireError{
		Code:    CodeConflict,
		Message: message,
		Data:    ErrorData{EvenerErrorInfo: ErrorConflictingMutationID},
	}
}

// The two bound values §15's `tombstone-capacity` data names: the global
// tombstone-count cap or the global serialized-bytes cap.
const (
	TombstoneCapacityBoundCount = "count"
	TombstoneCapacityBoundBytes = "bytes"
)

// TombstoneCapacityErrorData is §12's `tombstone-capacity` data: the bound the
// persist would exceed and the remnant-gated names blocking every eviction
// candidate.
type TombstoneCapacityErrorData struct {
	ErrorData
	Bound         string   `json:"bound"`
	BlockingNames []string `json:"blockingNames"`
}

// TombstoneCapacity is §12's `tombstone-capacity` refusal: a tombstone persist
// that would exceed a global bound even after evicting every evictable
// tombstone, because every remaining candidate but the incoming tombstone is
// remnant-gated (§15). Conflict class.
func TombstoneCapacity(bound string, blockingNames []string, message string) WireError {
	return WireError{
		Code:    CodeConflict,
		Message: message,
		Data: TombstoneCapacityErrorData{
			ErrorData:     ErrorData{EvenerErrorInfo: ErrorTombstoneCapacity},
			Bound:         bound,
			BlockingNames: append([]string(nil), blockingNames...),
		},
	}
}

// HostDetached is §11's `host-detached` refusal: the host's channel is gone.
// Token unconsumed, no record; the client Connects and re-plans.
func HostDetached(message string) WireError {
	return WireError{
		Code:    CodeUnavailable,
		Message: message,
		Data:    ErrorData{EvenerErrorInfo: ErrorHostDetached},
	}
}

// The three probe-failure values §11's `probe-failed` data names.
const (
	ProbeFailureReadFailed      = "read-failed"
	ProbeFailureTimedOut        = "timed-out"
	ProbeFailureUnauthenticated = "unauthenticated"
)

// ProbeFailedErrorData is §11's `probe-failed` data: the host probed plus
// which of the three failures it was.
type ProbeFailedErrorData struct {
	ErrorData
	Host    string `json:"host"`
	Failure string `json:"failure"`
}

// ProbeFailed is §11's `probe-failed` refusal (unavailable class): the deploy
// step-(3) running re-probe read failed, timed out, or was unauthenticated.
// Token unconsumed, no record.
func ProbeFailed(host, failure, message string) WireError {
	return WireError{
		Code:    CodeUnavailable,
		Message: message,
		Data: ProbeFailedErrorData{
			ErrorData: ErrorData{EvenerErrorInfo: ErrorProbeFailed},
			Host:      host,
			Failure:   failure,
		},
	}
}

// RemnantOpenErrorData is §11's `remnant-open` data: the id of the open
// teardown remnant blocking the name.
type RemnantOpenErrorData struct {
	ErrorData
	RemnantID string `json:"remnantId"`
}

// RemnantOpen is §11's `remnant-open` refusal (conflict class): an open
// teardown remnant fences the name, past the dedup check and before any probe
// or acquisition.
func RemnantOpen(remnantID, message string) WireError {
	return WireError{
		Code:    CodeConflict,
		Message: message,
		Data: RemnantOpenErrorData{
			ErrorData: ErrorData{EvenerErrorInfo: ErrorRemnantOpen},
			RemnantID: remnantID,
		},
	}
}

type MutationOutcome string

const (
	MutationOutcomeNotAccepted   MutationOutcome = "notAccepted"
	MutationOutcomeUnknown       MutationOutcome = "unknown"
	MutationOutcomeTargetDeleted MutationOutcome = "targetDeleted"
)

type RetryDisposition string

const (
	RetryDispositionAutomatic RetryDisposition = "automatic"
	RetryDispositionBlocked   RetryDisposition = "blocked"
	RetryDispositionNone      RetryDisposition = "none"
)

type ErrorData struct {
	EvenerErrorInfo  ErrorInfo        `json:"evenerErrorInfo"`
	ClientMutationID string           `json:"clientMutationId,omitempty"`
	MutationOutcome  MutationOutcome  `json:"mutationOutcome,omitempty"`
	RetryDisposition RetryDisposition `json:"retryDisposition,omitempty"`
	Cause            string           `json:"cause,omitempty"`
}

// HostFieldErrorData is a host mutation's validation-refusal data (component 08
// slice 2): the standard ErrorData plus the input the refusal blames, spelled
// the way the wire names that input (HostEntry's json tags) so a dialog places
// the message without parsing prose. A refusal that blames the entry as a
// whole — a cycle — leaves Field empty and the client shows it form-level. Same
// composition as LifecycleErrorData: the embedded ErrorData keeps every
// consumer that only reads evenerErrorInfo working.
type HostFieldErrorData struct {
	ErrorData
	Field string `json:"field,omitempty"`
}

// InvalidHostField is the validation refusal for a host mutation that blames one
// input. field is HostEntry's wire spelling of that input; an empty field means
// the refusal blames the entry as a whole. message is the hub's own prose,
// unchanged.
func InvalidHostField(field, message string) WireError {
	return WireError{
		Code:    CodeInvalidParams,
		Message: message,
		Data: HostFieldErrorData{
			ErrorData: ErrorData{EvenerErrorInfo: ErrorInvalidHostField},
			Field:     field,
		},
	}
}

type WireError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

func (e WireError) Error() string {
	return e.Message
}

// TransportFailureError reports that Client.Request failed at the transport
// layer rather than by returning an error frame the peer sent: the connection's
// read loop is gone (or its notification buffer overflowed), so the client
// synthesized an InternalError-shaped failure for a request the peer never
// answered. The embedded WireError keeps the shape generic callers already
// match — errors.As still finds a CodeInternalError WireError — while the
// distinct type is the provenance an adapter uses to tell a lost response from
// an InternalError that arrived intact. Without it an adapter has only the
// message text, and reclassifies an application error such as "cannot parse
// transcript: unexpected EOF" as host unavailability, driving an automatic
// mutation retry for a failure that was never a transport loss.
type TransportFailureError struct {
	WireError
}

// Unwrap exposes the synthesized WireError so errors.As(err, &WireError) still
// matches exactly as it did before provenance was retained.
func (e TransportFailureError) Unwrap() error { return e.WireError }

// IsTransportFailure reports whether err is a failure the client synthesized
// because its read loop is gone (see TransportFailureError), as opposed to an
// error frame the peer sent. Adapters check it instead of reconstructing
// transport provenance from message text.
func IsTransportFailure(err error) bool {
	_, ok := errors.AsType[TransportFailureError](err)
	return ok
}

func InvalidParams(message string) WireError {
	return WireError{
		Code:    CodeInvalidParams,
		Message: message,
		Data:    ErrorData{EvenerErrorInfo: ErrorInvalidParams},
	}
}

// TranscriptItemCursorStale reports an item-mode cursor that cannot be used
// for the current transcript incarnation. The cursor itself is intentionally
// never included in the message or error data.
func TranscriptItemCursorStale() WireError {
	return WireError{
		Code:    CodeInvalidParams,
		Message: "transcript item cursor is stale; refresh the thread",
		Data: ErrorData{
			EvenerErrorInfo:  ErrorTranscriptItemCursorStale,
			RetryDisposition: RetryDispositionAutomatic,
		},
	}
}

func PathOutsideSession(message string) WireError {
	return WireError{
		Code:    CodeInvalidParams,
		Message: message,
		Data:    ErrorData{EvenerErrorInfo: ErrorPathOutsideSession},
	}
}

func ResourceNotFound(message string) WireError {
	return WireError{
		Code:    CodeInvalidParams,
		Message: message,
		Data:    ErrorData{EvenerErrorInfo: ErrorResourceNotFound},
	}
}

func InvalidRequest(message string) WireError {
	return WireError{
		Code:    CodeInvalidRequest,
		Message: message,
		Data:    ErrorData{EvenerErrorInfo: ErrorInvalidParams},
	}
}

func MethodNotFound(method string) WireError {
	return WireError{
		Code:    CodeMethodNotFound,
		Message: "method not found: " + method,
		Data:    ErrorData{EvenerErrorInfo: ErrorMethodNotFound},
	}
}

func InternalError(message string) WireError {
	return WireError{
		Code:    CodeInternalError,
		Message: message,
		Data:    ErrorData{EvenerErrorInfo: ErrorInternal},
	}
}

// TranscriptHistoryFailed reports a read of a thread whose history failed,
// naming ordinal, the last entry it had recorded (see
// ErrorTranscriptHistoryFailed). The history read stamps it with
// WithHistoryReadIdentity.
func TranscriptHistoryFailed(ordinal uint64) WireError {
	return WireError{
		Code:    CodeInternalError,
		Message: fmt.Sprintf("thread history failed at entry %d", ordinal),
		Data:    ErrorData{EvenerErrorInfo: ErrorTranscriptHistoryFailed},
	}
}

// HistoryReadErrorData is the data of a thread/read or thread/turns/list
// error: the error's own data plus the boot generation the read ran under,
// and its resync epoch when the read had reached the thread's history. It
// carries no snapshot identity and no items, so a client never adopts a
// generation or epoch from it, and never replaces anything with it.
type HistoryReadErrorData struct {
	ErrorData
	BootGeneration string  `json:"bootGeneration"`
	Epoch          *uint64 `json:"epoch,omitempty"`
}

// WithHistoryReadIdentity stamps a history read's error with the boot
// generation and epoch it ran under. An error whose data is not ErrorData
// keeps its own data unchanged.
func WithHistoryReadIdentity(err WireError, bootGeneration string, epoch uint64) WireError {
	return withReadIdentity(err, bootGeneration, &epoch)
}

// WithReadBootGeneration stamps a read's error raised before the read reached
// a thread's history (invalid params, an unknown thread, an unavailable
// subscription) with the boot generation alone.
func WithReadBootGeneration(err WireError, bootGeneration string) WireError {
	return withReadIdentity(err, bootGeneration, nil)
}

func withReadIdentity(err WireError, bootGeneration string, epoch *uint64) WireError {
	if data, ok := err.Data.(ErrorData); ok {
		err.Data = HistoryReadErrorData{ErrorData: data, BootGeneration: bootGeneration, Epoch: epoch}
	}
	return err
}

// UpgradeRequired refuses a client that announced clientVersion, an AppWire
// protocol older than serverVersion.
func UpgradeRequired(clientVersion, serverVersion string) WireError {
	return WireError{
		Code:    CodeInvalidRequest,
		Message: fmt.Sprintf("protocol version %q is older than this server's %q: upgrade required", clientVersion, serverVersion),
		Data:    ErrorData{EvenerErrorInfo: ErrorUpgradeRequired},
	}
}

func Conflict(message string) WireError {
	return WireError{
		Code:    CodeConflict,
		Message: message,
		Data:    ErrorData{EvenerErrorInfo: ErrorConflict},
	}
}

func MutationNotAccepted(clientMutationID, message string) WireError {
	return WireError{
		Code:    CodeConflict,
		Message: message,
		Data: ErrorData{
			EvenerErrorInfo:  ErrorConflict,
			ClientMutationID: clientMutationID,
			MutationOutcome:  MutationOutcomeNotAccepted,
			RetryDisposition: RetryDispositionNone,
		},
	}
}

// NotAccepted marks the refusal as a request that wasn't carried out and isn't
// to be retried as it is, since it would be refused the same way.
// clientMutationID names the refused mutation, or is empty when the request
// carried none. The code and message stay, and so does the data: the standard
// ErrorData, or a struct that embeds it (HostFieldErrorData,
// LifecycleErrorData and the like), is marked where it stands, keeping its
// evenerErrorInfo and every field of its own. Data of any other shape, or
// none, gives way to a bare ErrorData, so the outcome is always readable.
func (e WireError) NotAccepted(clientMutationID string) WireError {
	e.Data = markedNotAccepted(e.Data, clientMutationID)
	return e
}

var errorDataType = reflect.TypeFor[ErrorData]()

// ErrorDataOf is the standard data an error's Data is, or a struct of it embeds
// (HostFieldErrorData and the like), and whether it has one.
func ErrorDataOf(data any) (ErrorData, bool) {
	if plain, ok := data.(ErrorData); ok {
		return plain, true
	}
	value := reflect.ValueOf(data)
	if value.Kind() != reflect.Struct {
		return ErrorData{}, false
	}
	field, ok := embeddedErrorData(value)
	if !ok {
		return ErrorData{}, false
	}
	return field.Interface().(ErrorData), true
}

// markedNotAccepted is a copy of data with its ErrorData marked: data itself
// when it is one, or the ErrorData a struct embeds. The error data types embed
// ErrorData by value, and Data holds them by value, so reaching the embedded
// one means copying the struct into something settable; reflection does that
// for every such type without each one opting in, so a new wrapper can't be
// missed. Data of any other shape gives way to a fresh ErrorData.
func markedNotAccepted(data any, clientMutationID string) any {
	mark := func(data *ErrorData) {
		data.ClientMutationID = clientMutationID
		data.MutationOutcome = MutationOutcomeNotAccepted
		data.RetryDisposition = RetryDispositionNone
	}
	if plain, ok := data.(ErrorData); ok {
		mark(&plain)
		return plain
	}
	if value := reflect.ValueOf(data); value.Kind() == reflect.Struct {
		copied := reflect.New(value.Type()).Elem()
		copied.Set(value)
		if field, ok := embeddedErrorData(copied); ok {
			mark(field.Addr().Interface().(*ErrorData))
			return copied.Interface()
		}
	}
	var fresh ErrorData
	mark(&fresh)
	return fresh
}

// embeddedErrorData is the ErrorData field a struct value embeds itself,
// settable when the struct is. FieldByName also finds one promoted from a
// deeper embed (a longer Index), which isn't this data's, so that doesn't count.
func embeddedErrorData(value reflect.Value) (reflect.Value, bool) {
	field, ok := value.Type().FieldByName("ErrorData")
	if !ok || !field.Anonymous || field.Type != errorDataType || len(field.Index) != 1 {
		return reflect.Value{}, false
	}
	return value.Field(field.Index[0]), true
}

func MutationUnknown(clientMutationID, message string) WireError {
	return WireError{
		Code:    CodeInternalError,
		Message: message,
		Data: ErrorData{
			EvenerErrorInfo:  ErrorMutationOutcomeUnknown,
			ClientMutationID: clientMutationID,
			MutationOutcome:  MutationOutcomeUnknown,
			RetryDisposition: RetryDispositionBlocked,
			Cause:            "persistenceUnavailable",
		},
	}
}

func Unavailable(message string) WireError {
	return WireError{
		Code:    CodeUnavailable,
		Message: message,
		Data:    ErrorData{EvenerErrorInfo: ErrorActionUnavailable},
	}
}

func SessionUnavailable(message string) WireError {
	return WireError{
		Code:    CodeUnavailable,
		Message: message,
		Data:    ErrorData{EvenerErrorInfo: ErrorSessionUnavailable},
	}
}

func HubLaunchError(message string) WireError {
	return WireError{
		Code:    CodeUnavailable,
		Message: message,
		Data:    ErrorData{EvenerErrorInfo: ErrorHubLaunch},
	}
}

func QueuedDrainPartial(message string) WireError {
	return WireError{
		Code:    CodeConflict,
		Message: message,
		Data:    ErrorData{EvenerErrorInfo: ErrorQueuedDrainPartial},
	}
}

// InstanceRenamePersisted reports a provider-instance rename that stood but
// could not carry the instance's credentials cleanly: the config carries the
// new name while a stored key or OAuth record was left behind. The message
// names what was left; the instance itself is renamed.
func InstanceRenamePersisted(message string) WireError {
	return WireError{
		Code:    CodeInternalError,
		Message: message,
		Data:    ErrorData{EvenerErrorInfo: ErrorInstanceRenamePersisted},
	}
}

// InstanceRemoveApplied reports a provider-instance removal that stood but
// could not put back what it deleted: the config entry is gone, or a credential
// it removed stayed deleted, so the instance no longer resolves. The message
// names what was left; the removal itself is not a failure the caller can
// retry.
func InstanceRemoveApplied(message string) WireError {
	return WireError{
		Code:    CodeInternalError,
		Message: message,
		Data:    ErrorData{EvenerErrorInfo: ErrorInstanceRemoveApplied},
	}
}

// EndpointConflict reports a refusal of an asserted destination: the name does
// not resolve where the client asserted it does. Distinct from Conflict so a
// client can tell a moved endpoint from a genuine conflict such as a name
// collision, which must keep its own message and the form the user typed.
func EndpointConflict(message string) WireError {
	return WireError{
		Code:    CodeConflict,
		Message: message,
		Data:    ErrorData{EvenerErrorInfo: ErrorEndpointConflict},
	}
}

// TeardownUnknownKeyErrorData is §11's `teardown-unknown-key` data: the id the
// call named, so a client can render which handle went stale.
type TeardownUnknownKeyErrorData struct {
	ErrorData
	RemnantID string `json:"remnantId"`
}

// TeardownUnknownKey is §11's `teardown-unknown-key` refusal (not-found class):
// the named remnant id is unknown or purged. It fires exactly when the named
// remnant id is unknown or purged — "a cleared-remnant marker still present
// returns the `already-cleared` arm instead". The not-found class rides
// CodeInvalidParams, the code every other not-found refusal in this envelope
// carries (ResourceNotFound's), because the wire defines no separate code for
// it: a client matches the discriminant, never the code.
func TeardownUnknownKey(remnantID, message string) WireError {
	return WireError{
		Code:    CodeInvalidParams,
		Message: message,
		Data: TeardownUnknownKeyErrorData{
			ErrorData: ErrorData{EvenerErrorInfo: ErrorTeardownUnknownKey},
			RemnantID: remnantID,
		},
	}
}

// ConcurrentEditErrorData is §11's `concurrent-edit` data on the commit path:
// the fingerprint the commit staged against and the fingerprint it observed at
// the final check.
type ConcurrentEditErrorData struct {
	ErrorData
	StagedFingerprint   string `json:"stagedFingerprint"`
	ObservedFingerprint string `json:"observedFingerprint"`
}

// ConcurrentEdit is §11's `concurrent-edit` refusal (conflict class): the
// commit's final check found the hub.toml fingerprint moved after bounded
// retries. The mutation commits nothing, and no window's edit is erased.
func ConcurrentEdit(stagedFingerprint, observedFingerprint, message string) WireError {
	return WireError{
		Code:    CodeConflict,
		Message: message,
		Data: ConcurrentEditErrorData{
			ErrorData:           ErrorData{EvenerErrorInfo: ErrorConcurrentEdit},
			StagedFingerprint:   stagedFingerprint,
			ObservedFingerprint: observedFingerprint,
		},
	}
}
