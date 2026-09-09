package protocol

import "fmt"

// Error codes for control RPC responses.
const (
	CodeInvalidRequest   = "invalid-request"
	CodeMethodNotFound   = "method-not-found"
	CodeInvalidParams    = "invalid-params"
	CodeNotFound         = "not-found"
	CodePermissionDenied = "permission-denied"
	CodeFailed           = "failed"
	CodeProtocolMismatch = "protocol-mismatch"
	CodeBusy             = "busy"
)

// Error is a protocol-level failure. It is the RPC error object, not CLI text.
type Error struct {
	OperationID string `json:"operationId,omitempty"`
	Code        string `json:"code"`
	Message     string `json:"message"`
}

func (e *Error) Error() string {
	if e == nil {
		return ""
	}
	if e.Message == "" {
		return e.Code
	}
	if e.Code == "" {
		return e.Message
	}
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

// ErrPermissionDenied is returned when the peer is not allowed on this pipe.
func ErrPermissionDenied() *Error {
	return &Error{Code: CodePermissionDenied, Message: "access denied"}
}

// ErrMethodNotFound is returned for an unknown method.
func ErrMethodNotFound(method string) *Error {
	return &Error{Code: CodeMethodNotFound, Message: fmt.Sprintf("unknown method %q", method)}
}

// ErrProtocolMismatch is returned when protocol name or version does not match.
func ErrProtocolMismatch(gotName string, gotVersion int) *Error {
	return &Error{
		Code:    CodeProtocolMismatch,
		Message: fmt.Sprintf("unsupported protocol %q version %d (want %q version %d)", gotName, gotVersion, Name, Version),
	}
}

// ErrInvalidRequest is returned for a malformed request.
func ErrInvalidRequest(msg string) *Error {
	return &Error{Code: CodeInvalidRequest, Message: msg}
}

// ErrInvalidParams is returned when params cannot be used.
func ErrInvalidParams(msg string) *Error {
	return &Error{Code: CodeInvalidParams, Message: msg}
}

// ErrNotFound is returned when a unit is not loaded.
func ErrNotFound(unit string) *Error {
	return &Error{Code: CodeNotFound, Message: fmt.Sprintf("unit %q is not loaded", unit)}
}

// ErrFailed is a generic handler failure.
func ErrFailed(msg string) *Error {
	return &Error{Code: CodeFailed, Message: msg}
}

// ErrBusy rejects excess work before entering a handler or creating side effects.
func ErrBusy() *Error {
	return &Error{Code: CodeBusy, Message: "control request capacity exhausted; retry later"}
}

func asError(err error) *Error {
	if err == nil {
		return nil
	}
	if pe, ok := err.(*Error); ok {
		return pe
	}
	return ErrFailed(err.Error())
}
