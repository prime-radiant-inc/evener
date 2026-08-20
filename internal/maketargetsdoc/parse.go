// Package maketargetsdoc parses the "## " target-annotation comment blocks
// that sit directly above rules in make/*.mk, so the doc generator and
// `make help` share one implementation of the grammar rather than two that
// can drift apart.
//
// The grammar is defined in
// docs/superpowers/specs/2026-08-20-makefile-and-docs-decomposition-design.md
// §2, and is authoritative over the summary in this file's comments.
package maketargetsdoc

import (
	"fmt"
	"regexp"
	"strings"
)

// Target is one annotated rule: a required one-line summary plus four
// optional structured fields. The doc generator and `make help` both build
// directly on this shape.
type Target struct {
	Name      string
	Summary   string
	Proves    string
	Trigger   string
	Requires  string
	FailsWhen string
}

// fieldAttempt matches a "## " line's content when it OPENS with a
// lowercase, hyphenated key immediately followed by a colon — the shape a
// structured field takes, whether or not what follows the colon is
// well-formed. The discrimination is on that leading key-shaped prefix, not
// on the presence of a colon anywhere in the line: a summary like "Build
// the runtime pair: evener and evener-hub." starts with an uppercase word
// and spaces, so it never matches this pattern and stays a summary.
//
// Anything matching here is treated as an attempted field, valid or not:
// the caller checks the key against the four real ones (else "unknown
// key"), and then that a space immediately follows the colon (else
// malformed — this is what catches both "## trigger:" with nothing after
// it and "## trigger:value" with no space, rather than letting either
// silently become summary prose with the field left empty).
var fieldAttempt = regexp.MustCompile(`^([a-z][a-z0-9-]*):(.*)$`)

