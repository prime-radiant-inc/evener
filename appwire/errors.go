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
	ErrorInvalidParams             ErrorInfo = "invalidParams"
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
	// ErrorInstanceRemovePersisted marks a provider-instance removal that
	// APPLIED (the authored entry left providers.toml, the registry reloaded,
	// and the credential the instance resolved is gone) before the removal's
	// follow-up cleanup failed to delete the copy it set the OAuth record aside
	// as; the message names the file still on disk. Clients must report the
	// removal as standing rather than as a failed removal - the instance is
	// gone, and a retry can only fail on a missing instance.
	ErrorInstanceRemovePersisted ErrorInfo = "instanceRemovePersisted"
	// ErrorInstanceRenamePersisted marks a provider-instance rename that
	// APPLIED (providers.toml carries the new name) before the follow-up
	// credential move or reload failed; the message names what was left behind.
	// The rename is on disk, so clients must report it as the standing write it
	// was and steer to the new name rather than report a failed save - the old
	// name is gone and re-issuing the rename can only fail on a missing
	// instance.
	ErrorInstanceRenamePersisted ErrorInfo = "instanceRenamePersisted"
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

// InstanceRemovePersisted reports a provider-instance removal that stood but
// left a copy of the removed OAuth record on disk, which the removal could not
// delete. The message names the file; the instance itself is gone.
func InstanceRemovePersisted(message string) WireError {
	return WireError{
		Code:    CodeInternalError,
		Message: message,
		Data:    ErrorData{EvenerErrorInfo: ErrorInstanceRemovePersisted},
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
