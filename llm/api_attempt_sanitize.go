package llm

import (
	"encoding/json"
	"net/http"
	"net/textproto"
	"net/url"
	"sort"
	"strings"

	apilog "primeradiant.com/serf/llm/apilog"
)

var commonCredentialHeaderNames = map[string]struct{}{
	"Api-Key":             {},
	"Authorization":       {},
	"Cookie":              {},
	"Proxy-Authorization": {},
	"X-Api-Key":           {},
	"X-Auth-Token":        {},
	"X-Goog-Api-Key":      {},
}

var commonCredentialQueryNames = map[string]struct{}{
	"access_token": {},
	"api_key":      {},
	"apikey":       {},
	"key":          {},
	"token":        {},
}

// NewAPILogCredentialMaterial records the provider/config-derived names and
// values that must be removed before request metadata or errors are persisted.
func NewAPILogCredentialMaterial(headerNames, queryNames []string, values ...string) APILogCredentialMaterial {
	material := APILogCredentialMaterial{}
	secretNames := make(map[string]struct{})
	for _, name := range headerNames {
		canonical := canonicalCredentialHeaderName(name)
		if canonical == "" {
			continue
		}
		if material.HeaderNames == nil {
			material.HeaderNames = make(map[string]struct{})
		}
		material.HeaderNames[canonical] = struct{}{}
		if _, structural := commonCredentialHeaderNames[canonical]; !structural {
			addCredentialSecretNames(secretNames, strings.ToLower(strings.TrimSpace(name)), strings.ToLower(canonical))
		}
	}
	for _, name := range queryNames {
		canonical := canonicalCredentialQueryName(name)
		if canonical == "" {
			continue
		}
		if material.QueryNames == nil {
			material.QueryNames = make(map[string]struct{})
		}
		material.QueryNames[canonical] = struct{}{}
		if _, structural := commonCredentialQueryNames[canonical]; !structural {
			addCredentialSecretNames(secretNames, strings.ToLower(strings.TrimSpace(name)), strings.ToLower(canonical))
		}
	}
	seenValues := make(map[string]struct{}, len(values))
	for _, value := range values {
		if value == "" {
			continue
		}
		if _, duplicate := seenValues[value]; duplicate {
			continue
		}
		seenValues[value] = struct{}{}
		material.Values = append(material.Values, value)
	}
	rawSecretNames := make([]string, 0, len(secretNames))
	for name := range secretNames {
		rawSecretNames = append(rawSecretNames, name)
	}
	material.secretNames = caseFoldedCredentialNameVariants(rawSecretNames)
	material.patterns = credentialValueVariants(material.Values)
	return material
}

func addCredentialSecretNames(names map[string]struct{}, values ...string) {
	for _, value := range values {
		if value != "" {
			names[value] = struct{}{}
		}
	}
}

// APILogCredentialMaterialForRequest incorporates credential values from the
// final request handed to RoundTrip. Redirect hooks may replace configured
// credential headers or add credential query parameters after an adapter has
// built its static credential material.
func APILogCredentialMaterialForRequest(req *http.Request, configured APILogCredentialMaterial) APILogCredentialMaterial {
	headerNames := make([]string, 0, len(configured.HeaderNames))
	for name := range configured.HeaderNames {
		headerNames = append(headerNames, name)
	}
	queryNames := make([]string, 0, len(configured.QueryNames))
	for name := range configured.QueryNames {
		queryNames = append(queryNames, name)
	}
	values := append([]string(nil), configured.Values...)
	if req == nil {
		return NewAPILogCredentialMaterial(headerNames, queryNames, values...)
	}
	appendCredentialHeaders := func(headers http.Header) {
		for name, requestValues := range headers {
			if !credentialHeaderName(name, configured) {
				continue
			}
			headerNames = append(headerNames, name)
			for _, value := range requestValues {
				values = append(values, value)
				values = append(values, structuredCredentialHeaderValues(name, value)...)
			}
		}
	}
	appendCredentialHeaders(req.Header)
	appendCredentialHeaders(req.Trailer)
	if req.URL != nil {
		for part := range strings.SplitSeq(req.URL.RawQuery, "&") {
			rawName, rawValue, _ := strings.Cut(part, "=")
			name := unescapeQueryComponent(rawName)
			if !credentialQueryName(name, configured) {
				continue
			}
			queryNames = append(queryNames, name)
			if rawName != name {
				queryNames = append(queryNames, rawName)
			}
			values = append(values, rawValue, unescapeQueryComponent(rawValue))
		}
		if req.URL.User != nil {
			values = append(values, req.URL.User.Username())
			if password, ok := req.URL.User.Password(); ok {
				values = append(values, password)
			}
		}
	}
	return NewAPILogCredentialMaterial(headerNames, queryNames, values...)
}

