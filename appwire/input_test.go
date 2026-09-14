package appwire

import (
	"encoding/json"
	"reflect"
	"slices"
	"testing"
)

func TestNormalizeMutationInputRejectsNonCanonicalTypes(t *testing.T) {
	for _, itemType := range []string{"", "input_text", "input_image", "audio"} {
		t.Run(itemType, func(t *testing.T) {
			if _, err := NormalizeMutationInput([]InputItem{{Type: itemType, Text: "legacy"}}); err == nil {
				t.Fatalf("NormalizeMutationInput accepted type %q", itemType)
			}
		})
	}
}

func TestNormalizeMutationInputPreservesCanonicalPayload(t *testing.T) {
	input := []InputItem{
		{Type: "text", Text: " \n "},
		{Type: "text", Text: "canonical text"},
		{Type: "image", MediaType: "image/png", Data: []byte{1, 2, 3}, Name: "proof.png", Metadata: map[string]string{"source": "test"}},
	}
	normalized, err := NormalizeMutationInput(input)
	if err != nil {
		t.Fatalf("NormalizeMutationInput: %v", err)
	}
	if !normalized.HasContent() || len(normalized.Items) != 2 {
		t.Fatalf("normalized input = %#v", normalized.Items)
	}
	text, image := normalized.Items[0], normalized.Items[1]
	if text.Type != "text" || text.Text != "canonical text" ||
		image.Type != "image" || image.MediaType != "image/png" || image.Name != "proof.png" ||
		!slices.Equal(image.Data, []byte{1, 2, 3}) || image.Metadata["source"] != "test" {
		t.Fatalf("normalized input = %#v", normalized.Items)
	}
	input[2].Data[0] = 9
	input[2].Metadata["source"] = "mutated"
	if normalized.Items[1].Data[0] != 1 || normalized.Items[1].Metadata["source"] != "test" {
		t.Fatalf("normalized input aliases caller: %#v", normalized.Items[1])
	}
}

func TestNormalizeMutationInputSkillSelection(t *testing.T) {
	want := []InputItem{{Type: "text", Text: "REQUEST_55a"}, {Type: "skill", Name: "pkg:probe"},
		{Type: "image", MediaType: "image/png", Data: []byte{1, 2}}}
	got, err := NormalizeMutationInput(want)
	if err != nil {
		t.Fatal(err)
	}
	if !got.HasContent() || !reflect.DeepEqual(got.Items, want) {
		t.Fatalf("input=%#v", got.Items)
	}
	only, err := NormalizeMutationInput(want[1:2])
	if err != nil || !only.HasContent() {
		t.Fatalf("skill-only=%#v error=%v", only, err)
	}
}

func TestSkillInputRejectsRawPathAndBody(t *testing.T) {
	for _, raw := range []string{
		`{"type":"skill","name":"pkg:probe","path":""}`,
		`{"type":"skill","name":"pkg:probe","body":"BODY_55a"}`,
		`{"type":"skill","name":"pkg:probe","text":""}`,
	} {
		var item InputItem
		if err := json.Unmarshal([]byte(raw), &item); err == nil {
			t.Fatalf("accepted=%s", raw)
		}
	}
}

func TestSkillInputUnmarshalAcceptsOnlyTypeAndName(t *testing.T) {
	var item InputItem
	if err := json.Unmarshal([]byte(`{"type":"skill","name":"pkg:probe"}`), &item); err != nil {
		t.Fatalf("canonical skill item rejected: %v", err)
	}
	if !reflect.DeepEqual(item, InputItem{Type: "skill", Name: "pkg:probe"}) {
		t.Fatalf("decoded skill item = %#v", item)
	}
	// A missing name is a normalization-time validation, not a decode error:
	// the raw-key contract only rejects non-{type,name} fields.
	var unnamed InputItem
	if err := json.Unmarshal([]byte(`{"type":"skill"}`), &unnamed); err != nil {
		t.Fatalf("nameless skill item failed decode: %v", err)
	}
	if unnamed.Type != "skill" || unnamed.Name != "" {
		t.Fatalf("nameless skill item = %#v", unnamed)
	}
}

