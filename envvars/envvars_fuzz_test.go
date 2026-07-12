package envvars

import (
	"reflect"
	"strings"
	"testing"
)

func FuzzEnvvarsSurface(f *testing.F) {
	f.Add("  value  ", uint8(0))
	f.Add("SERF_MODEL", uint8(1))
	f.Add("OPENAI", uint8(2))
	f.Add("on", uint8(3))
	f.Add("\x00", uint8(4))
	f.Add("0", uint8(255))
	f.Fuzz(func(t *testing.T, raw string, selector uint8) {
		fuzzVarHelpers(t, raw)
		fuzzRegistry(t, raw)
		fuzzProviders(t, raw, selector)
		fuzzRecorderConfig(t, raw)
	})
}

func fuzzVarHelpers(t *testing.T, raw string) {
	t.Helper()

	v := Var{Name: "SERF_ENVVARS_FUZZ_VALUE"}
	envValue := strings.ReplaceAll(raw, "\x00", "")
	t.Setenv(v.Name, envValue)

	if got := v.Getenv(); got != envValue {
		t.Fatalf("Getenv() = %q, want %q", got, envValue)
	}
	if got, ok := v.LookupEnv(); !ok || got != envValue {
		t.Fatalf("LookupEnv() = %q, %v; want %q, true", got, ok, envValue)
	}
	if got := v.Trimmed(); got != strings.TrimSpace(envValue) {
		t.Fatalf("Trimmed() = %q, want %q", got, strings.TrimSpace(envValue))
	}
	if got := v.From(nil); got != envValue {
		t.Fatalf("From(nil) = %q, want %q", got, envValue)
	}

	lookup := func(name string) string { return "  " + name + raw + "  " }
	if got := v.From(lookup); got != lookup(v.Name) {
		t.Fatalf("From(lookup) = %q, want %q", got, lookup(v.Name))
	}
	if got := v.FromTrimmed(lookup); got != strings.TrimSpace(lookup(v.Name)) {
		t.Fatalf("FromTrimmed(lookup) = %q, want %q", got, strings.TrimSpace(lookup(v.Name)))
	}

	setValue := envValue + "-set"
	if err := v.Setenv(setValue); err != nil {
		t.Fatalf("Setenv: %v", err)
	}
	if got := v.Getenv(); got != setValue {
		t.Fatalf("Getenv() after Setenv = %q, want %q", got, setValue)
	}
	if err := v.Unsetenv(); err != nil {
		t.Fatalf("Unsetenv: %v", err)
	}
	if got, ok := v.LookupEnv(); ok || got != "" {
		t.Fatalf("LookupEnv() after Unsetenv = %q, %v; want empty, false", got, ok)
	}
	if got, want := v.Assignment(raw), v.Name+"="+raw; got != want {
		t.Fatalf("Assignment(%q) = %q, want %q", raw, got, want)
	}
}

func fuzzRegistry(t *testing.T, raw string) {
	t.Helper()

	all := All()
	if len(all) == 0 {
		t.Fatal("All() returned an empty registry")
	}
	originalFirst := all[0]
	all[0] = Var{Name: "MUTATED"}
	if All()[0] != originalFirst {
		t.Fatal("All() returned an alias of the registry")
	}

	visibilities := []Visibility{Public, Internal, Inherited, Tooling, Visibility(raw)}
	for _, visibility := range visibilities {
		got := ByVisibility(visibility)
		var want []Var
		for _, v := range allVars {
			if v.Visibility == visibility {
				want = append(want, v)
			}
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("ByVisibility(%q) = %#v, want %#v", visibility, got, want)
		}
	}

	want, wantOK := Var{}, false
	for _, v := range allVars {
		if v.Name == raw {
			want, wantOK = v, true
			break
		}
	}
	if got, ok := Find(raw); ok != wantOK || got != want {
		t.Fatalf("Find(%q) = %#v, %v; want %#v, %v", raw, got, ok, want, wantOK)
	}
	if got, ok := Find(SERFModel.Name); !ok || got != SERFModel {
		t.Fatalf("Find(%q) = %#v, %v", SERFModel.Name, got, ok)
	}
	if got, ok := Find("SERF_ENVVARS_FUZZ_MISSING"); ok || got != (Var{}) {
		t.Fatalf("Find(missing) = %#v, %v", got, ok)
	}
}

