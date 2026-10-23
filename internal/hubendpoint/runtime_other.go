//go:build !linux && !darwin

package hubendpoint

import "os"

func ownedByCurrentUser(os.FileInfo) bool { return false }

func openRecord(path string) (*os.File, error) { return os.Open(path) }
