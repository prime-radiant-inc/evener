package main

import (
	"flag"
	"io"
	"testing"
)

// serveFlagSet returns the flag set `evener serve` registers, captured before
// it parses. The negative idle timeout makes serve refuse right after parsing,
// before it touches anything else.
func serveFlagSet(t *testing.T) *flag.FlagSet {
	t.Helper()
	deps := defaultServeDeps()
	var captured *flag.FlagSet
	deps.newFlagSet = func(name string, _ flag.ErrorHandling) *flag.FlagSet {
		captured = flag.NewFlagSet(name, flag.ContinueOnError)
		captured.SetOutput(io.Discard)
		return captured
	}
	if err := runServeWithDeps([]string{"--daemon-idle-timeout=-1s"}, deps); err == nil {
		t.Fatal("serve accepted a negative idle timeout")
	}
	return captured
}

// `evener` and `evener serve` take the same launch flags: a flag both accept
// has one default and one description.
func TestRunAndServeShareOneDefinitionPerLaunchFlag(t *testing.T) {
	runSet, _ := newRunFlagSet(io.Discard)
	serveSet := serveFlagSet(t)
	shared := 0
	launchSet := flag.NewFlagSet("launch", flag.ContinueOnError)
	launchSet.SetOutput(io.Discard)
	registerLaunchFlags(launchSet)
	launchSet.VisitAll(func(f *flag.Flag) {
		if runSet.Lookup(f.Name) == nil || serveSet.Lookup(f.Name) == nil {
			t.Errorf("launch flag --%s is missing from run or serve", f.Name)
		}
	})
	serveSet.VisitAll(func(serveFlag *flag.Flag) {
		runFlag := runSet.Lookup(serveFlag.Name)
		if runFlag == nil {
			return
		}
		shared++
		if runFlag.DefValue != serveFlag.DefValue {
			t.Errorf("--%s default differs: run %q, serve %q", serveFlag.Name, runFlag.DefValue, serveFlag.DefValue)
		}
		if runFlag.Usage != serveFlag.Usage {
			t.Errorf("--%s usage differs between run and serve", serveFlag.Name)
		}
	})
	if shared == 0 {
		t.Fatal("run and serve share no flags")
	}
}
