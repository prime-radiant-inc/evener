package skill

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"

	"primeradiant.com/evener/agent/internal/frontmatter"
)

// LoadedSkill is one validated source version of a discovered skill.
type LoadedSkill struct {
	Descriptor Descriptor
	Body       string
	Digest     string
}

// RenderedSkill is a complete model-facing skill document and its digest.
type RenderedSkill struct {
	Content string
	Digest  string
}

// SkillDocument is the data-safe representation rendered inside skill-context.
type SkillDocument struct {
	Name          string `json:"name"`
	Description   string `json:"description"`
	Source        string `json:"source"`
	BaseDirectory string `json:"base_directory"`
	Instructions  string `json:"instructions"`
}

// Load reads and validates the exact source recorded by d.
func Load(d Descriptor) (LoadedSkill, []Diagnostic, error) {
	return load(d, true)
}

func load(d Descriptor, verifyDeclaredName bool) (LoadedSkill, []Diagnostic, error) {
	data, err := os.ReadFile(d.Meta.SkillFile)
	if err != nil {
		diagnostic := Diagnostic{
			Category: "unreadable_source",
			Name:     d.CatalogName,
			Source:   d.Meta.SkillFile,
			Message:  fmt.Sprintf("reading skill source: %v", err),
		}
		return LoadedSkill{}, []Diagnostic{diagnostic}, fmt.Errorf("reading skill source: %w", err)
	}

	current, diagnostics, err := Parse(data, d.Meta.SkillFile)
	if err != nil {
		return LoadedSkill{}, diagnostics, err
	}
	if verifyDeclaredName && current.Meta.Name != d.Meta.Name {
		diagnostic := Diagnostic{
			Category: "source_identity_changed",
			Name:     d.CatalogName,
			Source:   d.Meta.SkillFile,
			Field:    "name",
			Message:  fmt.Sprintf("declared skill name changed from %q to %q", d.Meta.Name, current.Meta.Name),
		}
		return LoadedSkill{}, append(diagnostics, diagnostic), fmt.Errorf("skill source identity changed from %q to %q", d.Meta.Name, current.Meta.Name)
	}

	document, err := frontmatter.Parse(string(data))
	if err != nil {
		return LoadedSkill{}, diagnostics, fmt.Errorf("parsing skill frontmatter: %w", err)
	}
	current.CatalogName = d.CatalogName
	digest := sha256.Sum256(data)
	return LoadedSkill{
		Descriptor: current,
		Body:       document.Body,
		Digest:     hex.EncodeToString(digest[:]),
	}, diagnostics, nil
}

// Render encodes a loaded skill without interpreting its instruction body.
func Render(s LoadedSkill) RenderedSkill {
	document := SkillDocument{
		Name:          s.Descriptor.CatalogName,
		Description:   s.Descriptor.Meta.Description,
		Source:        s.Descriptor.Meta.SkillFile,
		BaseDirectory: s.Descriptor.Meta.Dir,
		Instructions:  s.Body,
	}
	encoded, _ := json.Marshal(document)
	content := "<skill-context>\n" + string(encoded) + "\n</skill-context>"
	digest := sha256.Sum256([]byte(content))
	return RenderedSkill{Content: content, Digest: hex.EncodeToString(digest[:])}
}
