package main

import (
	"fmt"
	"testing"
)

func TestParseSlashCommand(t *testing.T) {
	tests := []struct {
		input   string
		wantCmd string
		wantArg string
	}{
		{"/compact", "compact", ""},
		{" /compact ", "compact", ""},
		{"/compact extra args", "compact", "extra args"},
		{"/quit", "quit", ""},
		{"/help", "help", ""},
		{"/model gpt-4o", "model", "gpt-4o"},
		{"/clear", "clear", ""},
		{"/status", "status", ""},
		{"hello /compact", "", ""},
		{"", "", ""},
		{"no slash", "", ""},
	}

	for _, tt := range tests {
		t.Run(fmt.Sprintf("%q", tt.input), func(t *testing.T) {
			cmd, args := parseSlashCommand(tt.input)
			if cmd != tt.wantCmd {
				t.Errorf("parseSlashCommand(%q) cmd = %q, want %q", tt.input, cmd, tt.wantCmd)
			}
			if args != tt.wantArg {
				t.Errorf("parseSlashCommand(%q) args = %q, want %q", tt.input, args, tt.wantArg)
			}
		})
	}
}
