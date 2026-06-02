package hubapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"primeradiant.com/serf/agent/task"
)

// Client is a small typed HTTP client for the serf hub JSON API.
type Client struct {
	baseURL    *url.URL
	httpClient *http.Client
}

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

func (c *Client) Fork(ctx context.Context, ref Ref, req ForkRequest) (RefResponse, error) {
	var out RefResponse
	err := c.post(ctx, "/api/sessions/"+ref.PathEscaped()+"/fork", req, &out)
	return out, err
}

func (c *Client) SetModel(ctx context.Context, ref Ref, model string) error {
	return c.post(ctx, "/api/sessions/"+ref.PathEscaped()+"/model", map[string]string{"model": model}, nil)
}
