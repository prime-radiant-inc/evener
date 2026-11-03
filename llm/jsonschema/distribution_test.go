package jsonschema_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestLeafPackagesOutsideWorkspace(t *testing.T) {
	module, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	consumer := t.TempDir()
	mod := fmt.Sprintf("module numeric-consumer.test\n\ngo 1.27.0\n\nrequire primeradiant.com/evener/llm v0.0.0\nreplace primeradiant.com/evener/llm => %s\n", module)
	if err := os.WriteFile(filepath.Join(consumer, "go.mod"), []byte(mod), 0600); err != nil {
		t.Fatal(err)
	}
	const program = `package main
import (
 "encoding/json"
 "errors"
 "os"
 "primeradiant.com/evener/llm/jsonschema"
 "primeradiant.com/evener/llm/jsonschema/decimal"
)
func main() {
 n, err := decimal.Parse("9007199254740993")
 if err != nil || !n.IsInteger() || n.String() != "9007199254740993" { panic("decimal integer") }
 neighbor, err := decimal.Parse("9007199254740992")
 if err != nil || n.Cmp(neighbor) != 1 { panic("exact comparison") }
 s, err := jsonschema.CompileString("https://consumer.test/exact", ` + "`" + `{"type":"integer","const":9007199254740993}` + "`" + `)
 if err != nil || s == nil { panic(err) }
 if err := s.Validate(json.Number(n.String())); err != nil { panic(err) }
 var invalid *jsonschema.ValidationError
 if err := s.Validate(json.Number(neighbor.String())); !errors.As(err, &invalid) { panic("const rejection") }
 tiny, err := jsonschema.CompileString("https://consumer.test/tiny", ` + "`" + `{"type":"number","minimum":0,"maximum":1}` + "`" + `)
 if err != nil { panic(err) }
 if err := tiny.Validate(json.Number("1e-1000000000")); err != nil { panic(err) }
 if err := json.NewEncoder(os.Stdout).Encode(map[string]any{"exact":n.String(),"neighborRejected":true,"tinyValidated":true}); err != nil { panic(err) }
}
`
	if err := os.WriteFile(filepath.Join(consumer, "main.go"), []byte(program), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "go", "run", ".")
	cmd.Dir = consumer
	cmd.Env = append(os.Environ(), "GOWORK=off", "GOPROXY=off", "GOSUMDB=off", "GOFLAGS=", "GOTOOLCHAIN=local", "GO111MODULE=on")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("offline leaf consumer: %v deadline=%v\n%s", err, ctx.Err(), out)
	}
	var result struct {
		Exact            string `json:"exact"`
		NeighborRejected bool   `json:"neighborRejected"`
		TinyValidated    bool   `json:"tinyValidated"`
	}
	if err := json.Unmarshal(out, &result); err != nil {
		t.Fatalf("consumer result: %v\n%s", err, out)
	}
	if result.Exact != "9007199254740993" || !result.NeighborRejected || !result.TinyValidated {
		t.Fatalf("consumer result=%+v", result)
	}
	t.Logf("offline consumer imported both leaf packages and executed exact validation, only llm replaced, GOWORK/GOPROXY/GOSUMDB off: %s", out)
}
