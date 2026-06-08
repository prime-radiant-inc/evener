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

// sensitiveWord is the alternation of credential-looking words. It matches a word
// like "token" or "api_key" but NOT a larger identifier that merely contains it as
// a substring ("tokenizer", "keyboard"): the callers below bound it to a complete
// segment of the surrounding key, never an arbitrary substring.
const sensitiveWord = `(?:api[_-]?key|secret|token|password|passwd|access[_-]?key|private[_-]?key|client[_-]?secret|passphrase|credentials?|refresh[_-]?token|authorization)`

// sensitiveKeyRun matches a full assignment/JSON key (a run of [A-Za-z0-9_-]) that
// contains sensitiveWord as a COMPLETE _/-/boundary-delimited segment. The leading
// `(?:[A-Za-z0-9]+[_-])*` and trailing `(?:[_-][A-Za-z0-9]+)*` consume any sibling
// segments (the `OPENAI_`/`_ID` in `OPENAI_API_KEY`/`AWS_ACCESS_KEY_ID`), so a
// prefix or suffix never breaks the match — while still requiring the sensitive
// word to be its own segment. RE2 has no lookaround, so this structural framing,
// not a lookahead, is what excludes substrings like "token" in "tokenizer".
const sensitiveKeyRun = `(?:[A-Za-z0-9]+[_-])*` + sensitiveWord + `(?:[_-][A-Za-z0-9]+)*`

// standardRules masks the documented credential classes. Each rule keeps its
// capture groups (key/header name, separator, scheme — whatever is legible) and
// replaces the secret with the marker. Order matters: specific header rules and
// the structured key/JSON/URL rules precede the anchorless bare-token rules.
var standardRules = []redactRule{
	// Authorization: Bearer <tok> / Authorization: <tok> (also Proxy-Authorization).
	{regexp.MustCompile(`(?i)((?:Proxy-)?Authorization:\s*(?:Bearer\s+|Basic\s+|Token\s+)?)\S+`)},
	// Cookie / Set-Cookie: mask the whole value run (to end of line).
	{regexp.MustCompile(`(?i)((?:Set-)?Cookie:\s*).+`)},
	// Any *-Key / X-Api-Key style header: mask the value run.
	{regexp.MustCompile(`(?i)((?:[A-Za-z0-9-]*-)?(?:Api[_-]?)?Key:\s*).+`)},
	// JSON object form: "<key>":"<value>" / "<key>": "<value>" where the key contains
	// a sensitive segment. Keep `"key":"`, mask the value to the closing quote. The
	// quotes are `\\?"` so the rule also matches when this JSON is itself a value inside
	// an outer JSON string: the result snapshot is JSON-marshalled before redaction, so
	// a child's embedded JSON arrives doubly-escaped (`"` → `\"`). The value body
	// `(?:[^"\\]|\\.)*` spans escaped characters but halts at the closing quote. The
	// optional `x-` covers header-style JSON keys ("x-api-key"). Precedes the KEY=VALUE
	// rule so quoted JSON values are handled by their own quote-aware form.
	{regexp.MustCompile(`(?i)(\\?"(?:x-)?` + sensitiveKeyRun + `\\?"\s*:\s*\\?")(?:[^"\\]|\\.)*`)},
	// KEY=VALUE / KEY: VALUE assignment (env dump, config line) where KEY contains a
	// sensitive segment with any prefix/suffix. Group 1 preserves the boundary (start
	// of string/line or a non-key char) so it is not consumed; group 2 the key; group 3
	// the separator. Masks an optionally-quoted value up to the next whitespace, quote,
	// comma, OR backslash. The backslash stop is load-bearing: the snapshot is
	// JSON-marshalled, so a real newline between env lines is the two chars `\`+`n`;
	// stopping at `\` masks exactly one value instead of swallowing the next line's key.
	{regexp.MustCompile(`(?i)(^|[^A-Za-z0-9_-])(` + sensitiveKeyRun + `)(\s*[:=]\s*)"?[^"\s,\\]+`)},
	// URL-embedded password: scheme://user:PASSWORD@host. Keep `scheme://user:`,
	// mask just the password between `:` and `@`, keep `@` and the host legible.
	{regexp.MustCompile(`([a-zA-Z][a-zA-Z0-9+.\-]*://[^/\s:@]+:)[^/\s:@]+(@)`)},
	// Standalone provider API keys: sk-..., sk-ant-..., and similar long tokens.
	{regexp.MustCompile(`(?i)\b(sk-)[A-Za-z0-9_-]{8,}`)},
	// GitHub PATs/tokens: ghp_/gho_/ghu_/ghs_/ghr_/github_pat_ prefix + body.
	{regexp.MustCompile(`\b(ghp_|gho_|ghu_|ghs_|ghr_|github_pat_)[A-Za-z0-9_]{8,}`)},
	// Slack tokens: xoxb-/xoxa-/xoxp-/xoxr-/xoxs- prefix + body.
	{regexp.MustCompile(`\b(xox[baprs]-)[A-Za-z0-9-]{8,}`)},
	// AWS access key id: AKIA + 16 uppercase-alphanumeric. Keep the AKIA prefix.
	{regexp.MustCompile(`\b(AKIA)[0-9A-Z]{16}\b`)},
	// Google API key: AIza + 35+ url-safe chars. Keep the AIza prefix.
	{regexp.MustCompile(`\b(AIza)[0-9A-Za-z_\-]{35,}`)},
	// JWT: header.payload.signature of base64url segments. Keep the (algorithm) header
	// segment legible; mask the payload and signature, which carry claims and the key.
	{regexp.MustCompile(`\b(eyJ[A-Za-z0-9_\-]+)\.[A-Za-z0-9_\-]+\.[A-Za-z0-9_\-]+`)},
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
