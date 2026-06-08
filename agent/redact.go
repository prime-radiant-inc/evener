package agent

import (
	"regexp"
	"strings"
)

// redactMode selects how aggressively redact() masks a string.
type redactMode int

const (
	// redactNone returns the input unchanged. It is gated by the caller (an
	// explicit debug/unsafe opt-in) and is never the default.
	redactNone redactMode = iota
	// redactStandard masks credentials, tokens, authorization/cookie headers, and
	// credential-looking env/assignment values, keeping keys and header names
	// legible.
	redactStandard
	// redactStrict does everything redactStandard does and additionally elides
	// long opaque high-entropy blobs (base64-ish keys, encoded provider bodies,
	// raw JSONL payloads) that carry no KEY= or header anchor.
	redactStrict
)

// redactMarker replaces a masked secret. It is deliberately distinctive so a
// reader can tell redaction ran and so it never collides with real content.
const redactMarker = "«redacted»"

// redactRule is one entry in a redaction table: a compiled pattern whose first
// submatch group is the legible prefix to keep (key name, header name, scheme),
// with the remainder of the match replaced by redactMarker.
type redactRule struct {
	re *regexp.Regexp
}

// sensitiveKeyAlt is the alternation of credential-looking assignment/header key
// names. Used by both the header-colon and the env/assignment rules so the two
// forms stay in lockstep.
const sensitiveKeyAlt = `(?:api[_-]?key|secret|token|password|passwd|access[_-]?key|aws_secret_access_key|private[_-]?key|client[_-]?secret)`

// standardRules masks the documented credential classes. Each rule keeps group 1
// (the key/header name and its separator) and replaces the value with the marker.
// Order matters: specific header rules precede the generic assignment rule, and
// the standalone sk- token rule is independent of any anchor.
var standardRules = []redactRule{
	// Authorization: Bearer <tok> / Authorization: <tok> (also Proxy-Authorization).
	{regexp.MustCompile(`(?i)((?:Proxy-)?Authorization:\s*(?:Bearer\s+|Basic\s+|Token\s+)?)\S+`)},
	// Cookie / Set-Cookie: mask the whole value run (to end of line).
	{regexp.MustCompile(`(?i)((?:Set-)?Cookie:\s*).+`)},
	// Any *-Key / X-Api-Key style header: mask the value run.
	{regexp.MustCompile(`(?i)((?:[A-Za-z0-9-]*-)?(?:Api[_-]?)?Key:\s*).+`)},
	// Standalone provider API keys: sk-..., sk-ant-..., and similar long tokens.
	{regexp.MustCompile(`(?i)\b(sk-)[A-Za-z0-9_-]{8,}`)},
	// KEY=VALUE / KEY: VALUE assignment where KEY is credential-looking. Masks an
	// optionally-quoted value up to the next whitespace/quote.
	{regexp.MustCompile(`(?i)\b(` + sensitiveKeyAlt + `)(\s*[:=]\s*)"?[^"\s]+"?`)},
}

// strictExtraRules run AFTER the standard table under redactStrict. They redact
// long opaque/high-entropy runs that have no key/header anchor: base64-ish keys,
// encoded reasoning, and raw JSONL provider bodies. The length floor keeps prose
// words intact while catching credential-grade blobs.
var strictExtraRules = []redactRule{
	// A long unbroken base64/hex/token-ish run (40+ chars). No capture group, so the
	// whole run is replaced by the marker.
	{regexp.MustCompile(`[A-Za-z0-9+/=_-]{40,}`)},
	// A long double-quoted argument/body (60+ inner chars): keep the opening quote,
	// replace the inner body (and closing quote) with the marker.
	{regexp.MustCompile(`(")[^"]{60,}"`)},
}

// redact masks credential material in s according to mode. redactNone returns s
// unchanged; redactStandard applies the standard table; redactStrict applies the
// standard table and then the stricter opaque-blob rules (a strict superset).
func redact(s string, mode redactMode) string {
	switch mode {
	case redactNone:
		return s
	case redactStrict:
		s = applyRules(s, standardRules)
		return applyRules(s, strictExtraRules)
	default: // redactStandard
		return applyRules(s, standardRules)
	}
}

// applyRules masks every rule match. The kept prefix is the concatenation of the
// rule's capture groups (key name, separator, scheme, opening quote — whatever the
// rule chose to preserve for legibility); the marker is appended after it. A rule
// with no capture group replaces the whole match with the marker.
func applyRules(s string, rules []redactRule) string {
	for _, rule := range rules {
		s = rule.re.ReplaceAllStringFunc(s, func(match string) string {
			groups := rule.re.FindStringSubmatch(match)
			// Keep the rule's capture groups (legible prefix), then the marker.
			return strings.Join(groups[1:], "") + redactMarker
		})
	}
	return s
}
