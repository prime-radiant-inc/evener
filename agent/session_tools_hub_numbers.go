package agent

import (
	"bytes"
	"encoding/json"
	"maps"
)

// Only the fence revision and pending-page bounds bypass float64. All ordinary
// arguments retain the default decoder's values, types and JSON error handling.
func unmarshalManagerQuestionArguments(raw []byte, args *map[string]any) error {
	var fields, hubFields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return json.Unmarshal(raw, args)
	}
	_ = json.Unmarshal(fields["hub"], &hubFields)
	_, fenced := fields["expected_ask_revision"]
	page := false
	numericPageFields := [...]string{"expected_ask_revision", "offset", "limit"}
	for _, key := range numericPageFields {
		if _, present := hubFields[key]; present {
			page = true
			hubFields[key] = json.RawMessage("null")
		}
	}
	if !fenced && !page {
		return json.Unmarshal(raw, args)
	}
	// Selected values must not visit float64, even when they exceed its range.
	// The default view still judges every ordinary field with encoding/json.
	if fenced {
		fields["expected_ask_revision"] = json.RawMessage("null")
	}
	if page {
		hubRaw, err := json.Marshal(hubFields)
		if err != nil {
			return err
		}
		fields["hub"] = hubRaw
	}
	ordinaryRaw, err := json.Marshal(fields)
	if err != nil {
		return err
	}
	var ordinary map[string]any
	if err := json.Unmarshal(ordinaryRaw, &ordinary); err != nil {
		// Keep the untouched input's diagnostic offsets and partial argument
		// view when an unrelated field fails ordinary decoding.
		return json.Unmarshal(raw, args)
	}
	var exact map[string]any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&exact); err != nil {
		return err
	}
	if fenced {
		ordinary["expected_ask_revision"] = exact["expected_ask_revision"]
	}
	hub, _ := ordinary["hub"].(map[string]any)
	exactHub, _ := exact["hub"].(map[string]any)
	for _, key := range numericPageFields {
		if value, present := exactHub[key]; present {
			hub[key] = value
		}
	}
	if *args == nil {
		*args = ordinary
	} else {
		maps.Copy(*args, ordinary)
	}
	return nil
}
