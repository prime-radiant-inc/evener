package appwire

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

const (
	MethodEvenerSettingsKeybindingsGet   = "evener/settings/keybindings/get"
	MethodEvenerSettingsKeybindingsPatch = "evener/settings/keybindings/patch"

	NotifyEvenerSettingsKeybindingsChanged = "evener/settings/keybindings/changed"
)

// KeybindingsRule is one user override: a non-nil Chord rebinds the action, a
// nil Chord (JSON null) unbinds it. The server stores rules verbatim; last
// rule wins on conflict, resolved by the frontend.
type KeybindingsRule struct {
	Action string  `json:"action"`
	Chord  *string `json:"chord"`
}

// KeybindingsConfig is the user-controlled part of the keybindings overrides:
// the payload contract minus the store-owned revision.
type KeybindingsConfig struct {
	Version int               `json:"version"`
	Rules   []KeybindingsRule `json:"rules"`
}

// KeybindingsOverrides is the canonical payload returned by the get method and
// broadcast on change: {version, revision, rules}.
type KeybindingsOverrides struct {
	Version  int               `json:"version"`
	Revision uint64            `json:"revision"`
	Rules    []KeybindingsRule `json:"rules"`
}

// KeybindingsGetResponse is kept as an operation-oriented name for callers;
// the wire result's underlying named type is KeybindingsOverrides.
type KeybindingsGetResponse = KeybindingsOverrides

type KeybindingsPatchParams struct {
	ExpectedRevision uint64            `json:"expectedRevision"`
	Config           KeybindingsConfig `json:"config"`
}

type KeybindingsPatchResponse = KeybindingsOverrides

type KeybindingsChangedParams = KeybindingsOverrides

type KeybindingsChangedNotification = KeybindingsOverrides

type KeybindingsConflictData struct {
	EvenerErrorInfo ErrorInfo            `json:"evenerErrorInfo"`
	Current         KeybindingsOverrides `json:"current"`
}

// KeybindingsPostRenameData is the WireError.Data payload for
// ErrorKeybindingsPostRename: the patch APPLIED (Applied carries the
// canonical applied state) but a follow-up durable step failed. Clients
// should reconcile from Applied instead of treating the write as rejected.
type KeybindingsPostRenameData struct {
	EvenerErrorInfo ErrorInfo            `json:"evenerErrorInfo"`
	Applied         KeybindingsOverrides `json:"applied"`
}

const keybindingsConfigVersion = 1

// KeybindingsShippedDefaults is the payload when nothing is persisted: empty
// overrides (all default bindings) at revision 0.
func KeybindingsShippedDefaults() KeybindingsOverrides {
	return KeybindingsOverrides{Version: keybindingsConfigVersion, Rules: []KeybindingsRule{}}
}

// ValidateKeybindingsConfig checks the structural contract only: the version
// and rule-field sanity. Semantic validation (action ids, reserved chords,
// conflicts) is the frontend's job; the server does not know the action
// registry.
func ValidateKeybindingsConfig(config KeybindingsConfig) error {
	if config.Version != keybindingsConfigVersion {
		return fmt.Errorf("unsupported keybindings config version %d", config.Version)
	}
	for i, rule := range config.Rules {
		if strings.TrimSpace(rule.Action) == "" {
			return fmt.Errorf("rule %d: action must be a non-empty string", i)
		}
		if rule.Chord != nil && strings.TrimSpace(*rule.Chord) == "" {
			return fmt.Errorf("rule %d (%s): chord must be null or a non-empty string", i, rule.Action)
		}
	}
	return nil
}

func DecodeKeybindingsPatchParams(raw json.RawMessage) (KeybindingsPatchParams, error) {
	var params KeybindingsPatchParams
	if err := decodeKeybindingsStrictJSON(raw, &params); err != nil {
		return KeybindingsPatchParams{}, err
	}

	top, err := keybindingsObjectFields(raw)
	if err != nil {
		return KeybindingsPatchParams{}, err
	}
	if err := requireKeybindingsFields(top, "expectedRevision", "config"); err != nil {
		return KeybindingsPatchParams{}, err
	}
	if string(bytes.TrimSpace(top["expectedRevision"])) == "null" {
		return KeybindingsPatchParams{}, errors.New("expectedRevision must be an unsigned integer")
	}

	configFields, err := keybindingsObjectFields(top["config"])
	if err != nil {
		return KeybindingsPatchParams{}, fmt.Errorf("config: %w", err)
	}
	if err := requireKeybindingsFields(configFields, "version", "rules"); err != nil {
		return KeybindingsPatchParams{}, fmt.Errorf("config: %w", err)
	}
	if string(bytes.TrimSpace(configFields["version"])) == "null" {
		return KeybindingsPatchParams{}, errors.New("config: version must be an integer")
	}
	if string(bytes.TrimSpace(configFields["rules"])) == "null" {
		return KeybindingsPatchParams{}, errors.New("config: rules must be an array")
	}

	var rules []json.RawMessage
	if err := json.Unmarshal(configFields["rules"], &rules); err != nil {
		return KeybindingsPatchParams{}, fmt.Errorf("config: rules: %w", err)
	}
	for i, rawRule := range rules {
		ruleFields, err := keybindingsObjectFields(rawRule)
		if err != nil {
			return KeybindingsPatchParams{}, fmt.Errorf("rule %d: %w", i, err)
		}
		if err := requireKeybindingsFields(ruleFields, "action", "chord"); err != nil {
			return KeybindingsPatchParams{}, fmt.Errorf("rule %d: %w", i, err)
		}
		if string(bytes.TrimSpace(ruleFields["action"])) == "null" {
			return KeybindingsPatchParams{}, fmt.Errorf("rule %d: action must be a string", i)
		}
	}

	if err := ValidateKeybindingsConfig(params.Config); err != nil {
		return KeybindingsPatchParams{}, err
	}
	return params, nil
}

func decodeKeybindingsStrictJSON(raw []byte, dst any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(dst); err != nil {
		return fmt.Errorf("invalid keybindings patch: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return errors.New("invalid keybindings patch: trailing JSON value")
		}
		return fmt.Errorf("invalid keybindings patch: trailing JSON: %w", err)
	}
	return nil
}

func keybindingsObjectFields(raw json.RawMessage) (map[string]json.RawMessage, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return nil, err
	}
	if fields == nil {
		return nil, errors.New("expected JSON object")
	}
	return fields, nil
}

func requireKeybindingsFields(fields map[string]json.RawMessage, names ...string) error {
	for _, name := range names {
		if _, ok := fields[name]; !ok {
			return fmt.Errorf("missing required field %q", name)
		}
	}
	return nil
}
