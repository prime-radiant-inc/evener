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
			name: "longest operators",
			raw:  "a || b |& c || d",
			want: []shellCommandLine{
				{text: "a || ", indent: 0},
				{text: "b |& ", indent: 2},
				{text: "c || ", indent: 2},
				{text: "d", indent: 2},
			},
		},
		{
			name: "protected operators",
			raw:  "printf '%s' \"a;b && c\" `echo x;y` foo\\;bar && done",
			want: []shellCommandLine{
				{text: "printf '%s' \"a;b && c\" `echo x;y` foo\\;bar && ", indent: 0},
				{text: "done", indent: 2},
			},
		},
		{
			name: "comments stop operator scanning",
			raw:  "echo hi # && hidden; text\nprintf done",
			want: []shellCommandLine{
				{text: "echo hi # && hidden; text", indent: 0},
				{text: "printf done", indent: 0},
			},
		},
		{
			name: "nested substitutions stay opaque",
			raw:  "echo $(printf 'a;b' && printf c) && echo done",
			want: []shellCommandLine{
				{text: "echo $(printf 'a;b' && printf c) && ", indent: 0},
				{text: "echo done", indent: 2},
			},
		},
		{
			name: "source continuation",
			raw:  "printf \"left\\\\\nright\" && echo done",
			want: []shellCommandLine{
				{text: "printf \"left\\\\", indent: 0},
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
			name: "malformed and trailing input stays intact",
			raw:  "echo \"unterminated &&",
			want: []shellCommandLine{{text: "echo \"unterminated &&", indent: 0}},
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
		{
			name: "single quote backslash does not protect quote",
			raw:  "printf '%s' 'a\\' ; echo done",
			want: []shellCommandLine{
				{text: "printf '%s' 'a\\' ; ", indent: 0},
				{text: "echo done", indent: 2},
			},
		},
		{
			name: "case terminators and redirections",
			raw:  "case x in x) echo one ;; y) echo two ;& z) echo three ;;& esac; echo value >| out",
			want: []shellCommandLine{
				{text: "case x in x) echo one ;; ", indent: 0},
				{text: "y) echo two ;& ", indent: 2},
				{text: "z) echo three ;;& ", indent: 2},
				{text: "esac; ", indent: 2},
				{text: "echo value >| out", indent: 2},
			},
		},
		{
			name: "escaped space before hash is not a comment",
			raw:  "echo foo\\ # && bar",
			want: []shellCommandLine{
				{text: "echo foo\\ # && ", indent: 0},
				{text: "bar", indent: 2},
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