func fuzzProviders(t *testing.T, raw string, selector uint8) {
	t.Helper()

	all := Providers()
	if len(all) == 0 {
		t.Fatal("Providers() returned an empty registry")
	}
	originalFirst := all[0]
	all[0] = ProviderEnv{Name: "mutated"}
	if Providers()[0].Name != originalFirst.Name {
		t.Fatal("Providers() returned an alias of the registry")
	}

	selected := providers[int(selector)%len(providers)]
	got, ok := Provider(strings.ToUpper(selected.Name))
	if !ok || !reflect.DeepEqual(got, selected) {
		t.Fatalf("Provider(%q) = %#v, %v; want %#v, true", strings.ToUpper(selected.Name), got, ok, selected)
	}

	want, wantOK := ProviderEnv{}, false
	for _, p := range providers {
		if p.Name == strings.ToLower(raw) {
			want, wantOK = p, true
			break
		}
	}
	if got, ok := Provider(raw); ok != wantOK || !reflect.DeepEqual(got, want) {
		t.Fatalf("Provider(%q) = %#v, %v; want %#v, %v", raw, got, ok, want, wantOK)
	}
	if got, ok := Provider("serf-envvars-fuzz-missing"); ok || !reflect.DeepEqual(got, ProviderEnv{}) {
		t.Fatalf("Provider(missing) = %#v, %v", got, ok)
	}

	keys := APIKeyVars(selected.Name)
	if !reflect.DeepEqual(keys, selected.APIKeyVars) {
		t.Fatalf("APIKeyVars(%q) = %#v, want %#v", selected.Name, keys, selected.APIKeyVars)
	}
	if len(keys) > 0 {
		keys[0] = Var{Name: "MUTATED"}
		if APIKeyVars(selected.Name)[0].Name == "MUTATED" {
			t.Fatal("APIKeyVars returned an alias of the provider registry")
		}
	}
	if APIKeyVars("serf-envvars-fuzz-missing") != nil {
		t.Fatal("APIKeyVars(missing) did not return nil")
	}

	if key, ok := InjectAPIKeyVar(selected.Name); ok != (selected.InjectAPIKeyVar.Name != "") || key != selected.InjectAPIKeyVar {
		t.Fatalf("InjectAPIKeyVar(%q) = %#v, %v", selected.Name, key, ok)
	}
	if key, ok := InjectAPIKeyVar("ollama"); ok || key != (Var{}) {
		t.Fatalf("InjectAPIKeyVar(ollama) = %#v, %v", key, ok)
	}
	if key, ok := InjectAPIKeyVar("serf-envvars-fuzz-missing"); ok || key != (Var{}) {
		t.Fatalf("InjectAPIKeyVar(missing) = %#v, %v", key, ok)
	}

	if base, ok := BaseURLVar(selected.Name); !ok || base != selected.BaseURLVars[0] {
		t.Fatalf("BaseURLVar(%q) = %#v, %v", selected.Name, base, ok)
	}
	if base, ok := BaseURLVar("serf-envvars-fuzz-missing"); ok || base != (Var{}) {
		t.Fatalf("BaseURLVar(missing) = %#v, %v", base, ok)
	}

	modes := AuthModes(selected.Name)
	if !reflect.DeepEqual(modes, selected.AuthModes) {
		t.Fatalf("AuthModes(%q) = %#v, want %#v", selected.Name, modes, selected.AuthModes)
	}
	if len(modes) > 0 {
		modes[0] = "mutated"
		if AuthModes(selected.Name)[0] == "mutated" {
			t.Fatal("AuthModes returned an alias of the provider registry")
		}
	}
	if AuthModes("serf-envvars-fuzz-missing") != nil {
		t.Fatal("AuthModes(missing) did not return nil")
	}
}

func fuzzRecorderConfig(t *testing.T, raw string) {
	t.Helper()

	envValue := strings.ReplaceAll(raw, "\x00", "")
	wantTruthy := false
	switch strings.ToLower(strings.TrimSpace(envValue)) {
	case "1", "true", "yes", "on":
		wantTruthy = true
	}
	if got := recordTruthy(envValue); got != wantTruthy {
		t.Fatalf("recordTruthy(%q) = %v, want %v", envValue, got, wantTruthy)
	}
	if !recordTruthy(" TRUE ") || recordTruthy("off") {
		t.Fatal("recordTruthy rejected a true spelling or accepted a false spelling")
	}

	specific := Var{Name: "SERF_ENVVARS_FUZZ_RECORDER"}
	t.Setenv(SERFFuzzRecord.Name, "off")
	t.Setenv(specific.Name, envValue)
	if got := RecorderEnabled(specific); got != wantTruthy {
		t.Fatalf("RecorderEnabled(explicit %q) = %v, want %v", envValue, got, wantTruthy)
	}

	t.Setenv(SERFFuzzRecord.Name, envValue)
	if err := specific.Unsetenv(); err != nil {
		t.Fatalf("unset specific recorder: %v", err)
	}
	if got := RecorderEnabled(specific); got != wantTruthy {
		t.Fatalf("RecorderEnabled(master %q) = %v, want %v", envValue, got, wantTruthy)
	}
}
