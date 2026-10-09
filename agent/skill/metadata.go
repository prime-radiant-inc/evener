package skill

import (
	"bytes"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"primeradiant.com/evener/agent/internal/frontmatter"
)

const skillFrontmatterDelimiter = "---\n"

// utf8BOM is the byte-order mark an editor may leave at the very start of a file.
var utf8BOM = []byte{0xef, 0xbb, 0xbf}

// recoverMissingOpeningDelimiter accepts a skill source whose YAML frontmatter
// block is missing only its opening delimiter line. A skill written from
// scratch sometimes begins directly with "name: ..." and closes with "---",
// forgetting the leading line; this restores it so the block parses normally.
// It reports true only when the first non-blank line begins a plain mapping
// entry and the block before the closing "---" line parses as a YAML mapping
// carrying both a name and a description, so ordinary body text that merely
// contains a "---" rule is left alone. A leading UTF-8 BOM is trimmed either
// way, so the returned bytes never carry it into the parse.
func recoverMissingOpeningDelimiter(data []byte) ([]byte, bool) {
	data = bytes.TrimPrefix(data, utf8BOM)
	if bytes.HasPrefix(data, []byte(skillFrontmatterDelimiter)) {
		return data, false
	}
	if !looksLikeFrontmatterStart(data) {
		return data, false
	}
	normalized := append([]byte(skillFrontmatterDelimiter), data...)
	document, err := frontmatter.Parse(string(normalized))
	if err != nil || document.Meta == nil {
		return data, false
	}
	if !nonBlankString(document.Meta["name"]) || !nonBlankString(document.Meta["description"]) {
		return data, false
	}
	return normalized, true
}

// looksLikeFrontmatterStart reports whether data's first non-blank line begins a
// plain YAML mapping entry, the shape a delimiterless frontmatter block has. A
// Markdown heading, a list item, a bare "---", or prose does not qualify, so a
// body that merely contains a "---" rule is not mistaken for frontmatter.
func looksLikeFrontmatterStart(data []byte) bool {
	for len(data) > 0 {
		line := data
		if at := bytes.IndexByte(data, '\n'); at >= 0 {
			line, data = data[:at], data[at+1:]
		} else {
			data = nil
		}
		trimmed := bytes.TrimSpace(line)
		if len(trimmed) == 0 {
			continue
		}
		return trimmed[0] != '#' && bytes.ContainsRune(trimmed, ':')
	}
	return false
}

func nonBlankString(value any) bool {
	text, ok := value.(string)
	return ok && strings.TrimSpace(text) != ""
}

// InvocationControls governs how a skill may be advertised for invocation.
type InvocationControls struct {
	DisableModelInvocation bool `json:"disable_model_invocation"`
	UserInvocable          bool `json:"user_invocable"`
}

// Diagnostic describes a machine-readable problem or limitation associated with a skill.
type Diagnostic struct {
	Category    string `json:"category"`
	Name        string `json:"name,omitempty"`
	Source      string `json:"source"`
	OtherSource string `json:"other_source,omitempty"`
	Field       string `json:"field,omitempty"`
	Message     string `json:"message"`
}

// Descriptor is the complete discovery-time description of a skill.
type Descriptor struct {
	CatalogName string
	Meta        SkillMeta
	Controls    InvocationControls
	Unavailable bool
}

