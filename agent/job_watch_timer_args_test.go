package agent

import (
	"math"
	"strings"
	"testing"
)

func TestWatchArgsFromToolArgs_TimerFieldsParseAndDefaultSourceToSelf(t *testing.T) {
	t.Parallel()
	a, err := watchArgsFromToolArgs(map[string]any{
		"operation": "create", "repeat_seconds": float64(300), "note": "PR #123: newer than id 0",
	})
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if a.Source != "self" || a.RepeatSeconds != 300 || a.Note != "PR #123: newer than id 0" {
		t.Fatalf("args = %+v, want source self, repeat 300, note kept", a)
	}
	one, err := watchArgsFromToolArgs(map[string]any{"operation": "create", "source": nil, "after_seconds": float64(600)})
	if err != nil || one.Source != "self" || one.AfterSeconds != 600 {
		t.Fatalf("null source with after_seconds: args=%+v err=%v", one, err)
	}
}

func TestWatchArgsFromToolArgs_SourceStillRequiredWithoutTimeTrigger(t *testing.T) {
	t.Parallel()
	_, err := watchArgsFromToolArgs(map[string]any{"operation": "create", "events": []any{"assistant.tool"}})
	if err == nil || !strings.Contains(err.Error(), "source is required") {
		t.Fatalf("err = %v, want source is required", err)
	}
}

func TestWatchArgsFromToolArgs_IntegerArgumentsAreStrict(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		field string
		value any
	}{
		{"after_seconds", "600"}, {"after_seconds", 600.5},
		{"repeat_seconds", "300"}, {"progress_interval_ms", "1000"}, {"every", 1.5},
	} {
		_, err := watchArgsFromToolArgs(map[string]any{"operation": "create", "source": "self", tc.field: tc.value})
		if err == nil || !strings.Contains(err.Error(), tc.field+" must be an integer") {
			t.Errorf("%s=%v: err = %v, want must be an integer", tc.field, tc.value, err)
		}
	}
}

// TestWatchArgsFromToolArgs_SourceMustBeAString pins the one string argument
// whose empty value MEANS something: an unnamed source on a timer create becomes
// self. Coercing a present non-string to "" therefore does not fail closed — it
// silently creates a self timer out of a request that named something else.
func TestWatchArgsFromToolArgs_SourceMustBeAString(t *testing.T) {
	t.Parallel()
	for _, value := range []any{float64(123), 123, true, []any{"self"}, map[string]any{}} {
		_, err := watchArgsFromToolArgs(map[string]any{"operation": "create", "source": value, "after_seconds": float64(600)})
		if err == nil || !strings.Contains(err.Error(), "invalid_request: source must be a string") {
			t.Errorf("source=%v: err = %v, want source must be a string", value, err)
		}
	}
	// Absent, null, and blank all keep saying "no source named", so a timer
	// create still defaults to self.
	for _, args := range []map[string]any{
		{"operation": "create", "after_seconds": float64(600)},
		{"operation": "create", "source": nil, "after_seconds": float64(600)},
		{"operation": "create", "source": "", "after_seconds": float64(600)},
		{"operation": "create", "source": "  ", "after_seconds": float64(600)},
	} {
		a, err := watchArgsFromToolArgs(args)
		if err != nil || a.Source != "self" {
			t.Errorf("%v: args = %+v err = %v, want the self default", args, a, err)
		}
	}
}

func TestWatchArgsFromToolArgs_TimerFieldsAreCreateOnlyAndNullIsNeutral(t *testing.T) {
	t.Parallel()
	if _, err := watchArgsFromToolArgs(map[string]any{"operation": "list", "after_seconds": nil, "repeat_seconds": float64(0), "note": ""}); err != nil {
		t.Fatalf("neutral timer fields on list: %v", err)
	}
	_, err := watchArgsFromToolArgs(map[string]any{"operation": "clear", "watch_id": "w1", "note": "x"})
	if err == nil || !strings.Contains(err.Error(), "operation=\"create\"") {
		t.Fatalf("note on clear: err = %v, want create-only rejection", err)
	}
}

func TestValidateWatchTriggerShape_TimerRules(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		args watchArgs
		want string
	}{
		{"repeat on delegate", watchArgs{Operation: "create", Source: "dlg_a", Target: "caller", RepeatSeconds: 300}, "timers apply to source self"},
		{"after on job", watchArgs{Operation: "create", Source: "job_1", Target: "job_1", AfterSeconds: 600}, "timers apply to source self"},
		{"note without timer", watchArgs{Operation: "create", Source: "self", Target: "caller", Events: []string{"assistant.tool"}, Note: "x"}, "note applies to timers"},
		{"both time fields", watchArgs{Operation: "create", Source: "self", Target: "caller", AfterSeconds: 60, RepeatSeconds: 60}, "after_seconds and repeat_seconds"},
		{"timer with output_match", watchArgs{Operation: "create", Source: "self", Target: "caller", RepeatSeconds: 60, OutputMatch: "x"}, "repeat_seconds and output_match"},
		{"timer with send", watchArgs{Operation: "create", Source: "self", Target: "caller", RepeatSeconds: 300, Send: &watchSendArgs{To: "dlg_a"}}, "repeat_seconds and send are mutually exclusive"},
		{"one-shot with send", watchArgs{Operation: "create", Source: "self", Target: "caller", AfterSeconds: 600, Send: &watchSendArgs{To: "dlg_a"}}, "after_seconds and send are mutually exclusive"},
		{"progress on self", watchArgs{Operation: "create", Source: "self", Target: "caller", ProgressIntervalMS: 1000}, "for a timer use repeat_seconds"},
		// A request that names both fields is told about both, not pointed at
		// the timer field it already used.
		{"timer with progress", watchArgs{Operation: "create", Source: "self", Target: "caller", RepeatSeconds: 300, ProgressIntervalMS: 1000}, "repeat_seconds and progress_interval_ms are mutually exclusive"},
		{"progress on delegate", watchArgs{Operation: "create", Source: "dlg_a", Target: "caller", ProgressIntervalMS: 1000}, "for a timer use repeat_seconds"},
	}
	for _, tc := range cases {
		err := validateWatchTriggerShape(tc.args)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want %q", tc.name, err, tc.want)
		}
	}
	if err := validateWatchTriggerShape(watchArgs{Operation: "create", Target: "caller", ProgressIntervalMS: 1000}); err != nil {
		t.Fatalf("internal target-only progress call must stay allowed: %v", err)
	}
	if err := validateWatchTriggerShape(watchArgs{Operation: "create", Source: "self", Target: "caller", RepeatSeconds: 300, Note: "check the queue"}); err != nil {
		t.Fatalf("valid repeating self timer rejected: %v", err)
	}
	if err := validateWatchTriggerShape(watchArgs{Operation: "create", Source: "self", Target: "caller", AfterSeconds: 600}); err != nil {
		t.Fatalf("valid one-shot self timer rejected: %v", err)
	}
}

