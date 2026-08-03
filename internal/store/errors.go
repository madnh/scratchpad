package store

import "github.com/madnh/scratchpad/internal/pad"

// The error vocabulary lives in internal/pad, where the rules that raise most of it
// live. These aliases keep one spelling for callers that talk to the storage layer, so
// a surface never has to know which package a given code came from.
const (
	CodeNotYourTurn        = pad.CodeNotYourTurn
	CodeNotTaskOwner       = pad.CodeNotTaskOwner
	CodeNoSuchTask         = pad.CodeNoSuchTask
	CodeTaskNeedsOwner     = pad.CodeTaskNeedsOwner
	CodeRulesUnread        = pad.CodeRulesUnread
	CodePadNotFound        = pad.CodePadNotFound
	CodeUnauthorized       = pad.CodeUnauthorized
	CodeContentTooLarge    = pad.CodeContentTooLarge
	CodeInvalidProjectName = pad.CodeInvalidProjectName
	CodeInvalidRef         = pad.CodeInvalidRef
	CodeInvalidInput       = pad.CodeInvalidInput
	CodeLimitExceeded      = pad.CodeLimitExceeded
)

// CodedError is an error with a stable machine-readable code.
type CodedError = pad.CodedError

// coded builds a CodedError with a formatted message.
func coded(code, format string, args ...any) error { return pad.Coded(code, format, args...) }

// HasCode reports whether err carries the given stable code.
func HasCode(err error, code string) bool { return pad.HasCode(err, code) }