func structuredCredentialHeaderValues(name, value string) []string {
	switch canonicalCredentialHeaderName(name) {
	case "Authorization", "Proxy-Authorization":
		trimmed := strings.TrimSpace(value)
		if separator := strings.IndexFunc(trimmed, func(r rune) bool { return r == ' ' || r == '\t' }); separator >= 0 {
			if payload := strings.TrimSpace(trimmed[separator+1:]); payload != "" {
				return []string{payload}
			}
		}
	case "Cookie":
		var values []string
		for pair := range strings.SplitSeq(value, ";") {
			_, rawValue, ok := strings.Cut(pair, "=")
			if !ok {
				continue
			}
			rawValue = strings.TrimSpace(rawValue)
			if rawValue == "" {
				continue
			}
			values = append(values, rawValue)
			unquoted := strings.Trim(rawValue, `"`)
			if unquoted != rawValue {
				values = append(values, unquoted)
			}
			if decoded := unescapeQueryComponent(unquoted); decoded != unquoted {
				values = append(values, decoded)
			}
		}
		return values
	}
	return nil
}

// SanitizeRequestForAPILog returns a credential-free endpoint and an exact
// copy of every surviving final request header name and value.
func SanitizeRequestForAPILog(req *http.Request, material APILogCredentialMaterial) (string, map[string][]string) {
	if req == nil {
		return "", nil
	}
	endpoint := ""
	if req.URL != nil {
		endpoint = SanitizeEndpointURL(req.URL.String())
	}

	var headers map[string][]string
	for name, values := range req.Header {
		// Client requests send req.Host as Host and ignore Header["Host"].
		if strings.EqualFold(name, "Host") {
			continue
		}
		if credentialHeaderName(name, material) || containsCredentialEvidence(name, material) {
			continue
		}
		if strings.EqualFold(name, "Trailer") {
			values = sanitizeTrailerHeaderValues(values, material)
			if len(values) == 0 {
				continue
			}
		}
		if headerValuesContainCredential(values, material) {
			continue
		}
		if headers == nil {
			headers = make(map[string][]string)
		}
		headers[name] = append([]string(nil), values...)
	}
	if req.Host != "" &&
		!credentialHeaderName("Host", material) &&
		!containsCredentialEvidence("Host", material) &&
		!containsCredentialEvidence(req.Host, material) {
		if headers == nil {
			headers = make(map[string][]string)
		}
		headers["Host"] = []string{req.Host}
	}
	return endpoint, headers
}

func sanitizeTrailerHeaderValues(values []string, material APILogCredentialMaterial) []string {
	sanitized := make([]string, 0, len(values))
	for _, value := range values {
		names := strings.Split(value, ",")
		kept := make([]string, 0, len(names))
		removed := false
		for _, name := range names {
			name = strings.TrimSpace(name)
			if name == "" {
				continue
			}
			if credentialHeaderName(name, material) {
				removed = true
				continue
			}
			kept = append(kept, name)
		}
		if !removed {
			sanitized = append(sanitized, value)
			continue
		}
		if len(kept) > 0 {
			sanitized = append(sanitized, strings.Join(kept, ", "))
		}
	}
	return sanitized
}

// SanitizeErrorForAPILog omits credential-bearing provider or warning text.
func SanitizeErrorForAPILog(text string, material APILogCredentialMaterial) string {
	if containsCredentialEvidence(text, material) {
		return ""
	}
	return text
}