func TestNormalizeWatchArgs_TimerBoundsRejectNotClamp(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		args watchArgs
		want string
	}{
		{watchArgs{AfterSeconds: 59}, "after_seconds must be between 60 and 86400"},
		{watchArgs{AfterSeconds: 86401}, "after_seconds must be between 60 and 86400"},
		{watchArgs{RepeatSeconds: 59}, "repeat_seconds must be between 60 and 3600"},
		{watchArgs{RepeatSeconds: 3601}, "repeat_seconds must be between 60 and 3600"},
	} {
		err := normalizeWatchArgs(&tc.args)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%+v: err = %v, want %q", tc.args, err, tc.want)
		}
	}
	// The bounds are inclusive at both ends.
	for _, edge := range []watchArgs{
		{AfterSeconds: 60}, {AfterSeconds: 86400}, {RepeatSeconds: 60}, {RepeatSeconds: 3600},
	} {
		if err := normalizeWatchArgs(&edge); err != nil {
			t.Errorf("%+v: err = %v, want the edge of the range accepted", edge, err)
		}
	}
	ok := watchArgs{RepeatSeconds: 300, Note: strings.Repeat("n", 3000)}
	if err := normalizeWatchArgs(&ok); err != nil {
		t.Fatal(err)
	}
	if len(ok.Note) > watchMessageMaxChars {
		t.Fatalf("note not truncated: len=%d", len(ok.Note))
	}
	if !watchArgsHasCondition(watchArgs{Target: "caller", RepeatSeconds: 300}) || !watchArgsHasCondition(watchArgs{Target: "caller", AfterSeconds: 60}) {
		t.Fatal("time fields must count as conditions")
	}
}

// TestWatchIntArg_MaterializedZeroOnCreateReadsAsAbsent pins the timer fields to
// the contract progress_interval_ms already keeps: providers materialize every
// optional property, so a 0 or null time field on a create that arms some other
// trigger is not a timer and must not be refused.
func TestWatchIntArg_MaterializedZeroOnCreateReadsAsAbsent(t *testing.T) {
	t.Parallel()
	a, err := watchArgsFromToolArgs(map[string]any{
		"operation": "create", "source": "self", "events": []any{"assistant.tool"},
		"after_seconds": float64(0), "repeat_seconds": nil, "note": "",
	})
	if err != nil {
		t.Fatalf("materialized neutral timer fields on create: %v", err)
	}
	if a.AfterSeconds != 0 || a.RepeatSeconds != 0 || watchArgsIsTimer(a) {
		t.Fatalf("args = %+v, want both time fields zero and not a timer", a)
	}
	// A zero time field arms nothing, so it cannot stand in for the source.
	_, err = watchArgsFromToolArgs(map[string]any{"operation": "create", "after_seconds": float64(0)})
	if err == nil || !strings.Contains(err.Error(), "source is required") {
		t.Fatalf("after_seconds:0 with no source: err = %v, want source is required", err)
	}
}

// TestWatchIntArg_BoundIsThePlatformIntRange pins the shared parser's numeric
// bound to what int can hold. every predates the timer fields, its schema
// documents no maximum, and it used to be read by shellIntArg, which accepted
// any integral float64 a provider sent; an int32 bound here would silently
// shrink that argument.
func TestWatchIntArg_BoundIsThePlatformIntRange(t *testing.T) {
	t.Parallel()
	if math.MaxInt <= math.MaxInt32 {
		t.Skip("32-bit platform: int cannot hold a value above int32")
	}
	for _, tc := range []struct {
		name  string
		value any
		want  int
	}{
		{"above int32", float64(3_000_000_000), 3_000_000_000},
		{"below negative int32", float64(-3_000_000_000), -3_000_000_000},
		{"int32 max", float64(math.MaxInt32), math.MaxInt32},
	} {
		n, ok, err := watchIntArg(map[string]any{"every": tc.value}, "every")
		if err != nil || !ok || n != tc.want {
			t.Errorf("%s: watchIntArg = (%d, %v, %v), want (%d, true, nil)", tc.name, n, ok, err, tc.want)
		}
	}
	for _, tc := range []struct {
		name  string
		value any
	}{
		{"above the int range", 1e19},
		{"below the int range", -1e19},
		{"non-integral", 1.5},
		{"string", "600"},
	} {
		if _, _, err := watchIntArg(map[string]any{"every": tc.value}, "every"); err == nil ||
			!strings.Contains(err.Error(), "every must be an integer") {
			t.Errorf("%s: err = %v, want every must be an integer", tc.name, err)
		}
	}
}
