package llm

import (
	"net/http"
	"net/textproto"
	"net/url"
	"sort"
	"strings"
)

const apiLogCredentialReplacement = "[credential excluded]"

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
	for _, name := range headerNames {
		canonical := canonicalCredentialHeaderName(name)
		if canonical == "" {
			continue
		}
		if material.HeaderNames == nil {
			material.HeaderNames = make(map[string]struct{})
		}
		material.HeaderNames[canonical] = struct{}{}
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
	return material
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
	for name, requestValues := range req.Header {
		if !credentialHeaderName(name, configured) {
			continue
		}
		headerNames = append(headerNames, name)
		for _, value := range requestValues {
			values = append(values, value)
			values = append(values, structuredCredentialHeaderValues(name, value)...)
		}
	}
	if req.URL != nil {
		for _, part := range strings.Split(req.URL.RawQuery, "&") {
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
		for _, pair := range strings.Split(value, ";") {
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
		cleanURL := *req.URL
		cleanURL.User = nil
		cleanURL.RawQuery = sanitizeRawQuery(cleanURL.RawQuery, material)
		endpoint = cleanURL.String()
	}

	var headers map[string][]string
	for name, values := range req.Header {
		if credentialHeaderName(name, material) || headerValuesContainCredential(values, material.Values) {
			continue
		}
		if headers == nil {
			headers = make(map[string][]string)
		}
		headers[name] = append([]string(nil), values...)
	}
	return endpoint, headers
}

// SanitizeErrorForAPILog removes provider/config-derived credential names and
// raw or URL-escaped credential values before errors become durable API-log
// text or warnings.
func SanitizeErrorForAPILog(text string, material APILogCredentialMaterial) string {
	variants := credentialValueVariants(material.Values)
	for _, value := range variants {
		text = strings.ReplaceAll(text, value, apiLogCredentialReplacement)
	}
	for _, name := range credentialNameVariants(material) {
		text = replaceCredentialNameFold(text, name)
	}
	return text
}

func credentialNameVariants(material APILogCredentialMaterial) []string {
	seen := make(map[string]struct{}, len(material.HeaderNames)+len(material.QueryNames))
	for name := range material.HeaderNames {
		seen[name] = struct{}{}
	}
	for name := range material.QueryNames {
		seen[name] = struct{}{}
	}
	names := make([]string, 0, len(seen))
	for name := range seen {
		if name != "" {
			names = append(names, name)
		}
	}
	sort.Slice(names, func(i, j int) bool {
		if len(names[i]) == len(names[j]) {
			return names[i] < names[j]
		}
		return len(names[i]) > len(names[j])
	})
	return names
}

func replaceCredentialNameFold(text, name string) string {
	if text == "" || name == "" {
		return text
	}
	lowerText := strings.ToLower(text)
	lowerName := strings.ToLower(name)
	var out strings.Builder
	start := 0
	for {
		relative := strings.Index(lowerText[start:], lowerName)
		if relative < 0 {
			out.WriteString(text[start:])
			return out.String()
		}
		index := start + relative
		end := index + len(name)
		if credentialNameBoundary(text, index, end) {
			out.WriteString(text[start:index])
			out.WriteString(apiLogCredentialReplacement)
			start = end
			continue
		}
		out.WriteString(text[start : index+1])
		start = index + 1
	}
}

func credentialNameBoundary(text string, start, end int) bool {
	return (start == 0 || !credentialNameByte(text[start-1])) &&
		(end == len(text) || !credentialNameByte(text[end]))
}

func credentialNameByte(value byte) bool {
	return value >= 'a' && value <= 'z' ||
		value >= 'A' && value <= 'Z' ||
		value >= '0' && value <= '9' ||
		value == '-' || value == '_'
}

func sanitizeRawQuery(rawQuery string, material APILogCredentialMaterial) string {
	if rawQuery == "" {
		return ""
	}
	parts := strings.Split(rawQuery, "&")
	kept := parts[:0]
	for _, part := range parts {
		rawName, rawValue, _ := strings.Cut(part, "=")
		name := unescapeQueryComponent(rawName)
		value := unescapeQueryComponent(rawValue)
		if credentialQueryName(name, material) || credentialValueEqual(value, rawValue, material.Values) {
			continue
		}
		kept = append(kept, part)
	}
	return strings.Join(kept, "&")
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

func headerValuesContainCredential(headerValues, credentials []string) bool {
	for _, headerValue := range headerValues {
		for _, credential := range credentials {
			if credential != "" && strings.Contains(headerValue, credential) {
				return true
			}
		}
	}
	return false
}

func credentialValueEqual(decoded, raw string, credentials []string) bool {
	for _, credential := range credentials {
		if credential == "" {
			continue
		}
		if decoded == credential || raw == credential || raw == url.QueryEscape(credential) || raw == url.PathEscape(credential) {
			return true
		}
	}
	return false
}

func credentialValueVariants(values []string) []string {
	seen := make(map[string]struct{}, len(values)*3)
	for _, value := range values {
		if value == "" {
			continue
		}
		seen[value] = struct{}{}
		seen[url.QueryEscape(value)] = struct{}{}
		seen[url.PathEscape(value)] = struct{}{}
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
