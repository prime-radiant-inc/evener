package registry

import (
	"reflect"
	"testing"

	"github.com/BurntSushi/toml"
)

// Every field the schema reads from providers.toml must come back out of
// MarshalConfig, so a writer that forgets a key cannot silently drop what the
// user configured. The expected values are the reflection-filled Provider and
// Model; the written bytes decode through the schema structs without the
// parser's vocabulary checks, which the placeholder values would fail.
func TestMarshalConfigWritesEveryProviderAndModelField(t *testing.T) {
	var provider Provider
	fillEveryField(reflect.ValueOf(&provider).Elem())
	var model, glob Model
	fillEveryField(reflect.ValueOf(&model).Elem())
	fillEveryField(reflect.ValueOf(&glob).Elem())

	// Fields the config writer never emits: the overlay-only keys, the
	// provider id (the table name), a row's id (its key), and in-memory state.
	provider.ID, provider.Implicit, provider.Name, provider.Doc, provider.Hidden = "", nil, "", "", false
	provider.notes = nil
	for _, m := range []*Model{&model, &glob} {
		m.ID, m.Status, m.Hidden = "", "", false
	}
	provider.Models = map[string]Model{"row": model}

	data, err := MarshalConfig(&Layer{
		Default:   "p",
		TopGlobs:  map[string]Model{"glob-*": glob},
		Providers: map[string]Provider{"p": provider},
	})
	if err != nil {
		t.Fatalf("MarshalConfig: %v", err)
	}
	var doc fileSchema
	if _, err := toml.Decode(string(data), &doc); err != nil {
		t.Fatalf("decode written config: %v\n%s", err, data)
	}

	got := doc.Providers["p"]
	wantProvider, gotProvider := reflect.ValueOf(provider), reflect.ValueOf(got)
	for field := range gotProvider.Type().Fields() {
		if field.Anonymous || field.Name == "Models" { // compared below
			continue
		}
		if want := wantProvider.FieldByName(field.Name); !reflect.DeepEqual(want.Interface(), gotProvider.FieldByIndex(field.Index).Interface()) {
			t.Errorf("Provider.%s written as %#v, want %#v", field.Name, gotProvider.FieldByIndex(field.Index).Interface(), want.Interface())
		}
	}
	if gotTransport := got.transport(); !reflect.DeepEqual(gotTransport, provider.Transport) {
		t.Errorf("provider transport written as %+v, want %+v", gotTransport, provider.Transport)
	}
	if !reflect.DeepEqual(got.Caps, provider.Caps) {
		t.Errorf("provider caps written as %+v, want %+v", got.Caps, provider.Caps)
	}
	if gotRow := got.Models["row"].model(""); !reflect.DeepEqual(gotRow, model) {
		t.Errorf("provider model row written as %+v, want %+v", gotRow, model)
	}
	if gotGlob := doc.Models["glob-*"].model(""); !reflect.DeepEqual(gotGlob, glob) {
		t.Errorf("top-level glob row written as %+v, want %+v", gotGlob, glob)
	}
	if doc.Default != "p" {
		t.Errorf("default written as %q, want p", doc.Default)
	}
}
