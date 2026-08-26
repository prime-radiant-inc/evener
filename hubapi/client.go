package hubapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"primeradiant.com/evener/agent/task"
)

// Client is a small typed HTTP client for the evener hub JSON API.
type Client struct {
	baseURL    *url.URL
	httpClient *http.Client
}

// ConditionalResult is the result of a conditional navigation request.
// Value is the zero value when NotModified is true.
type ConditionalResult[T any] struct {
	NotModified bool
	ETag        string
	Value       T
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

func conditionalGet[T any](ctx context.Context, c *Client, target, etag string) (ConditionalResult[T], error) {
	var result ConditionalResult[T]
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return result, err
	}
	if etag != "" {
		req.Header.Set("If-None-Match", etag)
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return result, err
	}
	defer resp.Body.Close() //nolint:errcheck // response body close is not actionable
	result.ETag = resp.Header.Get("ETag")
	if resp.StatusCode == http.StatusNotModified {
		if result.ETag == "" {
			result.ETag = etag
		}
		return ConditionalResult[T]{NotModified: true, ETag: result.ETag}, nil
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var response ErrorResponse
		body, readErr := io.ReadAll(io.LimitReader(resp.Body, navigationDecodeLimit+1))
		if readErr == nil {
			_ = json.Unmarshal(body, &response)
		}
		return result, &HTTPError{Status: resp.StatusCode, Response: response}
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, navigationDecodeLimit+1))
	if err != nil {
		return result, err
	}
	if len(body) > navigationDecodeLimit {
		return result, fmt.Errorf("navigation response exceeds %d bytes", navigationDecodeLimit)
	}
	if err := json.Unmarshal(body, &result.Value); err != nil {
		return result, err
	}
	return result, nil
}

const navigationDecodeLimit = 2 << 20

func navigationPath(path string, values url.Values) string {
	if encoded := values.Encode(); encoded != "" {
		return path + "?" + encoded
	}
	return path
}

func navigationSegment(value string) string { return url.PathEscape(value) }

func navigationGet[T any](ctx context.Context, c *Client, path, etag string, query url.Values) (ConditionalResult[T], error) {
	targetPath := navigationPath(path, query)
	u := *c.baseURL
	basePath := strings.TrimRight(c.baseURL.Path, "/")
	escapedPath := basePath + "/" + strings.TrimLeft(strings.SplitN(targetPath, "?", 2)[0], "/")
	decoded, err := url.PathUnescape(escapedPath)
	if err != nil {
		return ConditionalResult[T]{}, err
	}
	u.Path = decoded
	u.RawPath = escapedPath
	if encoded := query.Encode(); encoded != "" {
		u.RawQuery = encoded
	} else {
		u.RawQuery = ""
	}
	return conditionalGet[T](ctx, c, u.String(), etag)
}

func (c *Client) NavigationManifest(ctx context.Context, etag string) (ConditionalResult[NavigationManifest], error) {
	return navigationGet[NavigationManifest](ctx, c, "/api/navigation", etag, nil)
}

func (c *Client) NavigationSection(ctx context.Context, section string, offset, limit uint32, etag string) (ConditionalResult[NavigationSectionResource], error) {
	query, err := navigationPageQuery(offset, limit, 50)
	if err != nil {
		return ConditionalResult[NavigationSectionResource]{}, err
	}
	return navigationGet[NavigationSectionResource](ctx, c, "/api/navigation/sections/"+navigationSegment(section), etag, query)
}

func (c *Client) NavigationPinSections(ctx context.Context, offset, limit uint32, etag string) (ConditionalResult[NavigationPinSectionCatalog], error) {
	query, err := navigationPageQuery(offset, limit, 100)
	if err != nil {
		return ConditionalResult[NavigationPinSectionCatalog]{}, err
	}
	return navigationGet[NavigationPinSectionCatalog](ctx, c, "/api/navigation/pin-sections", etag, query)
}

func (c *Client) NavigationPinSection(ctx context.Context, id string, offset, limit uint32, etag string) (ConditionalResult[NavigationSectionResource], error) {
	query, err := navigationPageQuery(offset, limit, 50)
	if err != nil {
		return ConditionalResult[NavigationSectionResource]{}, err
	}
	return navigationGet[NavigationSectionResource](ctx, c, "/api/navigation/pin-sections/"+navigationSegment(id), etag, query)
}

func (c *Client) NavigationCatalog(ctx context.Context, catalog string, offset, limit uint32, etag string) (ConditionalResult[NavigationProjectCatalog], error) {
	query, err := navigationPageQuery(offset, limit, 100)
	if err != nil {
		return ConditionalResult[NavigationProjectCatalog]{}, err
	}
	return navigationGet[NavigationProjectCatalog](ctx, c, "/api/navigation/catalogs/"+navigationSegment(catalog), etag, query)
}

