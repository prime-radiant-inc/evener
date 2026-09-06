package main

import (
	"io"
	"testing"
)

func TestProviderIdleTimeoutFlag(t *testing.T) {
	fs, _ := newRunFlagSet(io.Discard)
	if err := fs.Parse([]string{"--provider-idle-timeout", "15m"}); err != nil {
		t.Fatal(err)
	}
	if got := fs.Lookup("provider-idle-timeout").Value.String(); got != "15m" {
		t.Fatalf("duration=%q", got)
	}
}
