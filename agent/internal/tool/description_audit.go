package tool

import (
	"maps"
	"slices"

	"primeradiant.com/evener/llm"
)

// UndocumentedProperties lists every property path in td's parameter schema
// that carries no description, nested object and array-item properties
// included. Paths are dotted ("questions[].options[].label"); an empty slice
// means every property documents itself.
//
// It backs the description gates that keep model-facing tool schemas
// self-describing: a bare parameter name banks on the model's training prior
// naming it the way we do, and the harvested FuzzToolArgsValidate corpus
// records where that fails (a real grep call shaped like Claude Code's
// schema: -i, head_limit).
func UndocumentedProperties(td llm.ToolDefinition) []string {
	var missing []string
	var walk func(prefix string, schema map[string]any)
	walk = func(prefix string, schema map[string]any) {
		props, ok := schema["properties"].(map[string]any)
		if !ok {
			return
		}
		for _, name := range slices.Sorted(maps.Keys(props)) {
			pm, ok := props[name].(map[string]any)
			if !ok {
				continue
			}
			path := prefix + name
			if desc, _ := pm["description"].(string); desc == "" {
				missing = append(missing, path)
			}
			walk(path+".", pm)
			if items, ok := pm["items"].(map[string]any); ok {
				walk(path+"[].", items)
			}
		}
	}
	walk("", td.Parameters)
	return missing
}
