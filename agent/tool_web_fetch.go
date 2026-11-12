package agent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	htmltomarkdown "github.com/JohannesKaufmann/html-to-markdown/v2"
	"golang.org/x/net/html"

	"primeradiant.com/evener/agent/internal/cheapmodel"
	"primeradiant.com/evener/agent/internal/runetrim"
	"primeradiant.com/evener/llm"
)

const (
	webFetchTimeout               = 30 * time.Second
	webFetchMaxBytes              = 5 * 1024 * 1024 // 5 MiB
	webFetchMaxContent            = 100_000         // chars passed to cheap model
	webFetchRawFallbackMaxContent = 20_000          // chars returned when both models refuse
	webFetchUserAgent             = "evener/1.0"
	// Documentation hosts observed in #349 distinguish the fetcher UA from a
	// browser-shaped client; this is used for one deterministic 403/404 retry.
	webFetchAlternateUserAgent = "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36"
)

// webFetchCacheKey returns a deterministic 16-char hex key for a URL.
func webFetchCacheKey(rawURL string) string {
	return nonProjectHash(rawURL)
}

// htmlToMarkdown converts an HTML string to markdown, prepending a non-empty
// HTML document <title> as a top-level heading when no rendered body heading
// already carries it.
func htmlToMarkdown(rawHTML string) (string, error) {
	doc, err := html.Parse(strings.NewReader(rawHTML))
	if err != nil {
		return "", err
	}
	title, headingHasTitle := inspectHTMLTitle(doc)
	mdBytes, err := htmltomarkdown.ConvertNode(doc)
	if err != nil {
		return "", err
	}
	md := string(mdBytes)
	if title == "" || headingHasTitle || strings.EqualFold(strings.TrimSpace(md), title) {
		return md, nil
	}
	if strings.TrimSpace(md) == "" {
		return "# " + title, nil
	}
	return "# " + title + "\n\n" + md, nil
}

// inspectHTMLTitle walks doc iteratively before ConvertNode mutates it,
// returning the first non-empty HTML <title> (ignoring foreign namespaces such
// as <svg>/<math> and inert containers such as <template>) and whether any body
// heading already matches that title case-insensitively.
func inspectHTMLTitle(doc *html.Node) (title string, headingHasTitle bool) {
	type frame struct {
		node   *html.Node
		inHead bool
	}
	var headings []string
	stack := []frame{{node: doc}}
	for len(stack) > 0 {
		cur := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		n := cur.node
		if n.Type == html.ElementNode {
			if n.Namespace != "" {
				continue
			}
			switch n.Data {
			case "script", "style", "noscript", "iframe", "template":
				continue
			case "head":
				cur.inHead = true
			case "title":
				if title == "" {
					title = collapseHTMLWhitespace(htmlNodeText(n))
				}
				continue
			case "h1", "h2", "h3", "h4", "h5", "h6":
				if !cur.inHead {
					if h := collapseHTMLWhitespace(htmlNodeText(n)); h != "" {
						headings = append(headings, h)
					}
				}
			}
		}
		for child := n.LastChild; child != nil; child = child.PrevSibling {
			stack = append(stack, frame{node: child, inHead: cur.inHead})
		}
	}
	if title == "" {
		return "", false
	}
	return title, slices.ContainsFunc(headings, func(h string) bool {
		return strings.EqualFold(h, title)
	})
}

func htmlNodeText(root *html.Node) string {
	var b strings.Builder
	stack := []*html.Node{root}
	for len(stack) > 0 {
		n := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if n == root {
			for child := n.LastChild; child != nil; child = child.PrevSibling {
				stack = append(stack, child)
			}
			continue
		}
		if n.Type == html.TextNode {
			b.WriteString(n.Data)
			continue
		}
		if n.Type == html.ElementNode {
			if n.Namespace != "" {
				continue
			}
			switch n.Data {
			case "script", "style", "noscript", "iframe", "template":
				continue
			}
		}
		for child := n.LastChild; child != nil; child = child.PrevSibling {
			stack = append(stack, child)
		}
	}
	return b.String()
}

