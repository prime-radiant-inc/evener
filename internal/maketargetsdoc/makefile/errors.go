// errors.go carries the parse-side rejection reasons and the rejection type
// they are wrapped in. They live with ParseFamily because a caller of the
// parser has to be able to tell one rejection from another with errors.Is.
package makefile

import (
	"errors"
	"fmt"
)

// Reasons ParseFamily rejects a family file.
var (
	ErrBlockDetached            = errors.New("## block separated from its rule")
	ErrBlockOnDirective         = errors.New("## block above a directive")
	ErrBlockOnTargetVariable    = errors.New("## block above a target-specific variable line")
	ErrTargetUnannotated        = errors.New("target has no ## block")
	ErrBlockWithoutSummary      = errors.New("## block has no summary line")
	ErrBlockUnattached          = errors.New("## block never attached to a rule")
	ErrMalformedAnnotationLine  = errors.New("malformed ## line")
	ErrContinuationWithoutBlock = errors.New("continuation line outside a ## block")
)

// rejectionError is a rejection with its reason: Error is the full message a
// person reads, and Unwrap is the reason sentinel.
type rejectionError struct {
	reason error
	msg    string
}

func (e *rejectionError) Error() string { return e.msg }

func (e *rejectionError) Unwrap() error { return e.reason }

// reject formats a rejection message with reason.
func reject(reason error, format string, args ...any) error {
	return &rejectionError{reason: reason, msg: fmt.Sprintf(format, args...)}
}
