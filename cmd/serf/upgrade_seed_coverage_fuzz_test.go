//go:build serffuzz

package main

import (
	"context"
	"errors"
	"io"
	"testing"

	"primeradiant.com/serf/internal/selfupdate"
)

func FuzzUpgradeErrorSeedCoverage(f *testing.F) {
	f.Add(false)
	f.Fuzz(func(t *testing.T, _ bool) {
		old := selfUpdateUpgrade
		selfUpdateUpgrade = func(context.Context, selfupdate.Options) (selfupdate.Result, error) {
			return selfupdate.Result{}, errors.New("upgrade failed")
		}
		t.Cleanup(func() { selfUpdateUpgrade = old })
		if err := runUpgrade(nil, io.Discard, io.Discard); err == nil {
			t.Fatal("expected injected upgrade failure")
		}
	})
}
