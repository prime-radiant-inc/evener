package appwire

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
	// ErrorHostBusyOperation marks a deploy-pipeline refusal because the host's
	// per-host gate is held by a deploy/restart operation — including an
	// Ensure-triggered deploy, which holds its own operation-store record
	// (deploy pipeline 08b §5). The data names the controller-assigned record id
	// the UI's open/wait-able reference resolves through; the UI shows
	// open/wait, never a bare retry.
	ErrorHostBusyOperation ErrorInfo = "host-busy-operation"
	// ErrorHostBusyTransient marks the same busy class with no operation
	// reference: `plan`'s validation-plus-mint window (and, where fencing ships,
	// an open `orphan-unverified` fence's non-teardown refusals — crash-fencing
	// spec §8). The UI retries with backoff and shows no open/wait affordance.
	ErrorHostBusyTransient ErrorInfo = "host-busy-transient"
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