func unescapeQueryComponent(value string) string {
	unescaped, err := url.QueryUnescape(value)
	if err != nil {
		return value
	}
	return unescaped
}

func credentialHeaderName(name string, material APILogCredentialMaterial) bool {
	canonical := canonicalCredentialHeaderName(name)
	if _, common := commonCredentialHeaderNames[canonical]; common {
		return true
	}
	_, marked := material.HeaderNames[canonical]
	return marked
}

func credentialQueryName(name string, material APILogCredentialMaterial) bool {
	canonical := canonicalCredentialQueryName(name)
	if _, common := commonCredentialQueryNames[canonical]; common {
		return true
	}
	_, marked := material.QueryNames[canonical]
	return marked
}

func canonicalCredentialHeaderName(name string) string {
	return textproto.CanonicalMIMEHeaderKey(strings.TrimSpace(name))
}

func canonicalCredentialQueryName(name string) string {
	return strings.ToLower(strings.TrimSpace(name))
}

func headerValuesContainCredential(headerValues []string, material APILogCredentialMaterial) bool {
	patterns := credentialEvidencePatterns(material)
	for _, headerValue := range headerValues {
		if containsCredentialHeaderValueEvidence(headerValue, patterns, material.secretNames) {
			return true
		}
	}
	return false
}

func containsCredentialEvidence(text string, material APILogCredentialMaterial) bool {
	return containsCredentialDurableStringEvidenceParts(text, credentialEvidencePatterns(material), material.secretNames)
}

func containsCredentialHeaderValueEvidence(value string, patterns, secretNames []string) bool {
	if containsCredentialEvidenceParts(value, patterns, secretNames) {
		return true
	}
	encoded := apilog.EncodeHeaderValue([]byte(value))
	return containsCredentialDurableStringEvidenceParts(encoded.Data, patterns, secretNames)
}

func containsCredentialDurableStringEvidenceParts(text string, valuePatterns, secretNames []string) bool {
	if containsCredentialEvidenceParts(text, valuePatterns, secretNames) {
		return true
	}
	encoded, err := json.Marshal(text)
	if err != nil || len(encoded) < 2 {
		return false
	}
	return containsCredentialEvidenceParts(string(encoded[1:len(encoded)-1]), valuePatterns, secretNames)
}

func containsCredentialEvidenceParts(text string, valuePatterns, secretNames []string) bool {
	for _, pattern := range valuePatterns {
		if strings.Contains(text, pattern) {
			return true
		}
	}
	lowerText := strings.ToLower(text)
	for _, name := range secretNames {
		if name != "" && strings.Contains(lowerText, name) {
			return true
		}
	}
	return false
}

func caseFoldedCredentialNameVariants(names []string) []string {
	variants := credentialValueVariants(names)
	seen := make(map[string]struct{}, len(variants))
	caseFolded := make([]string, 0, len(variants))
	for _, variant := range variants {
		variant = strings.ToLower(variant)
		if _, duplicate := seen[variant]; duplicate {
			continue
		}
		seen[variant] = struct{}{}
		caseFolded = append(caseFolded, variant)
	}
	return caseFolded
}

func credentialEvidencePatterns(material APILogCredentialMaterial) []string {
	if material.patterns != nil {
		return material.patterns
	}
	return credentialValueVariants(material.Values)
}

func credentialValueVariants(values []string) []string {
	seen := make(map[string]struct{}, len(values)*4)
	for _, value := range values {
		if value == "" {
			continue
		}
		seen[value] = struct{}{}
		seen[url.QueryEscape(value)] = struct{}{}
		seen[url.PathEscape(value)] = struct{}{}
		if encoded, err := json.Marshal(value); err == nil && len(encoded) >= 2 {
			seen[string(encoded[1:len(encoded)-1])] = struct{}{}
		}
	}
	variants := make([]string, 0, len(seen))
	for variant := range seen {
		if variant != "" {
			variants = append(variants, variant)
		}
	}
	sort.Slice(variants, func(i, j int) bool {
		if len(variants[i]) == len(variants[j]) {
			return variants[i] < variants[j]
		}
		return len(variants[i]) > len(variants[j])
	})
	return variants
}
