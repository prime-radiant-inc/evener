package launchconfig

import (
	"reflect"
	"strings"
	"testing"
)

// layerFieldKey is the schema row key for one Layer field: its toml name, or
// the explicit name for a field hidden from toml.
func layerFieldKey(f reflect.StructField) string {
	tag, _, _ := strings.Cut(f.Tag.Get("toml"), ",")
	if tag == "-" {
		if f.Name == "EnabledPlugins" {
			return "enabled_plugins"
		}
		return ""
	}
	return tag
}

// layerFieldValue returns a distinctive non-zero value for one Layer field, so a
// conversion that drops the field shows up.
func layerFieldValue(f reflect.StructField) reflect.Value {
	v := reflect.New(f.Type).Elem()
	switch f.Type {
	case reflect.TypeFor[string]():
		v.SetString("x")
	case reflect.TypeFor[int]():
		v.SetInt(7)
	case reflect.TypeFor[*bool]():
		b := true
		v.Set(reflect.ValueOf(&b))
	case reflect.TypeFor[*int]():
		n := 7
		v.Set(reflect.ValueOf(&n))
	case reflect.TypeFor[[]string]():
		v.Set(reflect.ValueOf([]string{"x"}))
	case reflect.TypeFor[*[]string]():
		s := []string{"x"}
		v.Set(reflect.ValueOf(&s))
	case reflect.TypeFor[[]MCPServerSpec]():
		v.Set(reflect.ValueOf([]MCPServerSpec{{Name: "m", Command: "c", Args: []string{"a"}}}))
	case reflect.TypeFor[map[string]string]():
		v.Set(reflect.ValueOf(map[string]string{"K": "v"}))
	default:
		panic("layerFieldValue: unhandled Layer field type " + f.Type.String() + " for " + f.Name)
	}
	return v
}

// layerWith sets one field, then applies the companion values the field only
// takes effect alongside (a prompt file needs its mode, sandbox_net needs a
// non-off sandbox).
func layerWith(f reflect.StructField) Layer {
	var l Layer
	reflect.ValueOf(&l).Elem().FieldByName(f.Name).Set(layerFieldValue(f))
	switch f.Name {
	case "SystemPromptFile":
		l.SystemPromptMode = "file"
	case "SystemPromptText":
		l.SystemPromptMode = "inline"
	case "SystemPromptAppendFile":
		l.SystemPromptAppendMode = "file"
	case "SystemPromptAppendText":
		l.SystemPromptAppendMode = "inline"
	case "SystemPromptMode":
		l.SystemPromptMode = "file"
		l.SystemPromptFile = "x"
	case "SystemPromptAppendMode":
		l.SystemPromptAppendMode = "file"
		l.SystemPromptAppendFile = "x"
	case "SandboxNet":
		l.Sandbox = "read-only"
	}
	return l
}

// layerFields is every Layer field; a field added to Layer lands in each of the
// checks below until it is covered or named as an exclusion.
func layerFields() []reflect.StructField {
	typ := reflect.TypeFor[Layer]()
	fields := make([]reflect.StructField, 0, typ.NumField())
	for i := range typ.NumField() {
		fields = append(fields, typ.Field(i))
	}
	return fields
}

func TestEveryLayerFieldHasASchemaRow(t *testing.T) {
	exclusions := map[string]string{
		"Schema":             "the file's schema version, not a launch option",
		"SystemPromptAppend": "legacy spelling folded into the append mode and file at merge",
	}
	rows := map[string]bool{}
	for _, opt := range LaunchOptionSchema() {
		rows[opt.Field] = true
	}
	for _, f := range layerFields() {
		if _, excluded := exclusions[f.Name]; excluded {
			continue
		}
		if key := layerFieldKey(f); !rows[key] {
			t.Errorf("Layer.%s has no LaunchOptionSchema row %q; add a row or name an exclusion", f.Name, key)
		}
	}
}

func TestEveryLayerFieldSurvivesMerge(t *testing.T) {
	exclusions := map[string]string{
		"Schema":             "the file's schema version is not merged",
		"SystemPromptAppend": "legacy spelling; merge folds it into the append mode and file",
	}
	for _, f := range layerFields() {
		t.Run(f.Name, func(t *testing.T) {
			if reason, excluded := exclusions[f.Name]; excluded {
				t.Skip(reason)
			}
			// app_replay_size is honored only at the global layer.
			in := layerWith(f)
			resolved, _ := mergeLayers(map[LayerName]Layer{LayerGlobal: in})
			got := reflect.ValueOf(resolved.Effective).FieldByName(f.Name).Interface()
			if want := reflect.ValueOf(in).FieldByName(f.Name).Interface(); !reflect.DeepEqual(got, want) {
				t.Fatalf("Layer.%s merged to %#v, want %#v", f.Name, got, want)
			}
		})
	}
}

func TestEveryLayerFieldRoundTripsTheWire(t *testing.T) {
	for _, f := range layerFields() {
		t.Run(f.Name, func(t *testing.T) {
			in := layerWith(f)
			if got := FromWire(ToWire(in)); !reflect.DeepEqual(got, in) {
				t.Fatalf("FromWire(ToWire(layer)) = %+v, want %+v", got, in)
			}
		})
	}
}

func TestEveryLayerFieldReachesArgs(t *testing.T) {
	exclusions := map[string]string{
		"Schema":                 "the file's schema version is not an argument",
		"Env":                    "delivered by ToEnv, not argv",
		"SystemPromptText":       "spawn writes inline text to a file and renders the file mode",
		"SystemPromptAppendText": "spawn writes inline text to a file and renders the file mode",
	}
	for _, f := range layerFields() {
		t.Run(f.Name, func(t *testing.T) {
			if reason, excluded := exclusions[f.Name]; excluded {
				t.Skip(reason)
			}
			// The baseline keeps the companion fields and unsets only the
			// field under test.
			with := layerWith(f)
			without := with
			reflect.ValueOf(&without).Elem().FieldByName(f.Name).SetZero()
			if got, base := ToArgs(Resolved{Effective: with}), ToArgs(Resolved{Effective: without}); reflect.DeepEqual(got, base) {
				t.Fatalf("Layer.%s set in Effective leaves argv unchanged", f.Name)
			}
		})
	}
}
