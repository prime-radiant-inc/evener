package modelavailability

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"sync"
	"time"

	"primeradiant.com/evener/llm"
)

type StatusKind string

const (
	StatusSuccess StatusKind = "success"
	StatusEmpty   StatusKind = "empty-success"
	StatusFailure StatusKind = "failure"
	StatusTimeout StatusKind = "timeout"
)

type ProviderStatus struct {
	Kind   StatusKind `json:"kind"`
	Detail string     `json:"detail,omitempty"`
}
type Snapshot struct {
	Version  string
	Complete bool
	Choices  []string
	Status   map[string]ProviderStatus
	key      []byte
	mu       *sync.Mutex
}
type Page struct {
	Choices    []string                  `json:"choices,omitempty"`
	Oversized  []int                     `json:"oversized,omitempty"`
	Next       string                    `json:"next,omitempty"`
	Version    string                    `json:"version"`
	Complete   bool                      `json:"complete"`
	Terminal   bool                      `json:"terminal"`
	Status     map[string]ProviderStatus `json:"status"`
	Provenance string                    `json:"provenance"`
}

const (
	DefaultInlineMaxCount = 128
	DefaultInlineMaxBytes = 4096
	DefaultPageMaxBytes   = 4096
	DefaultPageMaxCount   = 128
)

func Capture(parent context.Context, providers []string, fetch func(context.Context, string) ([]llm.ModelInfo, error), budget time.Duration) Snapshot {
	ctx, cancel := context.WithTimeout(parent, budget)
	defer cancel()
	providers = append([]string(nil), providers...)
	sort.Strings(providers)
	type result struct {
		name   string
		models []llm.ModelInfo
		err    error
	}
	out := make(chan result, len(providers))
	for _, name := range providers {
		go func(name string) {
			m, e := fetch(ctx, name)
			select {
			case out <- result{name, m, e}:
			case <-ctx.Done():
			}
		}(name)
	}
	status := map[string]ProviderStatus{}
	set := map[string]bool{}
	received := 0
	for received < len(providers) {
		select {
		case r := <-out:
			received++
			if r.err != nil {
				k := StatusFailure
				if errors.Is(r.err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
					k = StatusTimeout
				}
				status[r.name] = ProviderStatus{k, "enumeration did not complete"}
				continue
			}
			if len(r.models) == 0 {
				status[r.name] = ProviderStatus{StatusEmpty, "provider verified no models"}
			} else {
				status[r.name] = ProviderStatus{StatusSuccess, "enumeration succeeded"}
			}
			for _, m := range r.models {
				if id := strings.TrimSpace(m.ID); id != "" {
					set[r.name+"/"+id] = true
				}
			}
		case <-ctx.Done():
			for _, p := range providers {
				if _, ok := status[p]; !ok {
					status[p] = ProviderStatus{StatusTimeout, "enumeration timed out"}
				}
			}
			received = len(providers)
		}
	}
	choices := make([]string, 0, len(set))
	for c := range set {
		choices = append(choices, c)
	}
	sort.Strings(choices)
	complete := len(providers) > 0
	for _, p := range providers {
		if status[p].Kind != StatusSuccess && status[p].Kind != StatusEmpty {
			complete = false
		}
	}
	key := make([]byte, 32)
	_, _ = rand.Read(key)
	h := sha256.New()
	h.Write([]byte("model-snapshot-v2\x00"))
	h.Write([]byte{byte(len(providers))})
	for _, p := range providers {
		h.Write([]byte(p))
		h.Write([]byte{0})
		b, _ := json.Marshal(status[p])
		h.Write(b)
	}
	for _, c := range choices {
		h.Write([]byte(c))
		h.Write([]byte{0})
	}
	version := hex.EncodeToString(h.Sum(nil)[:12])
	return Snapshot{Version: version, Complete: complete, Choices: choices, Status: status, key: key, mu: &sync.Mutex{}}
}
func (s Snapshot) Inline(maxCount, maxBytes int) (string, bool) {
	if !s.Complete || len(s.Choices) > maxCount {
		return "", false
	}
	text := "Verified startup snapshot " + s.Version + ": " + strings.Join(s.Choices, ", ") + "."
	return text, len([]byte(text)) <= maxBytes
}

