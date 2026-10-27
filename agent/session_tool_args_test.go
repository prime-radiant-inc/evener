package agent

import (
	"math"
	"testing"
	"time"

	"primeradiant.com/evener/agent/internal/jobstore"
)

// The tool-argument decoders split into a strict family (a present value of
// the wrong shape is rejected) and a lenient family (it reads as absent).
// These tables pin which inputs each family accepts so consolidating the
// helpers cannot change what a model may send.

func argPtr[T any](v T) *T { return &v }

func samePtr[T comparable](a, b *T) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

func TestOptionalStringArg(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		args    map[string]any
		want    string
		wantErr bool
	}{
		{name: "absent", args: map[string]any{}},
		{name: "null", args: map[string]any{"k": nil}},
		{name: "string", args: map[string]any{"k": "v"}, want: "v"},
		{name: "number", args: map[string]any{"k": 3.0}, wantErr: true},
		{name: "bool", args: map[string]any{"k": true}, wantErr: true},
		{name: "array", args: map[string]any{"k": []any{"v"}}, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := optionalStringArg(tt.args, "k")
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if got != tt.want {
				t.Fatalf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestOptionalBoolArg(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		args    map[string]any
		want    *bool
		wantErr bool
	}{
		{name: "absent", args: map[string]any{}},
		{name: "null", args: map[string]any{"k": nil}},
		{name: "true", args: map[string]any{"k": true}, want: argPtr(true)},
		{name: "false", args: map[string]any{"k": false}, want: argPtr(false)},
		{name: "string", args: map[string]any{"k": "true"}, wantErr: true},
		{name: "number", args: map[string]any{"k": 1.0}, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := optionalBoolArg(tt.args, "k")
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if !samePtr(got, tt.want) {
				t.Fatalf("got %v, want %v", got, tt.want)
			}
		})
	}
}

func TestOptionalWholeIntArg(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		args    map[string]any
		want    *int
		wantErr bool
	}{
		{name: "absent", args: map[string]any{}},
		{name: "null", args: map[string]any{"k": nil}},
		{name: "zero", args: map[string]any{"k": 0.0}, want: argPtr(0)},
		{name: "whole", args: map[string]any{"k": 7.0}, want: argPtr(7)},
		{name: "fractional", args: map[string]any{"k": 1.5}, wantErr: true},
		{name: "negative", args: map[string]any{"k": -1.0}, wantErr: true},
		{name: "nan", args: map[string]any{"k": math.NaN()}, wantErr: true},
		{name: "infinity", args: map[string]any{"k": math.Inf(1)}, wantErr: true},
		{name: "string", args: map[string]any{"k": "3"}, wantErr: true},
		{name: "native int is not a decoded JSON number", args: map[string]any{"k": 3}, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := optionalWholeIntArg(tt.args, "k")
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if !samePtr(got, tt.want) {
				t.Fatalf("got %v, want %v", got, tt.want)
			}
		})
	}
}

func TestOptionalTimeArg(t *testing.T) {
	t.Parallel()

	want := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	tests := []struct {
		name    string
		args    map[string]any
		want    *time.Time
		wantErr bool
	}{
		{name: "absent", args: map[string]any{}},
		{name: "null", args: map[string]any{"k": nil}},
		{name: "blank", args: map[string]any{"k": "  "}},
		{name: "rfc3339", args: map[string]any{"k": "2026-01-02T03:04:05Z"}, want: &want},
		{name: "not a timestamp", args: map[string]any{"k": "yesterday"}, wantErr: true},
		{name: "number", args: map[string]any{"k": 5.0}, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := optionalTimeArg(tt.args, "k")
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if (got == nil) != (tt.want == nil) || (got != nil && !got.Equal(*tt.want)) {
				t.Fatalf("got %v, want %v", got, tt.want)
			}
		})
	}
}

func TestWatchIntArg(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		args    map[string]any
		want    int
		present bool
		wantErr bool
	}{
		{name: "absent", args: map[string]any{}},
		{name: "null", args: map[string]any{"k": nil}},
		{name: "float whole", args: map[string]any{"k": 4.0}, want: 4, present: true},
		{name: "native int", args: map[string]any{"k": 4}, want: 4, present: true},
		{name: "negative", args: map[string]any{"k": -2.0}, want: -2, present: true},
		{name: "fractional", args: map[string]any{"k": 1.5}, wantErr: true},
		{name: "nan", args: map[string]any{"k": math.NaN()}, wantErr: true},
		{name: "string", args: map[string]any{"k": "3"}, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, present, err := watchIntArg(tt.args, "k")
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if got != tt.want || present != tt.present {
				t.Fatalf("got (%d, %v), want (%d, %v)", got, present, tt.want, tt.present)
			}
		})
	}
}

