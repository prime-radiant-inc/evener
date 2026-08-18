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
	ErrorInvalidParams          ErrorInfo = "invalidParams"
	ErrorMethodNotFound         ErrorInfo = "methodNotFound"
	ErrorProviderUnavailable    ErrorInfo = "providerUnavailable"
	ErrorSessionUnavailable     ErrorInfo = "sessionUnavailable"
	ErrorConflict               ErrorInfo = "conflict"
	ErrorActionUnavailable      ErrorInfo = "actionUnavailable"
	ErrorHubLaunch              ErrorInfo = "hubLaunch"
	ErrorQueuedDrainPartial     ErrorInfo = "queuedDrainPartial"
	ErrorMutationOutcomeUnknown ErrorInfo = "mutationOutcomeUnknown"
	ErrorInternal               ErrorInfo = "internal"
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
	EvenerErrorInfo    ErrorInfo        `json:"evenerErrorInfo"`
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
