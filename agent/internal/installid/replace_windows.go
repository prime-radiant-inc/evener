//go:build windows

package installid

import (
	"fmt"
	"os"
	"unsafe"

	"golang.org/x/sys/windows"
)

var replaceFileW = windows.NewLazySystemDLL("kernel32.dll").NewProc("ReplaceFileW")

func atomicReplaceInstallationID(tempPath, destinationPath string, destinationExists bool) error {
	if !destinationExists {
		// ReplaceFileW requires an existing destination. Creation is serialized by
		// the installation lock, and the temporary file is in the same directory.
		return os.Rename(tempPath, destinationPath)
	}
	destinationPath16, err := windows.UTF16PtrFromString(destinationPath)
	if err != nil {
		return fmt.Errorf("encode installation ID destination path: %w", err)
	}
	tempPath16, err := windows.UTF16PtrFromString(tempPath)
	if err != nil {
		return fmt.Errorf("encode installation ID temporary path: %w", err)
	}
	result, _, callErr := replaceFileW.Call(
		uintptr(unsafe.Pointer(destinationPath16)),
		uintptr(unsafe.Pointer(tempPath16)),
		0,
		0,
		0,
		0,
	)
	if result == 0 {
		return fmt.Errorf("atomically replace installation ID with ReplaceFileW: %w", callErr)
	}
	return nil
}
