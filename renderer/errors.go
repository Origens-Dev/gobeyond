// Package renderer evaluates GoBeyond rendering plans and emits deterministic
// HTML without executing JavaScript.
package renderer

import "fmt"

type ErrorCode string

const (
	CodeEvaluation ErrorCode = "evaluation_error"
	CodeRender     ErrorCode = "render_error"
	CodeUnsafeURL  ErrorCode = "unsafe_url"
	CodeUnsafeHTML ErrorCode = "unsafe_html"
)

// Error identifies a stable rendering failure and its rendering-plan path.
type Error struct {
	Code    ErrorCode
	Path    string
	Message string
	Cause   error
}

func (e *Error) Error() string {
	where := ""
	if e.Path != "" {
		where = " at " + e.Path
	}
	if e.Cause != nil {
		return fmt.Sprintf("renderer %s%s: %s: %v", e.Code, where, e.Message, e.Cause)
	}
	return fmt.Sprintf("renderer %s%s: %s", e.Code, where, e.Message)
}

func (e *Error) Unwrap() error { return e.Cause }

func fail(code ErrorCode, path, message string) error {
	return &Error{Code: code, Path: path, Message: message}
}