// targetSpecificVariable matches the remainder of a rule-shaped line
// (`name: <remainder>`) when <remainder> is itself a variable assignment —
// `PREFIX := $(HOME)/.local` rather than a prerequisite list. This is the
// `install-home: PREFIX := ...` shape from make/building.mk: syntactically a
// rule line, but not the one the annotation block is allowed to attach to.
var targetSpecificVariable = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*\s*(:=|\+=|\?=|!=|=)`)

// block accumulates one pending "##" annotation until it either attaches to
// a rule line or a grammar violation ends parsing.
type block struct {
	Target
	startLine int             // for the "dangling block" error at EOF
	lastKey   string          // "" means the summary is still the active accumulator
	inFields  bool            // true once the leading summary run has ended
	seenKeys  map[string]bool // duplicate-field detection
}

// fieldPtr returns the Target field a key writes to, or nil for a key that
// isn't one of the four the grammar defines.
func (b *block) fieldPtr(key string) *string {
	switch key {
	case "proves":
		return &b.Proves
	case "trigger":
		return &b.Trigger
	case "requires":
		return &b.Requires
	case "fails-when":
		return &b.FailsWhen
	default:
		return nil
	}
}

// appendToCurrent joins text onto whichever accumulator is active — the last
// field opened, or the summary if no field has been opened yet — with a
// single space, per spec §2's continuation rule.
func (b *block) appendToCurrent(text string) {
	dst := &b.Summary
	if b.lastKey != "" {
		dst = b.fieldPtr(b.lastKey)
	}
	if *dst == "" {
		*dst = text
	} else {
		*dst = *dst + " " + text
	}
}

// ParseFamily parses one make/*.mk family file's contents and returns its
// annotated targets in file order. Every rule in src must carry a
// contiguous "##" block with at least a summary line; any other shape
// (an unknown key, a block over a target-specific variable line, a block
// separated from its rule, a rule with no block at all) is an error rather
// than a silently wrong or missing entry.
func ParseFamily(src []byte) ([]Target, error) {
	var targets []Target
	var pending *block
	lineNo := 0

	for line := range strings.Lines(string(src)) {
		lineNo++
		line = strings.TrimRight(line, "\r\n")

		switch {
		case strings.HasPrefix(line, "##"):
			b, err := accumulate(pending, line, lineNo)
			if err != nil {
				return nil, err
			}
			pending = b

		case strings.TrimSpace(line) == "":
			if pending != nil {
				return nil, fmt.Errorf("line %d: blank line separates the ## block (opened at line %d) from its rule; a block must be contiguous with the rule it documents", lineNo, pending.startLine)
			}

		default:
			name, rest, isRuleShape := ruleShape(line)
			switch {
			case !isRuleShape:
				// A plain "#" comment (anything here that isn't "##", which
				// is routed to the case above) is still a comment for
				// contiguity purposes: spec §2 only breaks a pending block
				// on a non-comment line. Real family files carry multi-line
				// "#" rationale directly above the rule, and a "##" summary
				// placed above that rationale must not be forced to move.
				if pending != nil && !strings.HasPrefix(line, "#") {
					return nil, fmt.Errorf("line %d: %q separates the ## block (opened at line %d) from its rule; a block must be contiguous with the rule it documents", lineNo, line, pending.startLine)
				}

			case strings.HasPrefix(name, "."):
				// A directive such as .PHONY: never carries an annotation.
				if pending != nil {
					return nil, fmt.Errorf("line %d: %q is a directive, not a rule; the ## block opened at line %d cannot attach to it", lineNo, line, pending.startLine)
				}

			case targetSpecificVariable.MatchString(rest):
				// e.g. `install-home: PREFIX := $(HOME)/.local`: syntactically
				// a rule line, but it carries a target-specific variable, not
				// the prerequisites/recipe the annotation documents.
				if pending != nil {
					return nil, fmt.Errorf("line %d: %q is a target-specific variable line, not the rule; move the ## block (opened at line %d) to sit directly above the line that carries %s's prerequisites/recipe", lineNo, line, pending.startLine, name)
				}

			default:
				if pending == nil {
					return nil, fmt.Errorf("line %d: target %q has no ## annotation block above it", lineNo, name)
				}
				if pending.Summary == "" {
					return nil, fmt.Errorf("line %d: target %q's ## block (opened at line %d) has no summary line; a summary is required even when structured fields are present", lineNo, name, pending.startLine)
				}
				t := pending.Target
				t.Name = name
				targets = append(targets, t)
				pending = nil
			}
		}
	}

	if pending != nil {
		return nil, fmt.Errorf("## block at line %d is never attached to a rule", pending.startLine)
	}
	return targets, nil
}

// accumulate folds one "##"-prefixed line into pending, allocating a new
// block if this is the line that opens it, and returns the (possibly new)
// pending block.
func accumulate(pending *block, line string, lineNo int) (*block, error) {
	rest := line[len("##"):]

	var content string
	var continuation bool
	switch {
	case len(rest) >= 3 && rest[:3] == "   " && (len(rest) == 3 || rest[3] != ' '):
		content, continuation = rest[3:], true
	case len(rest) >= 1 && rest[0] == ' ' && (len(rest) == 1 || rest[1] != ' '):
		content = rest[1:]
	default:
		return nil, fmt.Errorf("line %d: %q is not a valid ## line; content must start with exactly one space (a summary or field line) or exactly three spaces (a continuation)", lineNo, line)
	}

	if continuation {
		if pending == nil {
			return nil, fmt.Errorf("line %d: continuation line %q has nothing to continue; it must follow a summary or field line already open in the same block", lineNo, line)
		}
		pending.appendToCurrent(content)
		return pending, nil
	}

	if pending == nil {
		pending = &block{startLine: lineNo}
	}

	if m := fieldAttempt.FindStringSubmatch(content); m != nil {
		key, afterColon := m[1], m[2]
		if pending.fieldPtr(key) == nil {
			return nil, fmt.Errorf("line %d: unknown annotation key %q; valid keys are proves, trigger, requires, fails-when", lineNo, key)
		}
		if !strings.HasPrefix(afterColon, " ") {
			return nil, fmt.Errorf("line %d: field %q's colon must be followed by a space, even for an empty value; write \"## %s: <value>\" or \"## %s: \"", lineNo, key, key, key)
		}
		value := afterColon[1:]
		if pending.seenKeys[key] {
			return nil, fmt.Errorf("line %d: annotation key %q appears twice in the same ## block (opened at line %d)", lineNo, key, pending.startLine)
		}
		if pending.seenKeys == nil {
			pending.seenKeys = map[string]bool{}
		}
		pending.seenKeys[key] = true
		pending.inFields = true
		pending.lastKey = key
		*pending.fieldPtr(key) = value
		return pending, nil
	}

	if pending.inFields {
		return nil, fmt.Errorf("line %d: %q looks like summary prose but appears after a field line; the summary must be the leading run of ## lines, before any key: field", lineNo, line)
	}
	pending.lastKey = ""
	pending.appendToCurrent(content)
	return pending, nil
}

// ruleShape reports whether line could be a target/rule declaration: it
// starts at column zero (not a recipe, comment, or continuation), and its
// text up to the first ':' is a single bare identifier. This mirrors the
// predicate makefiletargets_audit_test.go uses to find rule lines across
// make/*.mk, so both readings of "what is a rule line" stay in step.
//
// rest is the (trimmed) text after that colon; the caller still has to rule
// out a target-specific variable line and a directive like .PHONY before
// treating name as an annotatable rule.
func ruleShape(line string) (name, rest string, ok bool) {
	if line == "" || line[0] == '\t' || line[0] == ' ' || line[0] == '#' {
		return "", "", false
	}
	name, rest, cut := strings.Cut(line, ":")
	if !cut || strings.HasPrefix(rest, "=") || strings.ContainsAny(name, " \t") || name == "" {
		return "", "", false
	}
	return name, strings.TrimSpace(rest), true
}
