package transport

import "net/http"

type standardCompressionOwner interface {
	APILogTransportUsesStandardCompression() bool
}

func ownStandardCompression(base http.RoundTripper) (http.RoundTripper, bool) {
	if transport, ok := base.(*http.Transport); ok {
		return base, !transport.DisableCompression
	}
	if owner, ok := base.(standardCompressionOwner); ok {
		return base, owner.APILogTransportUsesStandardCompression()
	}
	return base, false
}

func requestWithOwnedStandardCompression(request *http.Request, enabled bool) (*http.Request, bool) {
	if !enabled || request.Header.Get("Accept-Encoding") != "" || request.Header.Get("Range") != "" || request.Method == http.MethodHead {
		return request, false
	}
	clone := request.Clone(request.Context())
	clone.Header = request.Header.Clone()
	clone.Header.Set("Accept-Encoding", "gzip")
	return clone, true
}
