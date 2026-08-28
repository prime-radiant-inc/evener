package appwire

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

const (
	MethodEvenerSettingsTranscriptDisplayGet   = "evener/settings/transcriptDisplay/get"
	MethodEvenerSettingsTranscriptDisplayPatch = "evener/settings/transcriptDisplay/patch"

	NotifyEvenerSettingsTranscriptDisplayChanged = "evener/settings/transcriptDisplay/changed"
)

type TranscriptViewportClass string

const (
	TranscriptViewportClassDesktop TranscriptViewportClass = "desktop"
	TranscriptViewportClassMobile  TranscriptViewportClass = "mobile"

	TranscriptViewportDesktop = TranscriptViewportClassDesktop
	TranscriptViewportMobile  = TranscriptViewportClassMobile
)

type TranscriptLevel string

const (
	TranscriptLevelChat     TranscriptLevel = "chat"
	TranscriptLevelIntent   TranscriptLevel = "intent"
	TranscriptLevelTools    TranscriptLevel = "tools"
	TranscriptLevelActivity TranscriptLevel = "activity"
	TranscriptLevelFull     TranscriptLevel = "full"
)

type TranscriptHookExitDetail string

const (
	TranscriptHookExitDetailNone       TranscriptHookExitDetail = "none"
	TranscriptHookExitDetailSuccessful TranscriptHookExitDetail = "successful"
	TranscriptHookExitDetailAll        TranscriptHookExitDetail = "all"

	TranscriptHookExitNone       = TranscriptHookExitDetailNone
	TranscriptHookExitSuccessful = TranscriptHookExitDetailSuccessful
	TranscriptHookExitAll        = TranscriptHookExitDetailAll
)

type TranscriptContentKind string

const (
	TranscriptContentKindPreset TranscriptContentKind = "preset"
	TranscriptContentKindCustom TranscriptContentKind = "custom"

	TranscriptContentPreset = TranscriptContentKindPreset
	TranscriptContentCustom = TranscriptContentKindCustom
)

type TranscriptDisplayCustomContent struct {
	ToolIntent      bool `json:"toolIntent"`
	ToolCalls       bool `json:"toolCalls"`
	Reasoning       bool `json:"reasoning"`
	ExpandByDefault bool `json:"expandByDefault"`
}

type TranscriptDisplayContent struct {
	Kind   TranscriptContentKind           `json:"kind"`
	Level  TranscriptLevel                 `json:"level,omitempty"`
	Custom *TranscriptDisplayCustomContent `json:"custom,omitempty"`
}

type TranscriptDisplayAdvanced struct {
	RoundTimings  bool                     `json:"roundTimings"`
	TokenCounts   bool                     `json:"tokenCounts"`
	EstimatedCost bool                     `json:"estimatedCost"`
	SystemEvents  bool                     `json:"systemEvents"`
	PromptEvents  bool                     `json:"promptEvents"`
	HookExits     TranscriptHookExitDetail `json:"hookExits"`
}

type TranscriptDisplayConfig struct {
	Version  int                       `json:"version"`
	Content  TranscriptDisplayContent  `json:"content"`
	Advanced TranscriptDisplayAdvanced `json:"advanced"`
}

// TranscriptDisplayConfigV1 is the versioned configuration shape used by the
// current transcript-display protocol. It is an alias so later versions can
// introduce a distinct Go type without changing the v1 wire contract.
type TranscriptDisplayConfigV1 = TranscriptDisplayConfig

type TranscriptDisplayDefault struct {
	Revision uint64                  `json:"revision"`
	Config   TranscriptDisplayConfig `json:"config"`
}

// TranscriptDisplayDefaults is the canonical pair returned by the get method.
type TranscriptDisplayDefaults struct {
	Desktop TranscriptDisplayDefault `json:"desktop"`
	Mobile  TranscriptDisplayDefault `json:"mobile"`
}

// TranscriptDisplayGetResponse is kept as an operation-oriented name for
// callers; the wire result's underlying named type is
// TranscriptDisplayDefaults.
type TranscriptDisplayGetResponse = TranscriptDisplayDefaults

type TranscriptDisplayDefaultsPatchParams struct {
	Layout           TranscriptViewportClass `json:"layout"`
	ExpectedRevision uint64                  `json:"expectedRevision"`
	Config           TranscriptDisplayConfig `json:"config"`
}

type TranscriptDisplayPatchResponse struct {
	Layout   TranscriptViewportClass `json:"layout"`
	Revision uint64                  `json:"revision"`
	Config   TranscriptDisplayConfig `json:"config"`
}

