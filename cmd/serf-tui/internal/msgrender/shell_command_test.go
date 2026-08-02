package msgrender

import (
	"reflect"
	"strings"
	"testing"
)

func TestFormatShellCommandFixtures(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want []shellCommandLine
	}{
		{
			name: "chains and pipelines",
			raw:  "cd /tmp && echo ok; printf '%s\\n' \"$HOME\" | tee out",
			want: []shellCommandLine{
				{text: "cd /tmp && ", indent: 0},
				{text: "echo ok; ", indent: 2},
				{text: "printf '%s\\n' \"$HOME\" | ", indent: 2},
				{text: "tee out", indent: 2},
			},
		},
		{
			name: "protected and nested operators",
			raw:  "echo \"a;b\" $(printf 'c && d') foo\\;bar && done",
			want: []shellCommandLine{
				{text: "echo \"a;b\" $(printf 'c && d') foo\\;bar && ", indent: 0},
				{text: "done", indent: 2},
			},
		},
		{
			name: "source continuation",
			raw:  "printf \"left\\\nright\" && echo done",
			want: []shellCommandLine{
				{text: "printf \"left\\", indent: 0},
				{text: "right\" && ", indent: 0},
				{text: "echo done", indent: 2},
			},
		},
		{
			name: "comments and malformed input",
			raw:  "echo hi # && hidden; text\nprintf \"unterminated &&",
			want: []shellCommandLine{
				{text: "echo hi # && hidden; text", indent: 0},
				{text: "printf \"unterminated &&", indent: 0},
			},
		},
		{
			name: "empty input",
			raw:  "",
			want: []shellCommandLine{{text: "", indent: 0}},
		},
		{
			name: "trailing operator",
			raw:  "echo done &&",
			want: []shellCommandLine{{text: "echo done &&", indent: 0}},
		},
		{
			name: "operator directly before source newline",
			raw:  "one &&\ntwo",
			want: []shellCommandLine{
				{text: "one &&", indent: 0},
				{text: "two", indent: 0},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := formatShellCommand(tt.raw)
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("formatShellCommand(%q) = %#v, want %#v", tt.raw, got, tt.want)
			}
			var source strings.Builder
			for _, line := range got {
				source.WriteString(line.text)
			}
			if gotSource := source.String(); gotSource != strings.ReplaceAll(tt.raw, "\n", "") {
				t.Fatalf("formatShellCommand(%q) lost source: %q", tt.raw, gotSource)
			}
		})
	}
}
