//go:build !linux && !darwin

package hostlock

import (
	"errors"
	"os"
)

func inspectFilesystem(*os.File) (string, bool, error) {
	return "", false, errors.New("hub storage inspection is unsupported on this platform")
}