func TestNormalizeMutationInputSkillRejectsForbiddenStructFields(t *testing.T) {
	for name, item := range map[string]InputItem{
		"text":      {Type: "skill", Name: "pkg:probe", Text: "BODY_55a"},
		"url":       {Type: "skill", Name: "pkg:probe", URL: "file:///skill"},
		"mediaType": {Type: "skill", Name: "pkg:probe", MediaType: "image/png"},
		"data":      {Type: "skill", Name: "pkg:probe", Data: []byte{1}},
		"path":      {Type: "skill", Name: "pkg:probe", Path: "/skills/probe/SKILL.md"},
		"metadata":  {Type: "skill", Name: "pkg:probe", Metadata: map[string]string{"k": "v"}},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := NormalizeMutationInput([]InputItem{item}); err == nil {
				t.Fatalf("NormalizeMutationInput accepted skill item with populated %s field", name)
			}
		})
	}
}

func TestNormalizeMutationInputSkillNameValidation(t *testing.T) {
	for _, name := range []string{"", "   ", "\t\n"} {
		if _, err := NormalizeMutationInput([]InputItem{{Type: "skill", Name: name}}); err == nil {
			t.Fatalf("NormalizeMutationInput accepted blank skill name %q", name)
		}
	}
	normalized, err := NormalizeMutationInput([]InputItem{{Type: "skill", Name: "  pkg:probe  "}})
	if err != nil {
		t.Fatalf("NormalizeMutationInput: %v", err)
	}
	if len(normalized.Items) != 1 || normalized.Items[0].Name != "pkg:probe" {
		t.Fatalf("trimmed skill name = %#v", normalized.Items)
	}
}

func TestNormalizeMutationInputSkillDuplicatesRetained(t *testing.T) {
	// Duplicate selections are an invocation-policy question, which stays a
	// consumption-time check: normalization only enforces shape, so canonical
	// duplicates pass through unchanged.
	input := []InputItem{
		{Type: "skill", Name: "pkg:probe"},
		{Type: "skill", Name: "pkg:probe"},
		{Type: "skill", Name: "pkg:other"},
	}
	normalized, err := NormalizeMutationInput(input)
	if err != nil {
		t.Fatalf("NormalizeMutationInput: %v", err)
	}
	if !normalized.HasContent() || !reflect.DeepEqual(normalized.Items, input) {
		t.Fatalf("duplicate skill selections = %#v", normalized.Items)
	}
}

func TestValidateSkillInputSupport(t *testing.T) {
	skill := []InputItem{{Type: "skill", Name: "pkg:probe"}}
	text := []InputItem{{Type: "text", Text: "REQUEST_55a"}}
	mixed := []InputItem{{Type: "text", Text: "REQUEST_55a"}, {Type: "skill", Name: "pkg:probe"}}
	if err := ValidateSkillInputSupport(skill, true); err != nil {
		t.Fatalf("supported skill input rejected: %v", err)
	}
	if err := ValidateSkillInputSupport(text, false); err != nil {
		t.Fatalf("text input rejected without skill support: %v", err)
	}
	if err := ValidateSkillInputSupport(nil, false); err != nil {
		t.Fatalf("empty input rejected without skill support: %v", err)
	}
	if err := ValidateSkillInputSupport(skill, false); err == nil {
		t.Fatal("skill input accepted without skill support")
	}
	if err := ValidateSkillInputSupport(mixed, false); err == nil {
		t.Fatal("mixed input with skill item accepted without skill support")
	}
}

