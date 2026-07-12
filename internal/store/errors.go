package store

import (
	"errors"
	"fmt"
)

// Error codes shared by the CLI and the MCP surface. An agent-facing error message is
// "<code>: <detail>" so a caller can branch on the stable code while a human still
// reads one plain sentence.
const (
	CodeNotYourTurn        = "not_your_turn"
	CodePadNotFound        = "pad_not_found"
	CodeUnauthorized       = "unauthorized"
	CodeContentTooLarge    = "content_too_large"
	CodeInvalidProjectName = "invalid_project_name"
	CodeInvalidRef         = "invalid_ref"
	CodeInvalidInput       = "invalid_input"
	CodeLimitExceeded      = "limit_exceeded"
)

// CodedError is an error with a stable machine-readable code.
type CodedError struct {
	Code string
	Msg  string
}

func (e *CodedError) Error() string { return e.Code + ": " + e.Msg }

// coded builds a CodedError with a formatted message.
func coded(code, format string, args ...any) error {
	return &CodedError{Code: code, Msg: fmt.Sprintf(format, args...)}
}

// HasCode reports whether err carries the given stable code.
func HasCode(err error, code string) bool {
	var ce *CodedError
	return errors.As(err, &ce) && ce.Code == code
}
