package main

import (
	"bytes"
	"errors"
	"testing"
)

type fuzzErrorWriter struct{}

func (fuzzErrorWriter) Write([]byte) (int, error) { return 0, errors.New("write failed") }

func FuzzRun(f *testing.F) {
	for _, subcommand := range []string{"", "help", "--help", "locate", "transcript", "apilog", "watches", "tree", "plugins", "unknown"} {
		f.Add(subcommand, false)
	}
	f.Add("unknown", true)
	f.Fuzz(func(t *testing.T, subcommand string, failWrites bool) {
		if len(subcommand) > 256 {
			return
		}
		var stdout, stderr bytes.Buffer
		if failWrites {
			_ = run([]string{subcommand}, fuzzErrorWriter{}, fuzzErrorWriter{})
			return
		}
		_ = run([]string{subcommand}, &stdout, &stderr)
	})
}
