package agent

import (
	"bytes"
	"encoding/json"
)

// Only the fence revision and pending-page bounds bypass float64. All ordinary
// arguments retain the default decoder's values, types and JSON error handling.
func unmarshalManagerQuestionArguments(raw []byte, args *map[string]any) error {
	if err := json.Unmarshal(raw, args); err != nil {
		return err
	}
	hub, _ := (*args)["hub"].(map[string]any)
	_, fenced := (*args)["expected_ask_revision"]
	page := false
	numericPageFields := [...]string{"expected_ask_revision", "offset", "limit"}
	for _, key := range numericPageFields {
		if _, present := hub[key]; present {
			page = true
			break
		}
	}
	if !fenced && !page {
		return nil
	}
	var exact map[string]any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&exact); err != nil {
		return err
	}
	if fenced {
		(*args)["expected_ask_revision"] = exact["expected_ask_revision"]
	}
	exactHub, _ := exact["hub"].(map[string]any)
	for _, key := range numericPageFields {
		if value, present := exactHub[key]; present {
			hub[key] = value
		}
	}
	return nil
}
