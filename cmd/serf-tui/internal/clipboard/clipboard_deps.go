//go:build !serffuzz

package clipboard

import (
	"context"
	"os"
	"os/exec"
	"runtime"
)

var clipboardGOOS = runtime.GOOS
var clipboardLookPath = exec.LookPath
var clipboardCreateTemp = os.CreateTemp
var clipboardReadFile = os.ReadFile
var clipboardRemove = os.Remove
var clipboardStat = os.Stat
var clipboardWrite = func(f *os.File, data []byte) (int, error) { return f.Write(data) }
var clipboardClose = func(f *os.File) error { return f.Close() }

var clipboardOutput = func(name string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), clipboardProbeTimeout)
	defer cancel()
	return exec.CommandContext(ctx, name, args...).Output()
}

var clipboardPowerShellOutput = clipboardOutput
