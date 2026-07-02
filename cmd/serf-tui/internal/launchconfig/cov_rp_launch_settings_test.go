package launchconfig

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"primeradiant.com/serf/appwire"
)

func TestParseOptionalBool(t *testing.T) {
	tru, fal := true, false
	cases := []struct {
		in      string
		want    *bool
		wantErr bool
	}{
		{"", nil, false},
		{"(default)", nil, false},
		{"true", &tru, false},
		{"YES", &tru, false},
		{"1", &tru, false},
		{"false", &fal, false},
		{"no", &fal, false},
		{"0", &fal, false},
		{"maybe", nil, true},
	}
	for _, c := range cases {
		got, err := parseOptionalBool(c.in)
		if (err != nil) != c.wantErr {
			t.Errorf("parseOptionalBool(%q) err = %v, wantErr %v", c.in, err, c.wantErr)
			continue
		}
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("parseOptionalBool(%q) = %v, want %v", c.in, deref(got), deref(c.want))
		}
	}
}

func deref(p *bool) any {
	if p == nil {
		return nil
	}
	return *p
}

func TestParseOptionalInt(t *testing.T) {
	if v, err := parseOptionalInt("(default)"); err != nil || v != nil {
		t.Fatalf("(default) = %v,%v want nil,nil", v, err)
	}
	if v, err := parseOptionalInt("  42 "); err != nil || v == nil || *v != 42 {
		t.Fatalf("42 = %v,%v", v, err)
	}
	if _, err := parseOptionalInt("notanint"); err == nil {
		t.Fatal("expected error for a non-integer")
	}
}

func TestParseEnvMap(t *testing.T) {
	if m, err := parseEnvMap(""); err != nil || m != nil {
		t.Fatalf("empty = %v,%v want nil,nil", m, err)
	}
	m, err := parseEnvMap("A=1, B = two ")
	if err != nil {
		t.Fatal(err)
	}
	if m["A"] != "1" || m["B"] != "two" {
		t.Fatalf("parseEnvMap = %v", m)
	}
	if _, err := parseEnvMap("NOEQUALS"); err == nil {
		t.Fatal("expected error for an entry without '='")
	}
}

func TestParseModelFallbacks(t *testing.T) {
	if got := parseModelFallbacks("(default)"); got != nil {
		t.Errorf("(default) = %v, want nil", got)
	}
	if got := parseModelFallbacks("[]"); got == nil || len(got) != 0 {
		t.Errorf("[] = %v, want empty non-nil", got)
	}
	if got := parseModelFallbacks("a, b ,c"); !reflect.DeepEqual(got, []string{"a", "b", "c"}) {
		t.Errorf("list = %v", got)
	}
}

func TestApplyEdit_ScalarAndNumericFields(t *testing.T) {
	base := appwire.LaunchConfigLayer{}

	got, err := applyEdit(base, "model", "  openai/gpt-5 ")
	if err != nil || got.Model != "openai/gpt-5" {
		t.Fatalf("model = %q, %v", got.Model, err)
	}

	got, err = applyEdit(base, "max_rounds", "7")
	if err != nil || got.MaxRounds == nil || *got.MaxRounds != 7 {
		t.Fatalf("max_rounds = %v, %v", got.MaxRounds, err)
	}
	if _, err := applyEdit(base, "max_rounds", "NaN"); err == nil {
		t.Fatal("expected error for non-int max_rounds")
	}

	got, err = applyEdit(base, "verbose", "true")
	if err != nil || got.Verbose == nil || !*got.Verbose {
		t.Fatalf("verbose = %v, %v", got.Verbose, err)
	}
	if _, err := applyEdit(base, "verbose", "perhaps"); err == nil {
		t.Fatal("expected error for non-bool verbose")
	}
}

func TestApplyEdit_SystemPromptDefaults(t *testing.T) {
	base := appwire.LaunchConfigLayer{SystemPromptText: "old", SystemPromptFile: "/x"}

	got, err := applyEdit(base, "system_prompt_text", "(default)")
	if err != nil || got.SystemPromptText != "" {
		t.Fatalf("system_prompt_text reset = %q, %v", got.SystemPromptText, err)
	}

	got, err = applyEdit(base, "system_prompt_file", "(default)")
	if err != nil || got.SystemPromptFile != "" {
		t.Fatalf("system_prompt_file reset = %q, %v", got.SystemPromptFile, err)
	}

	got, err = applyEdit(base, "system_prompt_append_text", "keepme")
	if err != nil || got.SystemPromptAppendText != "keepme" {
		t.Fatalf("append_text = %q, %v", got.SystemPromptAppendText, err)
	}
}

func TestApplyEdit_UnsupportedField(t *testing.T) {
	if _, err := applyEdit(appwire.LaunchConfigLayer{}, "not_a_field", "x"); err == nil {
		t.Fatal("expected error for an unsupported field")
	}
}