// Parse parses and validates discovery metadata from a SKILL.md file.
func Parse(data []byte, skillFile string) (Descriptor, []Diagnostic, error) {
	source, err := filepath.Abs(skillFile)
	if err != nil {
		diagnostic := Diagnostic{
			Category: "unreadable_source",
			Source:   skillFile,
			Message:  fmt.Sprintf("resolving skill source: %v", err),
		}
		return Descriptor{Unavailable: true}, []Diagnostic{diagnostic}, fmt.Errorf("resolving skill source: %w", err)
	}

	descriptor := Descriptor{
		Controls: InvocationControls{UserInvocable: true},
		Meta: SkillMeta{
			Dir:       filepath.Dir(source),
			SkillFile: source,
		},
		Unavailable: true,
	}
	invalidFrontmatter := func(message string, parseErr error) (Descriptor, []Diagnostic, error) {
		diagnostic := Diagnostic{
			Category: "invalid_frontmatter",
			Source:   source,
			Message:  message,
		}
		if parseErr == nil {
			parseErr = errors.New(message)
		}
		return descriptor, []Diagnostic{diagnostic}, parseErr
	}

	data, recovered := recoverMissingOpeningDelimiter(data)
	if !bytes.HasPrefix(data, []byte(skillFrontmatterDelimiter)) {
		return invalidFrontmatter("skill file requires YAML frontmatter", nil)
	}

	document, err := frontmatter.Parse(string(data))
	if err != nil {
		return invalidFrontmatter("skill frontmatter is not valid YAML", fmt.Errorf("parsing skill frontmatter: %w", err))
	}
	if document.Meta == nil {
		if document.Body == string(data) {
			// No whole-line closing delimiter: Parse returns the input verbatim.
			return invalidFrontmatter("skill frontmatter has no closing delimiter", nil)
		}
		// Framed, but the YAML decodes to null (e.g. "---\nnull\n---").
		return invalidFrontmatter("skill frontmatter has no metadata", nil)
	}
	descriptor.Meta.Metadata = cloneMetadata(document.Meta)

	name, ok := document.Meta["name"].(string)
	if !ok || strings.TrimSpace(name) == "" {
		diagnostic := Diagnostic{
			Category: "invalid_metadata",
			Source:   source,
			Field:    "name",
			Message:  "name requires a non-blank string",
		}
		return descriptor, []Diagnostic{diagnostic}, errors.New("skill name requires a non-blank string")
	}
	descriptor.Meta.Name = name

	var diagnostics []Diagnostic
	if recovered {
		diagnostics = append(diagnostics, Diagnostic{
			Category: "missing_frontmatter_delimiter",
			Name:     name,
			Source:   source,
			Message:  "skill frontmatter is missing its opening --- delimiter; the leading block was read as frontmatter",
		})
	}

	if !IsSlashAddressableName(name) {
		diagnostic := Diagnostic{
			Category: "invalid_metadata",
			Source:   source,
			Field:    "name",
			Message:  "name is not slash-addressable",
		}
		return descriptor, append(diagnostics, diagnostic), fmt.Errorf("skill name %q is not slash-addressable", name)
	}
	descriptor.CatalogName = name

	description, ok := document.Meta["description"].(string)
	if !ok || strings.TrimSpace(description) == "" {
		diagnostic := Diagnostic{
			Category: "invalid_metadata",
			Name:     name,
			Source:   source,
			Field:    "description",
			Message:  "description requires a non-blank string",
		}
		return descriptor, append(diagnostics, diagnostic), errors.New("skill description requires a non-blank string")
	}
	descriptor.Meta.Description = description

	var validationErrors []error

	disableModelInvocation, controlErr := parseControl(document.Meta, "disable-model-invocation", false)
	descriptor.Controls.DisableModelInvocation = disableModelInvocation
	if controlErr != nil {
		diagnostics = append(diagnostics, Diagnostic{
			Category: "invalid_control",
			Name:     name,
			Source:   source,
			Field:    "disable-model-invocation",
			Message:  controlErr.Error(),
		})
		validationErrors = append(validationErrors, controlErr)
	}

	userInvocable, controlErr := parseControl(document.Meta, "user-invocable", true)
	descriptor.Controls.UserInvocable = userInvocable
	if controlErr != nil {
		diagnostics = append(diagnostics, Diagnostic{
			Category: "invalid_control",
			Name:     name,
			Source:   source,
			Field:    "user-invocable",
			Message:  controlErr.Error(),
		})
		validationErrors = append(validationErrors, controlErr)
	}

	if value, present := document.Meta["allowed-tools"]; present {
		allowedTools, allowedErr := parseAllowedTools(value)
		if allowedErr != nil {
			diagnostics = append(diagnostics, Diagnostic{
				Category: "invalid_metadata",
				Name:     name,
				Source:   source,
				Field:    "allowed-tools",
				Message:  allowedErr.Error(),
			})
			validationErrors = append(validationErrors, allowedErr)
		} else {
			descriptor.Meta.AllowedTools = allowedTools
			diagnostics = append(diagnostics, Diagnostic{
				Category: "allowed_tools_not_enforced",
				Name:     name,
				Source:   source,
				Field:    "allowed-tools",
				Message:  "allowed-tools is preserved as metadata but does not grant or restrict tools",
			})
		}
	}

	if _, present := document.Meta["context"]; present {
		diagnostics = append(diagnostics, Diagnostic{
			Category: "unsupported_control",
			Name:     name,
			Source:   source,
			Field:    "context",
			Message:  "context is preserved as metadata but its behavior is unsupported",
		})
	}

	if len(validationErrors) != 0 {
		return descriptor, diagnostics, errors.Join(validationErrors...)
	}
	descriptor.Unavailable = false
	return descriptor, diagnostics, nil
}

func parseControl(meta map[string]any, field string, fallback bool) (bool, error) {
	value, present := meta[field]
	if !present {
		return fallback, nil
	}
	result, ok := value.(bool)
	if !ok {
		return false, fmt.Errorf("%s requires a boolean", field)
	}
	return result, nil
}

func parseAllowedTools(value any) ([]string, error) {
	switch value := value.(type) {
	case string:
		return []string{value}, nil
	case []any:
		tools := make([]string, len(value))
		for i, item := range value {
			tool, ok := item.(string)
			if !ok {
				return nil, errors.New("allowed-tools requires a string or an array of strings")
			}
			tools[i] = tool
		}
		return tools, nil
	default:
		return nil, errors.New("allowed-tools requires a string or an array of strings")
	}
}

func cloneMetadata(metadata map[string]any) map[string]any {
	if metadata == nil {
		return nil
	}
	cloned := make(map[string]any, len(metadata))
	for key, value := range metadata {
		cloned[key] = cloneMetadataValue(value)
	}
	return cloned
}

func cloneMetadataValue(value any) any {
	switch value := value.(type) {
	case map[string]any:
		return cloneMetadata(value)
	case map[any]any:
		// YAML mappings with non-string keys retain their decoded key types.
		cloned := make(map[any]any, len(value))
		for key, item := range value {
			cloned[key] = cloneMetadataValue(item)
		}
		return cloned
	case []any:
		cloned := make([]any, len(value))
		for i, item := range value {
			cloned[i] = cloneMetadataValue(item)
		}
		return cloned
	case []string:
		return append([]string(nil), value...)
	default:
		return value
	}
}