func TestSkillInputNonSkillDecodingUnchanged(t *testing.T) {
	// Unknown raw keys on non-skill items keep their ordinary
	// json.Unmarshal semantics: silently ignored, exactly as before the
	// skill-specific raw-key check existed.
	var text InputItem
	if err := json.Unmarshal([]byte(`{"type":"text","text":"hi","body":"BODY_55a"}`), &text); err != nil {
		t.Fatalf("text item with unknown raw key failed decode: %v", err)
	}
	if !reflect.DeepEqual(text, InputItem{Type: "text", Text: "hi"}) {
		t.Fatalf("text item = %#v", text)
	}
	var image InputItem
	if err := json.Unmarshal([]byte(`{"type":"image","mediaType":"image/png","data":"AQI=","path":""}`), &image); err != nil {
		t.Fatalf("image item with unknown raw key failed decode: %v", err)
	}
	if image.Type != "image" || image.MediaType != "image/png" || !slices.Equal(image.Data, []byte{1, 2}) || image.Path != "" {
		t.Fatalf("image item = %#v", image)
	}
	var untyped InputItem
	if err := json.Unmarshal([]byte(`{"name":"n"}`), &untyped); err != nil {
		t.Fatalf("typeless item failed decode: %v", err)
	}
	if !reflect.DeepEqual(untyped, InputItem{Name: "n"}) {
		t.Fatalf("typeless item = %#v", untyped)
	}
}

func TestInputBearingParamsDecodeSkillSelection(t *testing.T) {
	for _, decoder := range inputBearingParamsDecoders() {
		t.Run(decoder.name, func(t *testing.T) {
			items, err := decoder.decode(`{"input":[{"type":"text","text":"REQUEST_55a"},{"type":"skill","name":"pkg:probe"}]}`)
			if err != nil {
				t.Fatalf("decode: %v", err)
			}
			want := []InputItem{{Type: "text", Text: "REQUEST_55a"}, {Type: "skill", Name: "pkg:probe"}}
			if !reflect.DeepEqual(items, want) {
				t.Fatalf("decoded input = %#v", items)
			}
			// Skill-only selections decode on every input-bearing method.
			items, err = decoder.decode(`{"input":[{"type":"skill","name":"pkg:probe"}]}`)
			if err != nil {
				t.Fatalf("decode skill-only: %v", err)
			}
			if !reflect.DeepEqual(items, []InputItem{{Type: "skill", Name: "pkg:probe"}}) {
				t.Fatalf("skill-only input = %#v", items)
			}
		})
	}
}

func TestInputBearingParamsRejectRawSkillBodyAndPath(t *testing.T) {
	for _, raw := range []string{
		`{"input":[{"type":"skill","name":"pkg:probe","body":"BODY_55a"}]}`,
		`{"input":[{"type":"skill","name":"pkg:probe","path":"/skills/probe/SKILL.md"}]}`,
		`{"input":[{"type":"skill","name":"pkg:probe","text":"REQUEST_55a"}]}`,
	} {
		for _, decoder := range inputBearingParamsDecoders() {
			if _, err := decoder.decode(raw); err == nil {
				t.Fatalf("%s accepted %s", decoder.name, raw)
			}
		}
	}
}

func inputBearingParamsDecoders() []struct {
	name   string
	decode func(raw string) ([]InputItem, error)
} {
	return []struct {
		name   string
		decode func(raw string) ([]InputItem, error)
	}{
		{"ThreadStartParams", func(raw string) ([]InputItem, error) {
			var params ThreadStartParams
			if err := json.Unmarshal([]byte(raw), &params); err != nil {
				return nil, err
			}
			return params.Input, nil
		}},
		{"TurnStartParams", func(raw string) ([]InputItem, error) {
			var params TurnStartParams
			if err := json.Unmarshal([]byte(raw), &params); err != nil {
				return nil, err
			}
			return params.Input, nil
		}},
		{"TurnSteerParams", func(raw string) ([]InputItem, error) {
			var params TurnSteerParams
			if err := json.Unmarshal([]byte(raw), &params); err != nil {
				return nil, err
			}
			return params.Input, nil
		}},
		{"TurnQueueParams", func(raw string) ([]InputItem, error) {
			var params TurnQueueParams
			if err := json.Unmarshal([]byte(raw), &params); err != nil {
				return nil, err
			}
			return params.Input, nil
		}},
		{"TurnDrainAsSteerParams", func(raw string) ([]InputItem, error) {
			var params TurnDrainAsSteerParams
			if err := json.Unmarshal([]byte(raw), &params); err != nil {
				return nil, err
			}
			return params.Input, nil
		}},
	}
}
