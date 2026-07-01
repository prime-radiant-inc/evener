package agent

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"testing"

	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/internal/tool"
	"primeradiant.com/serf/llm"
)

// w3sub_readFileEnv is a minimal execenv whose ReadFile returns a caller-supplied
// payload, so the read_file handler's image/document side-channel branches can be
// driven without a real filesystem-backed environment.
type w3sub_readFileEnv struct {
	output string
}

func (e *w3sub_readFileEnv) Initialize() error        { return nil }
func (e *w3sub_readFileEnv) Cleanup()                 {}
func (e *w3sub_readFileEnv) WorkingDirectory() string { return "/work" }
func (e *w3sub_readFileEnv) Platform() string         { return "linux" }
func (e *w3sub_readFileEnv) OSVersion() string        { return "test" }
func (e *w3sub_readFileEnv) ReadFile(string, *int, *int) (string, error) {
	return e.output, nil
}
func (e *w3sub_readFileEnv) WriteFile(string, string) (string, error) {
	return "", errors.New("not implemented")
}
func (e *w3sub_readFileEnv) EditFile(string, string, string, bool) (string, error) {
	return "", errors.New("not implemented")
}
func (e *w3sub_readFileEnv) FileExists(string) bool { return false }
func (e *w3sub_readFileEnv) Glob(string, string) ([]string, error) {
	return nil, errors.New("not implemented")
}
func (e *w3sub_readFileEnv) Grep(string, string, string, bool, int, string) (string, error) {
	return "", errors.New("not implemented")
}
func (e *w3sub_readFileEnv) ListDirectory(string, int) ([]execenv.DirEntry, error) {
	return nil, errors.New("not implemented")
}
func (e *w3sub_readFileEnv) ExecCommand(context.Context, string, int, string, map[string]string) (execenv.ExecResult, error) {
	return execenv.ExecResult{}, errors.New("not implemented")
}

func w3sub_readFileResult(t *testing.T, env execenv.ExecutionEnvironment, path string) tool.ExecResult {
	t.Helper()
	deps := &toolDeps{readGuard: readGuard{trackRead: func(string) {}}}
	reg := tool.NewRegistry()
	if err := registerFileTools(reg, deps); err != nil {
		t.Fatalf("registerFileTools: %v", err)
	}
	args, _ := json.Marshal(map[string]any{"file_path": path, "purpose": "inspect"})
	return reg.ExecuteCall(context.Background(), env, llm.ToolCallData{ID: "c1", Name: "read_file", Arguments: args})
}

// read_file routes an [image: ...] ReadFile payload through the vision
// side-channel (session_tools_file.go image arm).
func TestW3Sub_RegisterFileTools_ImageResult(t *testing.T) {
	payload := "[image: pic.png]\n" + base64.StdEncoding.EncodeToString([]byte("fakepngbytes"))
	res := w3sub_readFileResult(t, &w3sub_readFileEnv{output: payload}, "pic.png")
	if res.IsError {
		t.Fatalf("unexpected error: %q", res.Output)
	}
	if len(res.ImageData) == 0 || res.ImageMediaType != "image/png" {
		t.Fatalf("expected an image side-channel result, got: %+v", res)
	}
	if res.ImagePurpose != "inspect" {
		t.Fatalf("purpose not carried through: %q", res.ImagePurpose)
	}
}

// read_file routes a [document: ...] ReadFile payload (PDFs) through the same
// side-channel (session_tools_file.go document arm).
func TestW3Sub_RegisterFileTools_DocumentResult(t *testing.T) {
	payload := "[document: doc.pdf]\n" + base64.StdEncoding.EncodeToString([]byte("fakepdfbytes"))
	res := w3sub_readFileResult(t, &w3sub_readFileEnv{output: payload}, "doc.pdf")
	if res.IsError {
		t.Fatalf("unexpected error: %q", res.Output)
	}
	if len(res.ImageData) == 0 || res.ImageMediaType != "application/pdf" {
		t.Fatalf("expected a document side-channel result, got: %+v", res)
	}
}
