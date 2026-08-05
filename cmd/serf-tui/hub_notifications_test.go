package main

import "testing"

func TestRunStillRunning_Exhausted(t *testing.T) {
	if runStillRunning("exhausted") {
		t.Fatal("exhausted run reported as still running")
	}
}
