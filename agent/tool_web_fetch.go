package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	htmltomarkdown "github.com/JohannesKaufmann/html-to-markdown/v2"

	"primeradiant.com/serf/llm"
)

const (
	webFetchTimeout    = 30 * time.Second
	webFetchMaxBytes   = 5 * 1024 * 1024 // 5 MiB
	webFetchMaxContent = 100_000         // chars passed to cheap model
)

// webFetchCacheKey returns a deterministic 16-char hex key for a URL.
func webFetchCacheKey(rawURL string) string {
	sum := sha256.Sum256([]byte(rawURL))
	return hex.EncodeToString(sum[:8])
}

// htmlToMarkdown converts an HTML string to markdown.
func htmlToMarkdown(html string) (string, error) {
	return htmltomarkdown.ConvertString(html)
}

// webFetchCachePath returns the absolute per-fetch directory path under the XDG cache dir.
func webFetchCachePath(rawURL string) string {
	date := time.Now().UTC().Format("2006-01-02")
	key := webFetchCacheKey(rawURL)
	return filepath.Join(CacheDir(), "web_cache", date, key)
}

// webFetch performs the full web_fetch operation: HTTP GET, cache files, cheap model Q&A.
func (s *Session) webFetch(ctx context.Context, rawURL string, question string) (any, error) {
	// Validate URL scheme.
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("invalid URL: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("unsupported URL scheme %q: only http and https are supported", u.Scheme)
	}

	// HTTP GET with timeout.
	httpCtx, cancel := context.WithTimeout(ctx, webFetchTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(httpCtx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}
	req.Header.Set("User-Agent", "serf/1.0")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetching URL: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("HTTP %d %s", resp.StatusCode, resp.Status)
	}

	// Read body with size limit.
	body, err := io.ReadAll(io.LimitReader(resp.Body, webFetchMaxBytes))
	if err != nil {
		return nil, fmt.Errorf("reading response body: %w", err)
	}

	contentType := resp.Header.Get("Content-Type")
	isHTML := strings.Contains(contentType, "text/html")
	sizeBytes := len(body)

	// Per-fetch cache directory (absolute XDG path).
	fetchDir := webFetchCachePath(rawURL)
	if err := os.MkdirAll(fetchDir, 0o755); err != nil {
		return nil, fmt.Errorf("creating cache dir: %w", err)
	}

	// Write raw file.
	rawExt := extFromContentType(contentType)
	rawName := "raw" + rawExt
	rawPath := filepath.Join(fetchDir, rawName)
	if err := os.WriteFile(rawPath, body, 0o644); err != nil {
		return nil, fmt.Errorf("writing raw file: %w", err)
	}

	// Convert to markdown if HTML; otherwise use raw content for the cheap model.
	var readableContent string
	mdPath := ""
	if isHTML {
		md, err := htmlToMarkdown(string(body))
		if err != nil {
			// Fall back to raw content if conversion fails.
			readableContent = string(body)
		} else {
			readableContent = md
			mdPath = filepath.Join(fetchDir, "rendered.md")
			if err := os.WriteFile(mdPath, []byte(md), 0o644); err != nil {
				return nil, fmt.Errorf("writing rendered markdown: %w", err)
			}
		}
	} else {
		readableContent = string(body)
	}

	// Truncate content for the cheap model.
	if len(readableContent) > webFetchMaxContent {
		readableContent = readableContent[:webFetchMaxContent]
	}

	// Call cheap model to answer the question.
	cheapReq := llm.Request{
		Model:    s.profile.CheapModel(),
		Provider: s.profile.ID(),
		Messages: []llm.Message{
			llm.System("You are a web content analyst. Read the provided content and answer the user's question concisely."),
			llm.User(fmt.Sprintf("URL: %s\n\nQuestion: %s\n\nContent:\n%s", rawURL, question, readableContent)),
		},
	}

	cheapResp, err := s.client.Complete(ctx, cheapReq)
	if err != nil {
		return nil, fmt.Errorf("cheap model call failed: %w", err)
	}

	result := map[string]any{
		"answer":       cheapResp.Text(),
		"raw_file":     rawPath,
		"url":          rawURL,
		"content_type": contentType,
		"size_bytes":   sizeBytes,
	}
	if mdPath != "" {
		result["markdown_file"] = mdPath
	}

	return result, nil
}

// extFromContentType returns a file extension for a content type.
func extFromContentType(ct string) string {
	ct = strings.ToLower(ct)
	switch {
	case strings.Contains(ct, "text/html"):
		return ".html"
	case strings.Contains(ct, "application/json"):
		return ".json"
	case strings.Contains(ct, "text/plain"):
		return ".txt"
	case strings.Contains(ct, "text/xml"), strings.Contains(ct, "application/xml"):
		return ".xml"
	default:
		return ".bin"
	}
}