type cursor struct {
	Schema   string `json:"schema"`
	Binding  string `json:"binding"`
	Snapshot string `json:"snapshot"`
	Offset   int    `json:"offset"`
	Count    int    `json:"count"`
	Bytes    int    `json:"bytes"`
}

func (s *Snapshot) token(c cursor) string {
	b, _ := json.Marshal(c)
	mac := hmac.New(sha256.New, s.key)
	mac.Write(b)
	return base64.RawURLEncoding.EncodeToString(append(b, mac.Sum(nil)...))
}
func (s *Snapshot) decode(token string) (cursor, error) {
	if len(token) > 1024 {
		return cursor{}, errors.New("malformed cursor")
	}
	raw, e := base64.RawURLEncoding.DecodeString(token)
	if e != nil || len(raw) <= 32 {
		return cursor{}, errors.New("malformed cursor")
	}
	b, tag := raw[:len(raw)-32], raw[len(raw)-32:]
	mac := hmac.New(sha256.New, s.key)
	mac.Write(b)
	if !hmac.Equal(tag, mac.Sum(nil)) {
		return cursor{}, errors.New("cursor authentication failed")
	}
	var c cursor
	d := json.NewDecoder(strings.NewReader(string(b)))
	d.DisallowUnknownFields()
	if d.Decode(&c) != nil || d.More() {
		return cursor{}, errors.New("malformed cursor")
	}
	if c.Schema != "model-list-v1" || c.Binding == "" || c.Snapshot != s.Version || c.Offset < 0 || c.Offset > len(s.Choices) || c.Count <= 0 || c.Bytes <= 0 {
		return cursor{}, errors.New("stale cursor")
	}
	return c, nil
}
func (s *Snapshot) Page(token string, maxCount, maxBytes int) (Page, error) {
	if s == nil || s.mu == nil || maxCount <= 0 || maxCount > DefaultPageMaxCount || maxBytes <= 0 || maxBytes > DefaultPageMaxBytes {
		return Page{}, errors.New("invalid page bounds")
	}
	off := 0
	if token != "" {
		c, e := s.decode(token)
		if e != nil {
			return Page{}, e
		}
		off = c.Offset
	}
	status := s.Status
	if status == nil {
		status = map[string]ProviderStatus{}
	}
	p := Page{Version: s.Version, Complete: s.Complete, Status: status, Provenance: "startup-frozen"}
	if off >= len(s.Choices) {
		p.Terminal = true
		if p.SerializedBytes() > maxBytes {
			return Page{}, errors.New("page envelope budget too small")
		}
		return p, nil
	}
	for off < len(s.Choices) && len(p.Choices)+len(p.Oversized) < maxCount {
		candidate := s.Choices[off]
		p.Choices = append(p.Choices, candidate)
		if p.SerializedBytes() > maxBytes {
			p.Choices = p.Choices[:len(p.Choices)-1]
			p.Oversized = append(p.Oversized, off)
		}
		off++
		if p.SerializedBytes() > maxBytes {
			p.Oversized = p.Oversized[:len(p.Oversized)-1]
			break
		}
	}
	if len(p.Choices) == 0 && len(p.Oversized) == 0 {
		return Page{}, errors.New("page envelope budget too small")
	}
	if off < len(s.Choices) {
		p.Next = s.token(cursor{"model-list-v1", "root", s.Version, off, maxCount, maxBytes})
	}
	if off >= len(s.Choices) {
		p.Terminal = true
	}
	if p.SerializedBytes() > maxBytes {
		return Page{}, errors.New("page envelope budget too small")
	}
	return p, nil
}
func (p Page) SerializedBytes() int { b, _ := json.Marshal(p); return len(b) }