func TestWatchStringArg(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		args    map[string]any
		want    string
		wantErr bool
	}{
		{name: "absent", args: map[string]any{}},
		{name: "null", args: map[string]any{"k": nil}},
		{name: "string", args: map[string]any{"k": "s"}, want: "s"},
		{name: "number", args: map[string]any{"k": 1.0}, wantErr: true},
		{name: "bool", args: map[string]any{"k": true}, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := watchStringArg(tt.args, "k")
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if got != tt.want {
				t.Fatalf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestLenientStringArg(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		args map[string]any
		want string
	}{
		{name: "absent", args: map[string]any{}},
		{name: "null", args: map[string]any{"k": nil}},
		{name: "number reads as empty", args: map[string]any{"k": 3.0}},
		{name: "string", args: map[string]any{"k": "v"}, want: "v"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := stringArg(tt.args, "k"); got != tt.want {
				t.Fatalf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestLenientOptionalIntArg(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		args map[string]any
		want *int
	}{
		{name: "absent", args: map[string]any{}},
		{name: "null", args: map[string]any{"k": nil}},
		{name: "string reads as absent", args: map[string]any{"k": "3"}},
		{name: "native int is not a decoded JSON number", args: map[string]any{"k": 3}},
		{name: "whole", args: map[string]any{"k": 3.0}, want: argPtr(3)},
		{name: "fractional truncates", args: map[string]any{"k": 1.9}, want: argPtr(1)},
		{name: "negative", args: map[string]any{"k": -4.0}, want: argPtr(-4)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := optionalIntArg(tt.args, "k"); !samePtr(got, tt.want) {
				t.Fatalf("got %v, want %v", got, tt.want)
			}
		})
	}
}

// The three list helpers differ in one detail a consolidation must keep:
// stringArrayArg reads null as absent, while the status and type helpers
// reject it.
func TestStringListArgs(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		args        map[string]any
		wantStrErr  bool
		wantStatErr bool
		wantTypeErr bool
		wantLen     int
	}{
		{name: "absent", args: map[string]any{}},
		{name: "null", args: map[string]any{"k": nil}, wantStatErr: true, wantTypeErr: true},
		{name: "empty array", args: map[string]any{"k": []any{}}},
		{name: "not an array", args: map[string]any{"k": "running"}, wantStrErr: true, wantStatErr: true, wantTypeErr: true},
		{name: "native string slice is not decoded JSON", args: map[string]any{"k": []string{"running"}}, wantStrErr: true, wantStatErr: true, wantTypeErr: true},
		{name: "non-string element", args: map[string]any{"k": []any{1.0}}, wantStrErr: true, wantStatErr: true, wantTypeErr: true},
		{name: "known status", args: map[string]any{"k": []any{"running"}}, wantTypeErr: true, wantLen: 1},
		{name: "known type", args: map[string]any{"k": []any{"shell"}}, wantStatErr: true, wantLen: 1},
		{name: "unknown value", args: map[string]any{"k": []any{"bogus"}}, wantStatErr: true, wantTypeErr: true, wantLen: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			strs, err := stringArrayArg(tt.args, "k")
			if (err != nil) != tt.wantStrErr {
				t.Fatalf("stringArrayArg err = %v, wantErr %v", err, tt.wantStrErr)
			}
			if err == nil && len(strs) != tt.wantLen {
				t.Fatalf("stringArrayArg len = %d, want %d", len(strs), tt.wantLen)
			}
			statuses, err := jobStatusArrayArg(tt.args, "k")
			if (err != nil) != tt.wantStatErr {
				t.Fatalf("jobStatusArrayArg err = %v, wantErr %v", err, tt.wantStatErr)
			}
			if err == nil && len(statuses) != tt.wantLen {
				t.Fatalf("jobStatusArrayArg len = %d, want %d", len(statuses), tt.wantLen)
			}
			types, err := jobTypeArrayArg(tt.args, "k")
			if (err != nil) != tt.wantTypeErr {
				t.Fatalf("jobTypeArrayArg err = %v, wantErr %v", err, tt.wantTypeErr)
			}
			if err == nil && len(types) != tt.wantLen {
				t.Fatalf("jobTypeArrayArg len = %d, want %d", len(types), tt.wantLen)
			}
		})
	}
}

func TestJobStatusArrayArgKeepsOrder(t *testing.T) {
	t.Parallel()

	statuses, err := jobStatusArrayArg(map[string]any{"k": []any{"completed", "running"}}, "k")
	if err != nil {
		t.Fatal(err)
	}
	if len(statuses) != 2 || statuses[0] != jobstore.StatusCompleted || statuses[1] != jobstore.StatusRunning {
		t.Fatalf("statuses = %v", statuses)
	}
}
