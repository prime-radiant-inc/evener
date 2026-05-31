package llm

import (
	"context"
	"errors"
)

// WrapContextError converts context cancellation/deadline errors into the SDK
// error hierarchy (AbortError or a timeout of [KindTimeout]). Other errors are
// returned unchanged.
func WrapContextError(provider string, err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) {
		return NewAbortError(err.Error(), err)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return NewRequestTimeoutError(provider, err.Error(), err)
	}
	return err
}
