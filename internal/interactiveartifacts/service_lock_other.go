//go:build !linux && !darwin

package interactiveartifacts

import (
	"errors"
	"os"
)

func acquireServiceLock(string) (*os.File, error) {
	return nil, errors.New("artifact ownership is unsupported on this platform")
}
