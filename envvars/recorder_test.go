package envvars

import "testing"

func TestRecordTruthy(t *testing.T) {
	for _, c := range []struct {
		in   string
		want bool
	}{
		{"1", true}, {"true", true}, {"TRUE", true}, {"yes", true}, {"on", true}, {" on ", true},
		{"0", false}, {"false", false}, {"off", false}, {"", false}, {"nope", false},
	} {
		if got := recordTruthy(c.in); got != c.want {
			t.Errorf("recordTruthy(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

// TestRecorderEnabled covers the master-switch precedence: an explicitly-set
// per-recorder var (on OR off) always wins; when it is unset, the recorder
// follows EVENER_FUZZ_RECORD; with nothing set, recording is off.
func TestRecorderEnabled(t *testing.T) {
	specific := EVENERRecordAppwire
	cases := []struct {
		name        string
		master      *string // nil = unset
		perRecorder *string // nil = unset
		want        bool
	}{
		{"nothing set -> off", nil, nil, false},
		{"master on, recorder unset -> on", new("1"), nil, true},
		{"master on, recorder off -> off (override)", new("true"), new("0"), false},
		{"master off, recorder on -> on (override)", new("0"), new("yes"), true},
		{"master unset, recorder on -> on (back-compat)", nil, new("on"), true},
		{"master unset, recorder empty -> off (explicit)", nil, new(""), false},
		{"master on, recorder empty -> off (explicit)", new("1"), new(""), false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if c.master != nil {
				t.Setenv(EVENERFuzzRecord.Name, *c.master)
			} else {
				t.Setenv(EVENERFuzzRecord.Name, "")
				if err := EVENERFuzzRecord.Unsetenv(); err != nil {
					t.Fatal(err)
				}
			}
			if c.perRecorder != nil {
				t.Setenv(specific.Name, *c.perRecorder)
			} else {
				t.Setenv(specific.Name, "")
				if err := specific.Unsetenv(); err != nil {
					t.Fatal(err)
				}
			}
			if got := RecorderEnabled(specific); got != c.want {
				t.Errorf("RecorderEnabled() = %v, want %v", got, c.want)
			}
		})
	}
}

//go:fix inline
