package renderplan

import "fmt"

type ErrorCode string

const (
	CodeDecode     ErrorCode = "decode_error"
	CodeVersion    ErrorCode = "unsupported_version"
	CodeValidation ErrorCode = "invalid_plan"
)

// Error is a stable, typed plan error. Path uses JSON-style field paths.
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
		return fmt.Sprintf("render plan %s%s: %s: %v", e.Code, where, e.Message, e.Cause)
	}
	return fmt.Sprintf("render plan %s%s: %s", e.Code, where, e.Message)
}

func (e *Error) Unwrap() error { return e.Cause }