func (c *Client) NavigationProject(ctx context.Context, key, etag string) (ConditionalResult[NavigationProjectResource], error) {
	return navigationGet[NavigationProjectResource](ctx, c, "/api/navigation/projects/"+navigationSegment(key), etag, nil)
}

func (c *Client) NavigationProjectPage(ctx context.Context, key, tier string, offset, limit uint32, etag string) (ConditionalResult[NavigationProjectPage], error) {
	query, err := navigationPageQuery(offset, limit, 50)
	if err != nil {
		return ConditionalResult[NavigationProjectPage]{}, err
	}
	query.Set("tier", tier)
	return navigationGet[NavigationProjectPage](ctx, c, "/api/navigation/projects/"+navigationSegment(key), etag, query)
}

func (c *Client) NavigationSessionLocation(ctx context.Context, ref, etag string) (ConditionalResult[NavigationSessionLocation], error) {
	return navigationGet[NavigationSessionLocation](ctx, c, "/api/navigation/sessions/"+navigationSegment(ref), etag, nil)
}

func navigationPageQuery(offset, limit, maximum uint32) (url.Values, error) {
	query := make(url.Values)
	if limit > maximum {
		return nil, fmt.Errorf("navigation limit must be at most %d", maximum)
	}
	if offset != 0 {
		query.Set("offset", fmt.Sprintf("%d", offset))
	}
	if limit != 0 {
		query.Set("limit", fmt.Sprintf("%d", limit))
	}
	return query, nil
}

func (c *Client) post(ctx context.Context, path string, body any, out any) error {
	var rbody *bytes.Reader
	if body == nil {
		rbody = bytes.NewReader(nil)
	} else {
		data, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rbody = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.URL(path), rbody)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close() //nolint:errcheck // response body close on read path; error is not actionable
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("hub returned %d", resp.StatusCode)
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func (c *Client) Health(ctx context.Context) (HealthResponse, error) {
	var out HealthResponse
	err := c.get(ctx, "/api/health", &out)
	return out, err
}

func (c *Client) Tree(ctx context.Context) (TreeResponse, error) {
	var out TreeResponse
	err := c.get(ctx, "/api/tree", &out)
	return out, err
}

func (c *Client) Session(ctx context.Context, ref Ref) (SessionDetail, error) {
	var out SessionDetail
	err := c.get(ctx, "/api/sessions/"+ref.PathEscaped(), &out)
	return out, err
}

func (c *Client) SpawnSchema(ctx context.Context) (SpawnSchema, error) {
	var out SpawnSchema
	err := c.get(ctx, "/api/spawn-schema", &out)
	return out, err
}

func (c *Client) Spawn(ctx context.Context, req SpawnRequest) (SpawnResponse, error) {
	var out SpawnResponse
	err := c.post(ctx, "/api/spawn", req, &out)
	return out, err
}

func (c *Client) Models(ctx context.Context) ([]ModelOption, error) {
	var out []ModelOption
	err := c.get(ctx, "/api/models", &out)
	return out, err
}

func (c *Client) Send(ctx context.Context, ref Ref, text string) error {
	return c.post(ctx, "/api/sessions/"+ref.PathEscaped()+"/send", map[string]string{"text": text}, nil)
}

func (c *Client) Tasks(ctx context.Context, ref Ref) ([]task.Task, error) {
	var out []task.Task
	err := c.get(ctx, "/api/sessions/"+ref.PathEscaped()+"/tasks", &out)
	return out, err
}

func (c *Client) Interrupt(ctx context.Context, ref Ref) error {
	return c.post(ctx, "/api/sessions/"+ref.PathEscaped()+"/interrupt", nil, nil)
}

func (c *Client) Compact(ctx context.Context, ref Ref) error {
	return c.post(ctx, "/api/sessions/"+ref.PathEscaped()+"/compact", nil, nil)
}

func (c *Client) Clear(ctx context.Context, ref Ref) (RefResponse, error) {
	var out RefResponse
	err := c.post(ctx, "/api/sessions/"+ref.PathEscaped()+"/clear", nil, &out)
	return out, err
}

func (c *Client) Fork(ctx context.Context, ref Ref, req ForkRequest) (ForkResponse, error) {
	var out ForkResponse
	err := c.post(ctx, "/api/sessions/"+ref.PathEscaped()+"/fork", req, &out)
	return out, err
}

func (c *Client) SetModel(ctx context.Context, ref Ref, model string) error {
	return c.post(ctx, "/api/sessions/"+ref.PathEscaped()+"/model", map[string]string{"model": model}, nil)
}