type TranscriptDisplayDefaultsPatchResponse = TranscriptDisplayPatchResponse

type TranscriptDisplayChangedParams struct {
	Layout   TranscriptViewportClass `json:"layout"`
	Revision uint64                  `json:"revision"`
	Config   TranscriptDisplayConfig `json:"config"`
}

type TranscriptDisplayChangedNotification = TranscriptDisplayChangedParams

type TranscriptDisplayConflictData struct {
	EvenerErrorInfo ErrorInfo                `json:"evenerErrorInfo"`
	Layout          TranscriptViewportClass  `json:"layout"`
	Current         TranscriptDisplayDefault `json:"current"`
}

const transcriptDisplayConfigVersion = 1

func TranscriptDisplayShippedDefaults() TranscriptDisplayDefaults {
	return TranscriptDisplayDefaults{
		Desktop: TranscriptDisplayDefault{Config: transcriptDisplayPreset(TranscriptLevelTools)},
		Mobile:  TranscriptDisplayDefault{Config: transcriptDisplayPreset(TranscriptLevelIntent)},
	}
}

func transcriptDisplayPreset(level TranscriptLevel) TranscriptDisplayConfig {
	return TranscriptDisplayConfig{
		Version: transcriptDisplayConfigVersion,
		Content: TranscriptDisplayContent{
			Kind:  TranscriptContentKindPreset,
			Level: level,
		},
		Advanced: TranscriptDisplayAdvanced{HookExits: TranscriptHookExitDetailNone},
	}
}

func ValidateTranscriptDisplayConfig(config TranscriptDisplayConfig) error {
	if config.Version != transcriptDisplayConfigVersion {
		return fmt.Errorf("unsupported transcript display config version %d", config.Version)
	}

	switch config.Content.Kind {
	case TranscriptContentKindPreset:
		if !validTranscriptLevel(config.Content.Level) {
			return fmt.Errorf("invalid transcript display level %q", config.Content.Level)
		}
		if config.Content.Custom != nil {
			return errors.New("preset content cannot include custom content")
		}
	case TranscriptContentKindCustom:
		if config.Content.Level != "" {
			return errors.New("custom content cannot include a preset level")
		}
		if config.Content.Custom == nil {
			return errors.New("custom content is required")
		}
	default:
		return fmt.Errorf("invalid transcript content kind %q", config.Content.Kind)
	}

	if !validTranscriptHookExitDetail(config.Advanced.HookExits) {
		return fmt.Errorf("invalid transcript hook exit detail %q", config.Advanced.HookExits)
	}
	return nil
}

func validTranscriptLevel(level TranscriptLevel) bool {
	switch level {
	case TranscriptLevelChat, TranscriptLevelIntent, TranscriptLevelTools, TranscriptLevelActivity, TranscriptLevelFull:
		return true
	default:
		return false
	}
}

func validTranscriptHookExitDetail(detail TranscriptHookExitDetail) bool {
	switch detail {
	case TranscriptHookExitDetailNone, TranscriptHookExitDetailSuccessful, TranscriptHookExitDetailAll:
		return true
	default:
		return false
	}
}

func validTranscriptViewportClass(layout TranscriptViewportClass) bool {
	switch layout {
	case TranscriptViewportClassDesktop, TranscriptViewportClassMobile:
		return true
	default:
		return false
	}
}

