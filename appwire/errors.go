package appwire

import "fmt"

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
	// ErrorHistoryFailed marks a read of a thread whose history entered its
	// failed state: an entry that fails to project, named in the message.
	ErrorHistoryFailed ErrorInfo = "historyFailed"
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
)

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

// HistoryFailed reports a read of a thread whose history failed: the
// transcript entry at ordinal cannot be projected, so no read can serve it.
func HistoryFailed(ordinal uint64) WireError {
	return WireError{
		Code:    CodeInternalError,
		Message: fmt.Sprintf("thread history failed at entry %d", ordinal),
		Data:    ErrorData{EvenerErrorInfo: ErrorHistoryFailed},
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
