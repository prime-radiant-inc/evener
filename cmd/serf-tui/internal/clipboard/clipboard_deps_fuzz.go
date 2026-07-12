//go:build serffuzz

package clipboard

import "os"

var clipboardGOOS = "linux"
var clipboardLookPath func(string) (string, error)
var clipboardCreateTemp func(string, string) (*os.File, error)
var clipboardReadFile func(string) ([]byte, error)
var clipboardRemove func(string) error
var clipboardStat func(string) (os.FileInfo, error)
var clipboardWrite func(*os.File, []byte) (int, error)
var clipboardClose func(*os.File) error
var clipboardOutput func(string, ...string) ([]byte, error)
var clipboardPowerShellOutput func(string, ...string) ([]byte, error)