func TestApplyEdit_EnvField(t *testing.T) {
	got, err := applyEdit(appwire.LaunchConfigLayer{}, "env", "K=v")
	if err != nil || got.Env["K"] != "v" {
		t.Fatalf("env = %v, %v", got.Env, err)
	}
	if _, err := applyEdit(appwire.LaunchConfigLayer{}, "env", "bad"); err == nil {
		t.Fatal("expected error for malformed env")
	}
}

func TestMcpEditValue(t *testing.T) {
	if got := mcpEditValue(nil); got != "" {
		t.Fatalf("mcpEditValue(nil) = %q, want empty", got)
	}
	got := mcpEditValue([]appwire.MCPServerSpec{{Name: "fs", Command: "/bin/echo", Args: []string{"a", "b"}}})
	// Round-trips through parseMCPs back to the same specs.
	back, err := parseMCPs(got)
	if err != nil {
		t.Fatalf("parseMCPs(%q): %v", got, err)
	}
	if len(back) != 1 || back[0].Name != "fs" || back[0].Command != "/bin/echo" ||
		!reflect.DeepEqual(back[0].Args, []string{"a", "b"}) {
		t.Fatalf("round-trip = %+v", back)
	}
}

func TestParseMCPs_Forms(t *testing.T) {
	if got, err := parseMCPs("   "); err != nil || got != nil {
		t.Fatalf("empty = %v,%v", got, err)
	}

	// Single JSON object form.
	single, err := parseMCPs(`{"name":"fs","command":"/bin/echo","args":["x"]}`)
	if err != nil || len(single) != 1 || single[0].Name != "fs" {
		t.Fatalf("single object = %+v, %v", single, err)
	}

	// Malformed JSON array.
	if _, err := parseMCPs(`[{bad`); err == nil {
		t.Fatal("expected error for malformed JSON array")
	}

	// Row form: "name:command args".
	rows, err := parseMCPs("fs:/bin/echo hello world")
	if err != nil || len(rows) != 1 || rows[0].Command != "/bin/echo" ||
		!reflect.DeepEqual(rows[0].Args, []string{"hello", "world"}) {
		t.Fatalf("rows = %+v, %v", rows, err)
	}

	// Row missing the command.
	if _, err := parseMCPs("fs:"); err == nil {
		t.Fatal("expected error for a row missing the command")
	}
	// Row missing the name separator.
	if _, err := parseMCPs("noseparator"); err == nil {
		t.Fatal("expected error for a row with no ':'")
	}
}

func TestValidateMCPs_Errors(t *testing.T) {
	if err := validateMCPs([]appwire.MCPServerSpec{{Name: "", Command: "/bin/echo"}}); err == nil {
		t.Fatal("expected missing-name error")
	}
	if err := validateMCPs([]appwire.MCPServerSpec{{Name: "x", Command: ""}}); err == nil {
		t.Fatal("expected missing-command error")
	}
	if err := validateMCPs([]appwire.MCPServerSpec{{Name: "x", Command: "/bin/echo"}}); err != nil {
		t.Fatalf("valid MCP rejected: %v", err)
	}
}

func TestValidateLocalLaunchPath(t *testing.T) {
	if err := validateLocalLaunchPath("  ", "file"); err == nil {
		t.Fatal("expected required-path error for whitespace")
	}
	if err := validateLocalLaunchPath("relative/path", "file"); err == nil {
		t.Fatal("expected absolute-path error for relative path")
	}

	// A bare command name is resolved via $PATH.
	if err := validateLocalLaunchPath("definitely-not-on-path-xyz", "command"); err == nil {
		t.Fatal("expected LookPath failure for a missing command")
	}

	dir := t.TempDir()
	if err := validateLocalLaunchPath(dir, "dir"); err != nil {
		t.Fatalf("existing dir rejected: %v", err)
	}
	if err := validateLocalLaunchPath(dir, "file"); err == nil {
		t.Fatal("expected 'is a directory' error when a dir is used as a file")
	}

	f := filepath.Join(dir, "f.txt")
	if err := os.WriteFile(f, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := validateLocalLaunchPath(f, "file"); err != nil {
		t.Fatalf("existing file rejected: %v", err)
	}
	if err := validateLocalLaunchPath(f, "command"); err == nil {
		t.Fatal("expected 'not executable' error for a non-exec file used as command")
	}
	if err := validateLocalLaunchPath(filepath.Join(dir, "missing"), "file"); err == nil {
		t.Fatal("expected stat error for a missing path")
	}
}

func TestSplitTrim(t *testing.T) {
	if got := splitTrim(" a , , b ,", ","); !reflect.DeepEqual(got, []string{"a", "b"}) {
		t.Fatalf("splitTrim = %v", got)
	}
	if got := splitTrim("", ","); len(got) != 0 {
		t.Fatalf("splitTrim empty = %v", got)
	}
}
