package agent

import (
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
		{"progress on self", watchArgs{Operation: "create", Source: "self", Target: "caller", ProgressIntervalMS: 1000}, "for a timer use repeat_seconds"},
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
}

func TestNormalizeWatchArgs_TimerBoundsRejectNotClamp(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		args watchArgs
		want string
	}{
		{watchArgs{AfterSeconds: 59}, "after_seconds must be between 60 and 86400"},
		{watchArgs{AfterSeconds: 86401}, "after_seconds must be between 60 and 86400"},
		{watchArgs{RepeatSeconds: 3601}, "repeat_seconds must be between 60 and 3600"},
	} {
		err := normalizeWatchArgs(&tc.args)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%+v: err = %v, want %q", tc.args, err, tc.want)
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

func TestWatchIntArg_PresentZeroOnCreateIsRejectedByBounds(t *testing.T) {
	t.Parallel()
	_, err := watchArgsFromToolArgs(map[string]any{"operation": "create", "after_seconds": float64(0)})
	if err == nil || !strings.Contains(err.Error(), "after_seconds must be between") {
		t.Fatalf("after_seconds:0 on create: err = %v, want bounds rejection", err)
	}
}
