package tool

import (
	"context"
	"encoding/json"
	"math"
	"strconv"
	"strings"
	"testing"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/llm"
)

func TestExactArgumentDocuments(t *testing.T) {
	for _, raw := range []string{"", "null", "{}", `{"nested":[9007199254740993,1e-1000000000,0e-1000000000,1.0000000000000001]}`} {
		args, err := DecodeArguments([]byte(raw))
		if err != nil {
			t.Fatalf("%q: %v", raw, err)
		}
		if raw != "" && raw != "null" && raw != "{}" {
			b, err := json.Marshal(args)
			if err != nil || string(b) != raw {
				t.Fatalf("retained %s, %v, want %s", b, err, raw)
			}
		}
	}
	for _, raw := range []string{" ", "[]", "1", `"text"`, "true", "{} {}", "{} garbage", `{"n":1e400}`, `{"nested":[{"n":-1e400}]}`, `{"n":1,}`} {
		if _, err := DecodeArguments([]byte(raw)); err == nil {
			t.Errorf("accepted %q", raw)
		}
	}
}

func TestExactArgumentDuplicateNumbers(t *testing.T) {
	for _, raw := range []string{`{"n":1e400,"n":0}`, `{"n":{"v":-1e400},"n":0}`, `{"n":[1e400],"n":[]}`} {
		var old map[string]any
		if err := json.Unmarshal([]byte(raw), &old); err == nil {
			t.Fatalf("legacy decoder unexpectedly accepted %s", raw)
		}
		if _, err := DecodeArguments([]byte(raw)); err == nil {
			t.Errorf("accepted overwritten overflow in %s", raw)
		}
	}
	for _, tc := range []struct{ raw, want string }{
		{`{"n":1,"n":2}`, `{"n":2}`},
		{`{"n":9007199254740993,"n":1.0000000000000001}`, `{"n":1.0000000000000001}`},
		{`{"n":{"v":-42},"n":0}`, `{"n":0}`},
		{`{"n":[12e2],"n":[]}`, `{"n":[]}`},
		{`{"n":1e-1000000000,"n":2e-1000000000}`, `{"n":2e-1000000000}`},
		{`{"n":{"v":1e-400},"n":0e-1000000000}`, `{"n":0e-1000000000}`},
	} {
		var old map[string]any
		if err := json.Unmarshal([]byte(tc.raw), &old); err != nil {
			t.Fatalf("legacy decoder rejected control %s: %v", tc.raw, err)
		}
		args, err := DecodeArguments([]byte(tc.raw))
		if err != nil {
			t.Fatalf("duplicate control %s: %v", tc.raw, err)
		}
		got, err := json.Marshal(args)
		if err != nil || string(got) != tc.want {
			t.Errorf("%s retained %s, %v, want %s", tc.raw, got, err, tc.want)
		}
	}
}

func TestWholeNumberInt(t *testing.T) {
	for _, tc := range []struct {
		raw   string
		want  int
		valid bool
	}{
		{"1.0", 1, true}, {"12e2", 1200, true}, {"-1e0", -1, true}, {"0e-1000000000", 0, true},
		{"1.0000000000000001", 0, false}, {"1e-400", 0, false}, {"1e-1000000000", 0, false},
		{"9223372036854775808", 0, false}, {"-9223372036854775808", math.MinInt, true}, {"9223372036854775807", math.MaxInt, true},
	} {
		got, ok := WholeNumberInt(json.Number(tc.raw))
		if ok != tc.valid || ok && got != tc.want {
			t.Errorf("%s: (%d,%v), want (%d,%v)", tc.raw, got, ok, tc.want, tc.valid)
		}
	}
}

func TestExactBreakerNumbers(t *testing.T) {
	key := func(n string) string {
		t.Helper()
		b, ok := canonicalArgumentBytes("test", []byte(`{"n":`+n+`}`), nil)
		if !ok {
			t.Fatal("canonicalization rejected", n)
		}
		return string(b)
	}
	for _, pair := range [][2]string{{"9007199254740993", "9.007199254740993e15"}, {"-9007199254740993", "-9007199254740993.0"}, {"18446744073709551617", "18446744073709551617.0"}, {"1.5", "1.50"}} {
		if key(pair[0]) != key(pair[1]) {
			t.Errorf("equivalent values differ: %v", pair)
		}
	}
	for _, pair := range [][2]string{{"9007199254740993.0", "9007199254740992.0"}, {"1.0000000000000001", "1"}, {"1e-400", "0"}, {"1e-1000000000", "0"}, {"1e-1000000000", "2e-1000000000"}} {
		if key(pair[0]) == key(pair[1]) {
			t.Errorf("distinct values merged: %v", pair)
		}
	}
}

