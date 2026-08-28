package hubapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// Client is a small typed HTTP client for the evener hub JSON API.
type Client struct {
	baseURL    *url.URL
	httpClient *http.Client
}

// HTTPError describes a non-successful HTTP response from the hub.
type HTTPError struct {
	Status   int
	Response ErrorResponse
}

func (e *HTTPError) Error() string {
	if e.Response.Error != "" {
		return fmt.Sprintf("hub returned %d: %s", e.Status, e.Response.Error)
	}
	return fmt.Sprintf("hub returned %d", e.Status)
}
func (e *HTTPError) StatusCode() int { return e.Status }

func NewClient(base string, httpClient *http.Client) (*Client, error) {
	if !strings.Contains(base, "://") {
		base = "http://" + base
	}
	u, err := url.Parse(base)
	if err != nil {
		return nil, err
	}
	if u.Scheme == "" || u.Host == "" {
		return nil, fmt.Errorf("invalid hub URL %q", base)
	}
	u.Path = strings.TrimRight(u.Path, "/")
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	return &Client{baseURL: u, httpClient: httpClient}, nil
}

func (c *Client) URL(path string) string {
	u := *c.baseURL
	fullPath := strings.TrimRight(c.baseURL.Path, "/") + "/" + strings.TrimLeft(path, "/")
	if p, q, ok := strings.Cut(fullPath, "?"); ok {
		u.Path = p
		u.RawQuery = q
	} else {
		u.Path = fullPath
		u.RawQuery = ""
	}
	return u.String()
}

func (c *Client) get(ctx context.Context, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.URL(path), nil)
	if err != nil {
		return err
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close() //nolint:errcheck // response body close on read path; error is not actionable
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("hub returned %d", resp.StatusCode)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func (c *Client) Health(ctx context.Context) (HealthResponse, error) {
	var out HealthResponse
	err := c.get(ctx, "/api/health", &out)
	return out, err
}