func collapseHTMLWhitespace(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// webFetchCachePath returns the absolute per-fetch directory path under the XDG cache dir.
func webFetchCachePath(rawURL string) string {
	date := time.Now().UTC().Format("2006-01-02")
	key := webFetchCacheKey(rawURL)
	return filepath.Join(CacheDir(), "web_cache", date, key)
}

// httpDoer is the injectable seam for web_fetch's HTTP GET. *http.Client
// satisfies it; tests supply a fake transport so the fetch never hits the
// network. Nil on the session means the production default (http.DefaultClient).
type httpDoer interface {
	Do(*http.Request) (*http.Response, error)
}

// webFetchClient returns the session's injected HTTP client, or the process
// default when none was set (the production path).
func (s *Session) webFetchClient() httpDoer {
	if s.httpClient != nil {
		return s.httpClient
	}
	return http.DefaultClient
}

// webFetch performs the full web_fetch operation: HTTP GET, cache files, cheap model Q&A.
func (s *Session) webFetch(ctx context.Context, rawURL string, question string) (any, error) {
	return s.webFetchWithRuntime(ctx, rawURL, question, webFetchRuntime{
		cachePath:  webFetchCachePath,
		mkdirAll:   os.MkdirAll,
		writeFile:  os.WriteFile,
		toMarkdown: htmlToMarkdown,
		newRequest: http.NewRequestWithContext,
	})
}

type webFetchRuntime struct {
	cachePath  func(string) string
	mkdirAll   func(string, os.FileMode) error
	writeFile  func(string, []byte, os.FileMode) error
	toMarkdown func(string) (string, error)
	newRequest func(context.Context, string, string, io.Reader) (*http.Request, error)
}

func (s *Session) webFetchWithRuntime(ctx context.Context, rawURL string, question string, rt webFetchRuntime) (any, error) {
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

	newRequest := func(userAgent string) (*http.Request, error) {
		req, err := rt.newRequest(httpCtx, http.MethodGet, rawURL, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("User-Agent", userAgent)
		return req, nil
	}

	req, err := newRequest(webFetchUserAgent)
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}

	resp, err := s.webFetchClient().Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetching URL: %w", err)
	}

	if resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusNotFound {
		_ = resp.Body.Close()
		if err := httpCtx.Err(); err != nil {
			return nil, fmt.Errorf("fetching URL: %w", err)
		}
		retryReq, err := newRequest(webFetchAlternateUserAgent)
		if err != nil {
			return nil, fmt.Errorf("creating retry request: %w", err)
		}
		resp, err = s.webFetchClient().Do(retryReq)
		if err != nil {
			return nil, fmt.Errorf("fetching URL after alternate user agent: %w", err)
		}
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
	isHTML := strings.Contains(strings.ToLower(contentType), "text/html")
	sizeBytes := len(body)

	// Per-fetch cache directory (absolute XDG path).
	fetchDir := rt.cachePath(rawURL)
	if err := rt.mkdirAll(fetchDir, 0o755); err != nil {
		return nil, fmt.Errorf("creating cache dir: %w", err)
	}

	// Write raw file.
	rawExt := extFromContentType(contentType)
	rawName := "raw" + rawExt
	rawPath := filepath.Join(fetchDir, rawName)
	if err := rt.writeFile(rawPath, body, 0o644); err != nil {
		return nil, fmt.Errorf("writing raw file: %w", err)
	}

	// Convert to markdown if HTML; otherwise use raw content for the cheap model.
	var readableContent string
	mdPath := ""
	if isHTML {
		md, err := rt.toMarkdown(string(body))
		if err != nil {
			// Fall back to raw content if conversion fails.
			readableContent = string(body)
		} else {
			readableContent = md
			mdPath = filepath.Join(fetchDir, "rendered.md")
			if err := rt.writeFile(mdPath, []byte(md), 0o644); err != nil {
				return nil, fmt.Errorf("writing rendered markdown: %w", err)
			}
		}
	} else {
		readableContent = string(body)
	}

	// Truncate content for the cheap model, retaining the complete converted
	// content for the bounded raw fallback below.
	modelContent := runetrim.Cut(readableContent, webFetchMaxContent)

	// Call cheap model to answer the question.
	p := s.currentProfile()
	cheapReq := llm.Request{
		Messages: []llm.Message{
			llm.System("You are a web content analyst. Read the provided content and answer the user's question concisely."),
			llm.User(fmt.Sprintf("URL: %s\n\nQuestion: %s\n\nContent:\n%s", rawURL, question, modelContent)),
		},
	}

	cheapResp, err := s.cheap.Complete(ctx, p, cheapReq)
	if err != nil {
		if errors.Is(err, cheapmodel.ErrAllModelsRefused) {
			fallbackContent, truncated := truncateWebFetchContent(readableContent, webFetchRawFallbackMaxContent)
			result := map[string]any{
				"fallback":          "raw",
				"fallback_reason":   "both_models_refused",
				"content":           fallbackContent,
				"content_source":    "raw",
				"content_truncated": truncated,
				"content_limit":     webFetchRawFallbackMaxContent,
				"content_untrusted": true,
				"raw_file":          rawPath,
				"url":               rawURL,
				"source_url":        rawURL,
				"question":          question,
				"content_type":      contentType,
				"size_bytes":        sizeBytes,
			}
			if mdPath != "" {
				result["content_source"] = "converted_markdown"
				result["markdown_file"] = mdPath
			}
			return result, nil
		}
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

// truncateWebFetchContent bounds fallback content by Unicode characters so the
// returned text is valid UTF-8 and the advertised limit is deterministic.
func truncateWebFetchContent(content string, limit int) (string, bool) {
	if limit <= 0 {
		return content, false
	}
	runes := []rune(content)
	if len(runes) <= limit {
		return content, false
	}
	return string(runes[:limit]), true
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
