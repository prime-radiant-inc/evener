package appwire

import (
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

func (p TurnSteerParams) EffectiveTurnID() string {
	return p.ExpectedTurnID
}

func (p TurnSteerParams) TargetRef() string {
	return p.ThreadID
}

func (p TurnInterruptParams) EffectiveTurnID() string {
	return p.ExpectedTurnID
}

func (p TurnInterruptParams) TargetRef() string {
	return p.ThreadID
}

// MutationInput is the canonical semantic payload accepted by retry-safe turn
// mutations. Items contains only meaningful text and image items.
type MutationInput struct {
	Items []InputItem
}

// NormalizeMutationInput enforces the flag-day retry-safe mutation input
// shape. Text and image are the only supported item types; blank text carries
// no semantic content and is removed.
func NormalizeMutationInput(items []InputItem) (MutationInput, error) {
	normalized := MutationInput{Items: make([]InputItem, 0, len(items))}
	for i, item := range items {
		switch item.Type {
		case "text":
			if strings.TrimSpace(item.Text) == "" {
				continue
			}
		case "image":
		default:
			return MutationInput{}, fmt.Errorf("input[%d].type %q is unsupported; want text or image", i, item.Type)
		}
		normalized.Items = append(normalized.Items, cloneMutationInputItem(item))
	}
	return normalized, nil
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
