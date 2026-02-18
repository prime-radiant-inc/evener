package cmdutil

import "testing"

func TestMaxRoundsToConfig(t *testing.T) {
	tests := []struct {
		name     string
		cli      int
		wantConf int
	}{
		{"not specified", -1, 0},   // 0 → applyDefaults sets to 200
		{"unlimited", 0, -1},       // -1 → no limit in session loop
		{"explicit limit", 50, 50}, // pass through
		{"explicit limit 1", 1, 1}, // edge case
		{"very negative", -999, 0}, // any negative → not specified
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := MaxRoundsToConfig(tt.cli)
			if got != tt.wantConf {
				t.Fatalf("MaxRoundsToConfig(%d) = %d, want %d", tt.cli, got, tt.wantConf)
			}
		})
	}
}

func TestResolveReasoningEffort(t *testing.T) {
	tests := []struct {
		name    string
		cli     string
		env     string
		wantSet bool
		wantVal string
		wantErr bool
	}{
		{name: "unset", cli: "", env: "", wantSet: false},
		{name: "env medium", cli: "", env: "medium", wantSet: true, wantVal: "medium"},
		{name: "cli overrides env", cli: "HIGH", env: "low", wantSet: true, wantVal: "high"},
		{name: "cli none clears", cli: "none", env: "high", wantSet: true, wantVal: ""},
		{name: "env none clears", cli: "", env: "none", wantSet: true, wantVal: ""},
		{name: "invalid", cli: "banana", env: "", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ResolveReasoningEffort(tt.cli, tt.env)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err=%v wantErr=%v", err, tt.wantErr)
			}
			if tt.wantErr {
				return
			}
			if got.Set != tt.wantSet {
				t.Fatalf("Set=%v want %v", got.Set, tt.wantSet)
			}
			if got.Value != tt.wantVal {
				t.Fatalf("Value=%q want %q", got.Value, tt.wantVal)
			}
		})
	}
}

func TestStringSliceFlag(t *testing.T) {
	var f StringSliceFlag
	if err := f.Set("a"); err != nil {
		t.Fatal(err)
	}
	if err := f.Set("b"); err != nil {
		t.Fatal(err)
	}
	if f.String() != "a,b" {
		t.Fatalf("String() = %q, want %q", f.String(), "a,b")
	}
	if len(f) != 2 || f[0] != "a" || f[1] != "b" {
		t.Fatalf("values = %v, want [a b]", []string(f))
	}
}
