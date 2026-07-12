package main

import "testing"

func FuzzNamingConversions(f *testing.F) {
	f.Add("workingDir", "internal/example.go")
	f.Add("working_dir", "appwire/example.go")
	f.Add("-", "llm/providers/example/wire.go")
	f.Fuzz(func(t *testing.T, tag, path string) {
		if len(tag)+len(path) > 4096 {
			return
		}
		_, _ = tagKey(tag)
		_ = checkJSONTag(tag, path)
		_ = checkTOMLTag(tag)
		_ = toCamelCase(tag)
		_ = toKebabCase(tag)
		_ = toSnakeCase(tag)
		_ = isExcluded(path)
	})
}
