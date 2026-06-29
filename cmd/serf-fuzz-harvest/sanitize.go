package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"strings"
)

// Sanitizer turns recorded traffic into committable seed bytes. Its default
// (shape-scrub) destroys every free-text and numeric leaf by construction —
// structure, framing, and a small allowlist of structural enum values survive,
// nothing else — so PII and secrets cannot leak. --keep-values opts out (local
// only, never committed) and relies on regex redaction + the abort gate instead.
//
// Every Process result passes through detectSecret, the airtight final gate: any
// high-confidence secret format or high-entropy token that survived aborts that
// seed (it is dropped, never written) and fails the whole run.
type Sanitizer struct {
	keepValues bool
}

// SecretLeakError is returned by Process when a secret survives sanitization.
// The seed is dropped and the harvester exits non-zero.
type SecretLeakError struct {
	Finding string
}

func (e *SecretLeakError) Error() string {
	return "secret survived sanitization: " + e.Finding
}

// Process sanitizes one payload. sse selects SSE framing (scrub each data: JSON
// payload, keep event:/comment/blank-line framing byte-for-byte) over plain JSON.
func (s *Sanitizer) Process(raw []byte, sse bool) ([]byte, error) {
	var out []byte
	var err error
	// Entropy quarantine only applies when real free-text survives (--keep-values).
	// Under shape-scrub the only strings left are placeholders, structural enum
	// values, and JSON keys — long snake_case identifiers, file paths, and ULIDs
	// that legitimately cross the entropy threshold — so there entropy would be
	// pure false-positive noise; the high-confidence regex gate is the protection.
	entropyCheck := s.keepValues
	switch {
	case s.keepValues:
		// Real values preserved; known secrets redacted in place. The abort gate
		// below is the backstop for anything redaction's format set misses.
		out = redactKnownSecrets(raw)
	case sse:
		if out, err = scrubSSE(raw); err != nil {
			return nil, err
		}
	default:
		if out, err = scrubJSON(raw); err != nil {
			return nil, err
		}
	}

	if finding := detectSecret(out, entropyCheck); finding != "" {
		return nil, &SecretLeakError{Finding: finding}
	}
	return out, nil
}

// enumKeys are the structural keys whose string values are framing the decoders
// branch on (not free-text content), so shape-scrub preserves them verbatim.
var enumKeys = map[string]bool{
	"type":          true,
	"role":          true,
	"finish_reason": true,
	"status":        true,
	"kind":          true,
	"object":        true,
	"event":         true,
}

// scrubJSON shape-scrubs a JSON payload: every key, type, array length, and
// nesting is preserved; each string leaf becomes a length-bucketed placeholder
// (except enum values) and each number leaf a same-kind zero sentinel.
func scrubJSON(raw []byte) ([]byte, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	return json.Marshal(scrubValue("", v))
}

// scrubJSONString shape-scrubs a JSON string and reports whether it parsed.
func scrubJSONString(s string) (string, bool) {
	out, err := scrubJSON([]byte(s))
	if err != nil {
		return "", false
	}
	return string(out), true
}

func scrubValue(key string, v any) any {
	switch val := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(val))
		for k, vv := range val {
			out[k] = scrubValue(k, vv)
		}
		return out
	case []any:
		out := make([]any, len(val))
		for i, vv := range val {
			// Array elements inherit the enclosing key's enum status so an array
			// like "content":[{"type":"text"}] keeps its enum leaves.
			out[i] = scrubValue(key, vv)
		}
		return out
	case string:
		if enumKeys[key] {
			return val
		}
		return placeholder(len(val))
	case json.Number:
		return scrubNumber(val)
	default:
		// bool, nil — structurally meaningful, kept as-is.
		return val
	}
}

// placeholder returns a synthetic string sized to its length bucket so inputs
// that differ only in free-text length collapse to one shape under dedup.
func placeholder(n int) string {
	switch {
	case n == 0:
		return ""
	case n <= 8:
		return strings.Repeat("x", 8)
	case n <= 64:
		return strings.Repeat("x", 64)
	default:
		return strings.Repeat("x", 512)
	}
}

// scrubNumber collapses a number leaf to a same-kind zero so the int/float
// distinction in the re-encoded JSON is preserved.
func scrubNumber(n json.Number) json.Number {
	if strings.ContainsAny(string(n), ".eE") {
		return json.Number("0.0")
	}
	return json.Number("0")
}

// scrubSSE rewrites an SSE stream, scrubbing each data: JSON payload while
// leaving event:, comment (:), blank-line, and [DONE] framing byte-for-byte.
func scrubSSE(raw []byte) ([]byte, error) {
	var out bytes.Buffer
	for _, seg := range splitAfterNewline(raw) {
		line, term := splitTerminator(seg)
		after, isData := strings.CutPrefix(line, "data:")
		if !isData {
			// event:, comments, blank lines, other fields — verbatim framing.
			out.WriteString(line)
			out.WriteString(term)
			continue
		}
		valTrim := strings.TrimSpace(after)
		prefix := "data:"
		if strings.HasPrefix(after, " ") {
			prefix = "data: "
		}
		switch valTrim {
		case "", "[DONE]":
			out.WriteString(line)
		default:
			if scrubbed, ok := scrubJSONString(valTrim); ok {
				out.WriteString(prefix + scrubbed)
			} else {
				out.WriteString(prefix + "scrubbed")
			}
		}
		out.WriteString(term)
	}
	return out.Bytes(), nil
}

