package pad

import (
	"errors"
	"fmt"
)

// Error codes shared by the CLI and the MCP surface. An agent-facing error message is
// "<code>: <detail>" so a caller can branch on the stable code while a human still
// reads one plain sentence.
//
// The vocabulary lives here, in the lowest package, because the rules that raise most
// of it are here: the turn rule and task ownership are domain rules, not storage
// concerns. internal/store aliases these so its own callers keep one spelling.
const (
	CodeNotYourTurn        = "not_your_turn"
	CodeNotTaskOwner       = "not_task_owner"
	CodeNoSuchTask         = "no_such_task"
	CodeTaskNeedsOwner     = "task_needs_owner"
	CodeRulesUnread        = "rules_unread"
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

// Coded builds a CodedError with a formatted message.
func Coded(code, format string, args ...any) error {
	return &CodedError{Code: code, Msg: fmt.Sprintf(format, args...)}
}

// HasCode reports whether err carries the given stable code.
func HasCode(err error, code string) bool {
	var ce *CodedError
	return errors.As(err, &ce) && ce.Code == code
}
