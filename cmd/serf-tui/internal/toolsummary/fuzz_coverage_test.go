package toolsummary

import (
	"errors"
	"io"
	"testing"

	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/lexers"
	"github.com/alecthomas/chroma/v2/styles"
)

func TestFuzzCoverageUnion(t *testing.T) {
	cases := []struct{ tool, args string }{
		{"shell", `{"command":"x","description":"legacy"}`},
		{"shell", `{"command":"line one\nline two"}`},
		{"read_file", `{"file_path":""}`},
		{"read_file", `{"file_path":"a/b","offset":1}`},
		{"read_file", `{"file_path":"a/b","limit":2}`},
		{"write_file", `{"file_path":"a/b","content":""}`},
		{"glob", `{"pattern":"*"}`},
		{"grep", `{"pattern":"x"}`},
		{"web_fetch", `{"url":"abcdefghijklmnopqrstuvwxyzabcdefghijklmnopqrstuvwxyzabcdefghijklmnopqrstuvwxyz"}`},
		{"delegate", `{"task":"abcdefghijklmnopqrstuvwxyzabcdefghijklmnopqrstuvwxyzabcdefghijklmnopqrstuvwxyz"}`},
		{"job_send_message", `{"target":"worker"}`},
		{"delegate_send", `{"to":"worker"}`},
		{"job_read_output", `{"job_id":"one"}`},
		{"job_stop", `{"job_id":"two"}`},
		{"use_skill", `{"skill_name":"test"}`},
		{"communicate", `{}`},
		{"unknown", `{"long":"abcdefghijklmnopqrstuvwxyzabcdefghijklmno","nested":{},"items":[]}`},
		{"task_list", `{"action":"view"}`},
		{"task_list", `{"action":"append","tasks":[]}`},
		{"task_list", `{"action":"append","tasks":[null,{"description":"d","prompt":"abcdefghijklmnopqrstuvwxyzabcdefghijklmnopqrstuvwxyzabcdefghijklmnopqrstuvwxyz"}]}`},
		{"task_list", `{"action":"update","updates":[]}`},
		{"task_list", `{"action":"update","updates":[null,{"id":1,"status":"unknown"}]}`},
	}
	for _, tc := range cases {
		SummarizeTool(tc.tool, tc.args)
	}
}

type errorLexer struct{ chroma.Lexer }

func (errorLexer) Tokenise(*chroma.TokeniseOptions, string) (chroma.Iterator, error) {
	return nil, errors.New("tokenise")
}

type errorFormatter struct{}

func (errorFormatter) Format(io.Writer, *chroma.Style, chroma.Iterator) error {
	return errors.New("format")
}

func TestHighlightDiffFallbacks(t *testing.T) {
	diff := "plain"
	lexer := lexers.Get("diff")
	style := styles.Get("monokai")
	if got := highlightDiffWith(diff, nil, style, errorFormatter{}); got != diff {
		t.Fatalf("nil lexer: got %q", got)
	}
	if got := highlightDiffWith(diff, lexer, style, nil); got != diff {
		t.Fatalf("nil formatter: got %q", got)
	}
	if got := highlightDiffWith(diff, errorLexer{Lexer: lexer}, style, errorFormatter{}); got != diff {
		t.Fatalf("tokenise error: got %q", got)
	}
	if got := highlightDiffWith(diff, lexer, style, errorFormatter{}); got != diff {
		t.Fatalf("format error: got %q", got)
	}
	if got := highlightDiffWith(diff, lexer, nil, errorFormatter{}); got != diff {
		t.Fatalf("nil style: got %q", got)
	}
}
