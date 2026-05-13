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
	ErrorInvalidParams       ErrorInfo = "invalidParams"
	ErrorMethodNotFound      ErrorInfo = "methodNotFound"
	ErrorProviderUnavailable ErrorInfo = "providerUnavailable"
	ErrorSessionUnavailable  ErrorInfo = "sessionUnavailable"
	ErrorConflict            ErrorInfo = "conflict"
	ErrorActionUnavailable   ErrorInfo = "actionUnavailable"
	ErrorHubLaunch           ErrorInfo = "hubLaunch"
	ErrorInternal            ErrorInfo = "internal"
)

type ErrorData struct {
	SerfErrorInfo ErrorInfo `json:"serfErrorInfo"`
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
		Data:    ErrorData{SerfErrorInfo: ErrorInvalidParams},
	}
}

func InvalidRequest(message string) WireError {
	return WireError{
		Code:    CodeInvalidRequest,
		Message: message,
		Data:    ErrorData{SerfErrorInfo: ErrorInvalidParams},
	}
}

func MethodNotFound(method string) WireError {
	return WireError{
		Code:    CodeMethodNotFound,
		Message: fmt.Sprintf("method not found: %s", method),
		Data:    ErrorData{SerfErrorInfo: ErrorMethodNotFound},
	}
}

func InternalError(message string) WireError {
	return WireError{
		Code:    CodeInternalError,
		Message: message,
		Data:    ErrorData{SerfErrorInfo: ErrorInternal},
	}
}

func Conflict(message string) WireError {
	return WireError{
		Code:    CodeConflict,
		Message: message,
		Data:    ErrorData{SerfErrorInfo: ErrorConflict},
	}
}

func Unavailable(message string) WireError {
	return WireError{
		Code:    CodeUnavailable,
		Message: message,
		Data:    ErrorData{SerfErrorInfo: ErrorActionUnavailable},
	}
}

func SessionUnavailable(message string) WireError {
	return WireError{
		Code:    CodeUnavailable,
		Message: message,
		Data:    ErrorData{SerfErrorInfo: ErrorSessionUnavailable},
	}
}

func HubLaunchError(message string) WireError {
	return WireError{
		Code:    CodeUnavailable,
		Message: message,
		Data:    ErrorData{SerfErrorInfo: ErrorHubLaunch},
	}
}
