package appwire

import (
	"encoding/json"
	"fmt"
	"maps"
	"strings"
)

func (p TurnStartParams) EffectiveInput() []InputItem {
	return p.Input
}

func (p ThreadStartParams) EffectiveInput() []InputItem {
	return p.Input
}

func (p TurnStartParams) TargetRef() string {
	return p.ThreadID
}

func (p TurnSteerParams) EffectiveInput() []InputItem {
	return p.Input
}

func (p TurnSteerParams) TargetRef() string {
	return p.ThreadID
}

func (p TurnInterruptParams) TargetRef() string {
	return p.ThreadID
}

// MutationInput is the canonical semantic payload accepted by retry-safe turn
// mutations. Items contains only meaningful text and image items.
type MutationInput struct {
	Items []InputItem
}

// UnmarshalJSON decodes one input item. Skill selections are canonical-only:
// ordinary json.Unmarshal silently drops unknown fields, so a skill item that
// smuggles a body or path in raw JSON (fields the InputItem struct never
// names) would decode as if it were canonical. For type "skill" every raw key
// other than "type" and "name" is therefore an error. Non-skill items keep
// the ordinary decode semantics, including ignoring unknown keys.
func (i *InputItem) UnmarshalJSON(data []byte) error {
	type plain InputItem
	var decoded plain
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	if decoded.Type == "skill" {
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(data, &fields); err != nil {
			return err
		}
		for field := range fields {
			if field != "type" && field != "name" {
				return fmt.Errorf("skill input rejects field %q", field)
			}
		}
	}
	*i = InputItem(decoded)
	return nil
}

// NormalizeMutationInput enforces the flag-day retry-safe mutation input
// shape. Text, image, and canonical skill selections are the supported item
// types; blank text carries no semantic content and is removed. Skill items
// carry only a canonical catalog name: populated body-ish or path-ish
// struct fields are rejected for Go callers the raw-key check cannot see, and
// the name is trimmed and required non-blank. Exact catalog identity and
// invocation policy remain consumption-time checks.
func NormalizeMutationInput(items []InputItem) (MutationInput, error) {
	normalized := MutationInput{Items: make([]InputItem, 0, len(items))}
	for i, item := range items {
		switch item.Type {
		case "text":
			if strings.TrimSpace(item.Text) == "" {
				continue
			}
		case "image":
		case "skill":
			if item.Text != "" || item.URL != "" || item.MediaType != "" || len(item.Data) != 0 || item.Path != "" || len(item.Metadata) != 0 {
				return MutationInput{}, fmt.Errorf("input[%d].type %q rejects populated text, url, mediaType, data, path, or metadata fields", i, item.Type)
			}
			name := strings.TrimSpace(item.Name)
			if name == "" {
				return MutationInput{}, fmt.Errorf("input[%d].name is required for skill selections", i)
			}
			item.Name = name
		default:
			return MutationInput{}, fmt.Errorf("input[%d].type %q is unsupported; want text, image, or skill", i, item.Type)
		}
		normalized.Items = append(normalized.Items, cloneMutationInputItem(item))
	}
	return normalized, nil
}

// ValidateSkillInputSupport is the pure consumption gate for skill input
// items: any skill item requires the target to advertise skill input
// support. A false verdict means every skill item is an unsupported-input
// error, which is how an endpoint that has not wired skill consumption keeps
// rejecting skill selections.
func ValidateSkillInputSupport(items []InputItem, supported bool) error {
	if supported {
		return nil
	}
	for i, item := range items {
		if item.Type == "skill" {
			return fmt.Errorf("input[%d]: skill input is unsupported on this target", i)
		}
	}
	return nil
}

// HasContent reports whether normalization retained meaningful input.
func (i MutationInput) HasContent() bool {
	return len(i.Items) != 0
}

func cloneMutationInputItem(item InputItem) InputItem {
	item.Data = append([]byte(nil), item.Data...)
	if item.Metadata != nil {
		metadata := item.Metadata
		item.Metadata = make(map[string]string, len(metadata))
		maps.Copy(item.Metadata, metadata)
	}
	return item
}