func TestExactSchemaNumbers(t *testing.T) {
	for _, constraint := range []string{`"type":"integer"`, `"minimum":9007199254740993`, `"maximum":9007199254740993`, `"multipleOf":9007199254740993`, `"const":9007199254740993`, `"enum":[9007199254740993]`} {
		t.Run(constraint, func(t *testing.T) {
			var schema map[string]any
			dec := json.NewDecoder(strings.NewReader(`{"type":"object","properties":{"n":{` + constraint + `}}}`))
			dec.UseNumber()
			if err := dec.Decode(&schema); err != nil {
				t.Fatal(err)
			}
			r := NewRegistry()
			calls := 0
			if err := r.Register(RegisteredTool{Definition: llm.ToolDefinition{Name: "exact", Description: "test", Parameters: schema}, Exec: func(context.Context, execenv.ExecutionEnvironment, map[string]any) (any, error) {
				calls++
				return nil, nil
			}}); err != nil {
				t.Fatal(err)
			}
			for _, n := range []string{"9007199254740993", "9007199254740993.0", "9.007199254740993e15"} {
				result := r.ExecuteCall(context.Background(), nil, llm.ToolCallData{Name: "exact", Arguments: json.RawMessage(`{"n":` + n + `}`)})
				if result.IsError {
					t.Fatalf("%s: %+v", n, result)
				}
			}
			if calls != 3 {
				t.Fatalf("calls=%d", calls)
			}
		})
	}
}

func TestValidationRecoveryDoesNotCoverExecution(t *testing.T) {
	r := NewRegistry()
	if err := r.Register(RegisteredTool{Definition: llm.ToolDefinition{Name: "panic_exec", Description: "test", Parameters: map[string]any{"type": "object"}}, Exec: func(context.Context, execenv.ExecutionEnvironment, map[string]any) (any, error) {
		panic("executor sentinel")
	}}); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if got := recover(); got != "executor sentinel" {
			t.Errorf("execution panic=%v", got)
		}
	}()
	r.ExecuteCall(context.Background(), nil, llm.ToolCallData{Name: "panic_exec", Arguments: json.RawMessage(`{}`)})
	t.Fatal("execution panic swallowed")
}

func TestExactSchemaRejections(t *testing.T) {
	checks := []struct{ constraint, good, bad string }{
		{`"type":"integer"`, "9007199254740993.0", "1.0000000000000001"},
		{`"minimum":9007199254740993`, "9007199254740993", "9007199254740992"},
		{`"maximum":9007199254740992`, "9007199254740992", "9007199254740993"},
		{`"minimum":0`, "1e-400", "-1e-400"},
		{`"multipleOf":1`, "9007199254740993.0", "1.0000000000000001"},
		{`"const":9007199254740993`, "9.007199254740993e15", "9007199254740992"},
		{`"enum":[9007199254740993]`, "9007199254740993.0", "9007199254740992"},
	}
	for _, size := range []int{2, 21} {
		values := []string{"9007199254740993", "9007199254740992"}
		for i := 2; i < size; i++ {
			values = append(values, strconv.Itoa(i))
		}
		good := "[" + strings.Join(values, ",") + "]"
		values[1] = "9.007199254740993e15"
		checks = append(checks, struct{ constraint, good, bad string }{`"type":"array","uniqueItems":true`, good, "[" + strings.Join(values, ",") + "]"})
	}
	for _, tc := range checks {
		t.Run(tc.constraint+tc.good, func(t *testing.T) {
			var property map[string]any
			dec := json.NewDecoder(strings.NewReader("{" + tc.constraint + "}"))
			dec.UseNumber()
			if err := dec.Decode(&property); err != nil {
				t.Fatal(err)
			}
			r := NewRegistry()
			calls := 0
			if err := r.Register(RegisteredTool{Definition: llm.ToolDefinition{Name: "exact_reject", Description: "test", Parameters: map[string]any{"type": "object", "properties": map[string]any{"n": property}}}, Exec: func(context.Context, execenv.ExecutionEnvironment, map[string]any) (any, error) {
				calls++
				return nil, nil
			}}); err != nil {
				t.Fatal(err)
			}
			for i, value := range []string{tc.good, tc.bad, tc.good} {
				result := r.ExecuteCall(context.Background(), nil, llm.ToolCallData{Name: "exact_reject", Arguments: json.RawMessage(`{"n":` + value + `}`)})
				if result.IsError != (i == 1) {
					t.Fatalf("%s: %+v", value, result)
				}
			}
			if calls != 2 {
				t.Fatalf("executed %d times, want only the2 valid calls", calls)
			}
		})
	}
}