func DecodeTranscriptDisplayDefaultsPatchParams(raw json.RawMessage) (TranscriptDisplayDefaultsPatchParams, error) {
	var params TranscriptDisplayDefaultsPatchParams
	if err := decodeTranscriptDisplayStrictJSON(raw, &params); err != nil {
		return TranscriptDisplayDefaultsPatchParams{}, err
	}

	top, err := transcriptDisplayObjectFields(raw)
	if err != nil {
		return TranscriptDisplayDefaultsPatchParams{}, err
	}
	if err := requireTranscriptDisplayFields(top, "layout", "expectedRevision", "config"); err != nil {
		return TranscriptDisplayDefaultsPatchParams{}, err
	}
	if string(bytes.TrimSpace(top["expectedRevision"])) == "null" {
		return TranscriptDisplayDefaultsPatchParams{}, errors.New("expectedRevision must be an unsigned integer")
	}
	if !validTranscriptViewportClass(params.Layout) {
		return TranscriptDisplayDefaultsPatchParams{}, fmt.Errorf("invalid transcript viewport class %q", params.Layout)
	}

	configFields, err := transcriptDisplayObjectFields(top["config"])
	if err != nil {
		return TranscriptDisplayDefaultsPatchParams{}, fmt.Errorf("config: %w", err)
	}
	if err := requireTranscriptDisplayFields(configFields, "version", "content", "advanced"); err != nil {
		return TranscriptDisplayDefaultsPatchParams{}, fmt.Errorf("config: %w", err)
	}
	advancedFields, err := transcriptDisplayObjectFields(configFields["advanced"])
	if err != nil {
		return TranscriptDisplayDefaultsPatchParams{}, fmt.Errorf("advanced: %w", err)
	}
	if err := requireTranscriptDisplayFields(advancedFields, "roundTimings", "tokenCounts", "estimatedCost", "systemEvents", "promptEvents", "hookExits"); err != nil {
		return TranscriptDisplayDefaultsPatchParams{}, fmt.Errorf("advanced: %w", err)
	}
	if err := requireTranscriptDisplayBooleans(advancedFields, "roundTimings", "tokenCounts", "estimatedCost", "systemEvents", "promptEvents"); err != nil {
		return TranscriptDisplayDefaultsPatchParams{}, fmt.Errorf("advanced: %w", err)
	}

	contentFields, err := transcriptDisplayObjectFields(configFields["content"])
	if err != nil {
		return TranscriptDisplayDefaultsPatchParams{}, fmt.Errorf("content: %w", err)
	}
	if err := requireTranscriptDisplayFields(contentFields, "kind"); err != nil {
		return TranscriptDisplayDefaultsPatchParams{}, fmt.Errorf("content: %w", err)
	}
	switch params.Config.Content.Kind {
	case TranscriptContentKindPreset:
		if err := requireTranscriptDisplayFields(contentFields, "level"); err != nil {
			return TranscriptDisplayDefaultsPatchParams{}, fmt.Errorf("content: %w", err)
		}
		if _, ok := contentFields["custom"]; ok {
			return TranscriptDisplayDefaultsPatchParams{}, errors.New("content cannot contain both preset and custom representations")
		}
	case TranscriptContentKindCustom:
		if err := requireTranscriptDisplayFields(contentFields, "custom"); err != nil {
			return TranscriptDisplayDefaultsPatchParams{}, fmt.Errorf("content: %w", err)
		}
		if _, ok := contentFields["level"]; ok {
			return TranscriptDisplayDefaultsPatchParams{}, errors.New("content cannot contain both custom and preset representations")
		}
		customFields, err := transcriptDisplayObjectFields(contentFields["custom"])
		if err != nil {
			return TranscriptDisplayDefaultsPatchParams{}, fmt.Errorf("custom: %w", err)
		}
		if err := requireTranscriptDisplayFields(customFields, "toolIntent", "toolCalls", "reasoning", "expandByDefault"); err != nil {
			return TranscriptDisplayDefaultsPatchParams{}, fmt.Errorf("custom: %w", err)
		}
		if err := requireTranscriptDisplayBooleans(customFields, "toolIntent", "toolCalls", "reasoning", "expandByDefault"); err != nil {
			return TranscriptDisplayDefaultsPatchParams{}, fmt.Errorf("custom: %w", err)
		}
	default:
		return TranscriptDisplayDefaultsPatchParams{}, fmt.Errorf("invalid transcript content kind %q", params.Config.Content.Kind)
	}

	if err := ValidateTranscriptDisplayConfig(params.Config); err != nil {
		return TranscriptDisplayDefaultsPatchParams{}, err
	}
	return params, nil
}

func decodeTranscriptDisplayStrictJSON(raw []byte, dst any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(dst); err != nil {
		return fmt.Errorf("invalid transcript display patch: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return errors.New("invalid transcript display patch: trailing JSON value")
		}
		return fmt.Errorf("invalid transcript display patch: trailing JSON: %w", err)
	}
	return nil
}

func transcriptDisplayObjectFields(raw json.RawMessage) (map[string]json.RawMessage, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return nil, err
	}
	if fields == nil {
		return nil, errors.New("expected JSON object")
	}
	return fields, nil
}

func requireTranscriptDisplayFields(fields map[string]json.RawMessage, names ...string) error {
	for _, name := range names {
		if _, ok := fields[name]; !ok {
			return fmt.Errorf("missing required field %q", name)
		}
	}
	return nil
}

func requireTranscriptDisplayBooleans(fields map[string]json.RawMessage, names ...string) error {
	for _, name := range names {
		if value := bytes.TrimSpace(fields[name]); !bytes.Equal(value, []byte("true")) && !bytes.Equal(value, []byte("false")) {
			return fmt.Errorf("field %q must be a boolean", name)
		}
	}
	return nil
}
