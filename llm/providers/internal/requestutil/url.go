package requestutil

import "net/url"

// WithQuery adds key/value pairs (empty values skipped) to a URL that may
// already carry a query string.
func WithQuery(raw string, pairs ...string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", err
	}
	q := u.Query()
	for i := 0; i+1 < len(pairs); i += 2 {
		if pairs[i+1] != "" {
			q.Set(pairs[i], pairs[i+1])
		}
	}
	u.RawQuery = q.Encode()
	return u.String(), nil
}
