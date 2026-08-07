//go:build !linux && !darwin

package transcript

import (
	"fmt"
	"os"
)

func openTranscriptAppendFile(path string) (*os.File, error) {
	file, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return nil, err
	}
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, err
	}
	if !info.Mode().IsRegular() {
		_ = file.Close()
		return nil, fmt.Errorf("transcript target %q is not a regular file", path)
	}
	return file, nil
}
