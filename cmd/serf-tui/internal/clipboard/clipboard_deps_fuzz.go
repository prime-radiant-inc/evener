//go:build serffuzz

package clipboard

import (
	"errors"
	"os"
)

var clipboardGOOS = "linux"
var clipboardLookPath = func(string) (string, error) { return "", errors.New("clipboard command disabled in fuzz replay") }
var clipboardCreateTemp = os.CreateTemp
var clipboardReadFile = os.ReadFile
var clipboardRemove = os.Remove
var clipboardStat = os.Stat
var clipboardWrite = func(f *os.File, data []byte) (int, error) { return f.Write(data) }
var clipboardClose = func(f *os.File) error { return f.Close() }
var clipboardOutput = func(string, ...string) ([]byte, error) {
	return nil, errors.New("clipboard command disabled in fuzz replay")
}
var clipboardPowerShellOutput = clipboardOutput