// splitAfterNewline splits b into segments each ending in its newline (the last
// segment has none), so framing is reconstructed exactly.
func splitAfterNewline(b []byte) []string {
	s := string(b)
	var out []string
	for {
		i := strings.IndexByte(s, '\n')
		if i < 0 {
			if s != "" {
				out = append(out, s)
			}
			return out
		}
		out = append(out, s[:i+1])
		s = s[i+1:]
	}
}

// splitTerminator separates a segment into its content and trailing line
// terminator ("\r\n", "\n", or "").
func splitTerminator(seg string) (line, term string) {
	if strings.HasSuffix(seg, "\r\n") {
		return seg[:len(seg)-2], "\r\n"
	}
	if strings.HasSuffix(seg, "\n") {
		return seg[:len(seg)-1], "\n"
	}
	return seg, ""
}

// highConfidenceSecrets are distinctive secret formats whose prefixes cannot
// appear in a scrubbed placeholder, so the abort gate can flag them without
// false-positiving on sanitized output.
var highConfidenceSecrets = []*regexp.Regexp{
	regexp.MustCompile(`sk-ant-[A-Za-z0-9_-]{20,}`),
	regexp.MustCompile(`sk-[A-Za-z0-9_-]{20,}`),
	regexp.MustCompile(`AIza[0-9A-Za-z_-]{35}`),
	regexp.MustCompile(`AKIA[0-9A-Z]{16}`),
	regexp.MustCompile(`gh[pousr]_[A-Za-z0-9]{36}`),
	regexp.MustCompile(`github_pat_[A-Za-z0-9_]{22,}`),
	regexp.MustCompile(`xox[baprs]-[A-Za-z0-9-]{10,}`),
	regexp.MustCompile(`eyJ[A-Za-z0-9_-]{6,}\.[A-Za-z0-9_-]{6,}\.[A-Za-z0-9_-]{6,}`),
	regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY-----`),
}

// redactionExtras are value-preserving redactions used only under --keep-values:
// generic credential fields and emails, replaced rather than aborted because
// real values are being intentionally kept.
var redactionExtras = []*regexp.Regexp{
	regexp.MustCompile(`(?i)(authorization|x-api-key|api[_-]?key|bearer)["']?\s*[:=]\s*["']?[A-Za-z0-9._\-]{8,}`),
	regexp.MustCompile(`[A-Za-z0-9._%+\-]+@[A-Za-z0-9.\-]+\.[A-Za-z]{2,}`),
}

const redactedToken = "REDACTED"

// redactKnownSecrets replaces every known secret format and (keep-values)
// credential field/email with REDACTED.
func redactKnownSecrets(b []byte) []byte {
	for _, re := range highConfidenceSecrets {
		b = re.ReplaceAll(b, []byte(redactedToken))
	}
	for _, re := range redactionExtras {
		b = re.ReplaceAll(b, []byte(redactedToken))
	}
	return b
}

// detectSecret is the airtight abort gate: it reports the first high-confidence
// secret format in b (and, when entropyCheck is set, the first high-entropy
// token), or "" when clean. Entropy is checked only under --keep-values, where
// real free-text survives; shape-scrubbed output would only false-positive on
// preserved enum values and keys.
func detectSecret(b []byte, entropyCheck bool) string {
	for _, re := range highConfidenceSecrets {
		if loc := re.FindIndex(b); loc != nil {
			return "matched " + re.String()
		}
	}
	if entropyCheck {
		if tok := highEntropyToken(b); tok != "" {
			return fmt.Sprintf("high-entropy token %q", tok)
		}
	}
	return ""
}

const (
	entropyMinTokenLen = 20
	entropyThreshold   = 4.0
)

// highEntropyToken returns the first token (length >= entropyMinTokenLen) whose
// Shannon entropy exceeds entropyThreshold bits/char — a probable secret —, or
// "". Scrubbed placeholders ("xxxx…") have near-zero entropy, so this never
// fires on sanitized output.
func highEntropyToken(b []byte) string {
	for _, tok := range secretCharTokens(string(b)) {
		if len(tok) >= entropyMinTokenLen && shannonEntropy(tok) > entropyThreshold {
			return tok
		}
	}
	return ""
}

// secretCharTokens splits s on characters that cannot appear in a base64/url-safe
// token, so high-entropy keys are isolated for entropy scoring.
func secretCharTokens(s string) []string {
	return strings.FieldsFunc(s, func(r rune) bool {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			return false
		case r == '+' || r == '/' || r == '=' || r == '_' || r == '-':
			return false
		default:
			return true
		}
	})
}

func shannonEntropy(s string) float64 {
	if s == "" {
		return 0
	}
	var counts [256]int
	for i := 0; i < len(s); i++ {
		counts[s[i]]++
	}
	var h float64
	n := float64(len(s))
	for _, c := range counts {
		if c == 0 {
			continue
		}
		p := float64(c) / n
		h -= p * math.Log2(p)
	}
	return h
}
