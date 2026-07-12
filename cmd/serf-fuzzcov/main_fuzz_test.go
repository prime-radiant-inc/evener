package main

import (
	"os"
	"path/filepath"
	"testing"
)

func FuzzCoverageParsers(f *testing.F) {
	f.Add("primeradiant.com/serf/agent 10 20\n", "agent", "90")
	f.Add("mode: set\nfile.go:1.1,1.2 1 1\n", "", "100%")
	f.Fuzz(func(t *testing.T, profile, focus, floor string) {
		if len(profile) > 1<<20 || len(focus)+len(floor) > 4096 {
			return
		}
		_ = parseFocus(focus)
		path := filepath.Join(t.TempDir(), "cover.out")
		if err := os.WriteFile(path, []byte(profile), 0o600); err != nil {
			t.Fatal(err)
		}
		_, _ = parseProfile(path)
		_, _ = parseGlobalFloor(floor)
	})
}
