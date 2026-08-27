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
	"slices"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"primeradiant.com/evener/llm"
)

type StatusKind string

const (
	StatusSuccess StatusKind = "success"
	StatusEmpty   StatusKind = "empty-success"
	StatusFailure StatusKind = "failure"
	StatusTimeout StatusKind = "timeout"
	StatusLimited StatusKind = "limited"
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
	// Capture bounds keep startup catalog work finite even when a provider
	// returns an unexpectedly large or malformed model list.
	captureMaxProviders   = 16
	captureMaxModels      = 4096
	captureMaxBytes       = 256 * 1024
	captureMaxProviderLen = 64
	captureMaxModelIDLen  = 256
)

func Capture(parent context.Context, providers []string, requiredProvider string, fetch func(context.Context, string) ([]llm.ModelInfo, error), visible func(string, llm.ModelInfo) bool, budget time.Duration) Snapshot {
	ctx, cancel := context.WithTimeout(parent, budget)
	defer cancel()
	providers, providerLimited := boundedProviders(providers, requiredProvider)
	sort.Strings(providers)
	type result struct {
		name    string
		ids     []string
		err     error
		limited bool
	}
	out := make(chan result, len(providers))
	for _, name := range providers {
		go func(name string) {
			models, err := fetch(ctx, name)
			r := result{name: name, err: err}
			if err == nil {
				r.ids, r.limited, r.err = boundedModelIDs(ctx, name, models, visible)
			}
			select {
			case out <- r:
			case <-ctx.Done():
			}
		}(name)
	}
	results := make(map[string]result, len(providers))
	received := 0
	for received < len(providers) {
		select {
		case r := <-out:
			received++
			results[r.name] = r
		case <-ctx.Done():
			received = len(providers)
		}
	}
	status := make(map[string]ProviderStatus, len(providers))
	choices := make([]string, 0)
	choiceBytes := 0
	complete := len(providers) > 0 && !providerLimited
	for _, p := range providers {
		r, ok := results[p]
		if !ok {
			status[p] = ProviderStatus{StatusTimeout, "enumeration timed out"}
			complete = false
			continue
		}
		if r.err != nil {
			kind := StatusFailure
			if errors.Is(r.err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
				kind = StatusTimeout
			}
			status[p] = ProviderStatus{kind, "enumeration did not complete"}
			complete = false
			continue
		}
		kind := StatusSuccess
		detail := "enumeration succeeded"
		if len(r.ids) == 0 {
			kind = StatusEmpty
			detail = "provider verified no models"
		}
		for _, id := range r.ids {
			choice := p + "/" + id
			if len(choices) >= captureMaxModels || choiceBytes+len(choice) > captureMaxBytes {
				r.limited = true
				continue
			}
			choices = append(choices, choice)
			choiceBytes += len(choice)
		}
		if r.limited {
			kind = StatusLimited
			detail = "enumeration exceeded safe capture bounds"
		}
		status[p] = ProviderStatus{kind, detail}
		if kind != StatusSuccess && kind != StatusEmpty {
			complete = false
		}
	}
	key := make([]byte, 32)
	_, _ = rand.Read(key)
	h := sha256.New()
	h.Write([]byte("model-snapshot-v2\x00"))
	h.Write([]byte{byte(len(providers))})
	if complete {
		h.Write([]byte{1})
	} else {
		h.Write([]byte{0})
	}
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
	return Snapshot{Version: version, Complete: complete, Choices: choices, Status: status, key: key}
}

func boundedModelIDs(ctx context.Context, provider string, models []llm.ModelInfo, visible func(string, llm.ModelInfo) bool) ([]string, bool, error) {
	seen := make(map[string]bool)
	ids := make([]string, 0, min(len(models), captureMaxModels))
	limited := false
	var bytes int
	for i, model := range models {
		if err := ctx.Err(); err != nil {
			return nil, false, err
		}
		if i >= captureMaxModels {
			limited = true
			break
		}
		id, ok := safeModelID(model.ID)
		if !ok {
			limited = true
			continue
		}
		model.ID = id
		if visible != nil && !visible(provider, model) {
			continue
		}
		if err := ctx.Err(); err != nil {
			return nil, false, err
		}
		if seen[id] {
			continue
		}
		choiceBytes := len(provider) + 1 + len(id)
		if bytes+choiceBytes > captureMaxBytes {
			limited = true
			break
		}
		seen[id] = true
		ids = append(ids, id)
		bytes += choiceBytes
	}
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	sort.Strings(ids)
	return ids, limited, nil
}

func boundedProviders(providers []string, requiredProvider string) ([]string, bool) {
	bounded := make([]string, 0, min(len(providers), captureMaxProviders))
	limited := false
	requiredPresent := false
	for _, provider := range providers {
		if !safeProviderName(provider) {
			limited = true
			continue
		}
		if provider == requiredProvider {
			requiredPresent = true
		}
		at, found := slices.BinarySearch(bounded, provider)
		if found {
			continue
		}
		if len(bounded) == captureMaxProviders {
			limited = true
			if at == len(bounded) {
				continue
			}
			bounded = bounded[:len(bounded)-1]
		}
		bounded = append(bounded, provider)
		copy(bounded[at+1:], bounded[at:len(bounded)-1])
		bounded[at] = provider
	}
	if requiredPresent && !slices.Contains(bounded, requiredProvider) {
		bounded[len(bounded)-1] = requiredProvider
		sort.Strings(bounded)
	}
	return bounded, limited
}

func safeProviderName(name string) bool {
	if name != strings.TrimSpace(name) || !safeIdentifier(name, captureMaxProviderLen) {
		return false
	}
	encoded, _ := json.Marshal(name)
	return len(encoded) == len(name)+2
}

func safeModelID(id string) (string, bool) {
	id = strings.TrimSpace(id)
	return id, safeIdentifier(id, captureMaxModelIDLen)
}

func safeIdentifier(value string, maxBytes int) bool {
	if value == "" || len(value) > maxBytes || !utf8.ValidString(value) {
		return false
	}
	for _, r := range value {
		if unicode.IsSpace(r) || unicode.IsControl(r) || unicode.In(r, unicode.Cf, unicode.Co, unicode.Cs) {
			return false
		}
	}
	return true
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
	if s == nil || len(s.key) == 0 || maxCount <= 0 || maxCount > DefaultPageMaxCount || maxBytes <= 0 || maxBytes > DefaultPageMaxBytes {
		return Page{}, errors.New("invalid page bounds")
	}
	off := 0
	if token != "" {
		c, e := s.decode(token)
		if e != nil {
			return Page{}, e
		}
		if c.Count != maxCount || c.Bytes != maxBytes {
			return Page{}, errors.New("stale cursor")
		}
		off = c.Offset
	}
	status := s.Status
	if status == nil {
		status = map[string]ProviderStatus{}
	}
	p := Page{Version: s.Version, Complete: s.Complete, Status: status, Provenance: "startup-frozen"}
	if off >= len(s.Choices) {
		p = s.pageAtOffset(p, off, maxCount, maxBytes)
		if p.SerializedBytes() > maxBytes {
			return Page{}, errors.New("page envelope budget too small")
		}
		return p, nil
	}
	for off < len(s.Choices) && len(p.Choices)+len(p.Oversized) < maxCount {
		trial := p
		trial.Choices = append(append([]string(nil), p.Choices...), s.Choices[off])
		trial = s.pageAtOffset(trial, off+1, maxCount, maxBytes)
		if trial.SerializedBytes() <= maxBytes {
			p = trial
			off++
			continue
		}
		if len(p.Choices)+len(p.Oversized) > 0 {
			break
		}
		trial = p
		trial.Oversized = append(append([]int(nil), p.Oversized...), off)
		trial = s.pageAtOffset(trial, off+1, maxCount, maxBytes)
		if trial.SerializedBytes() > maxBytes {
			return Page{}, errors.New("page envelope budget too small")
		}
		p = trial
		off++
	}
	if len(p.Choices) == 0 && len(p.Oversized) == 0 {
		return Page{}, errors.New("page envelope budget too small")
	}
	if p.SerializedBytes() > maxBytes {
		return Page{}, errors.New("page envelope budget too small")
	}
	return p, nil
}

func (s *Snapshot) pageAtOffset(p Page, off, maxCount, maxBytes int) Page {
	p.Next = ""
	p.Terminal = off >= len(s.Choices)
	if !p.Terminal {
		p.Next = s.token(cursor{"model-list-v1", "root", s.Version, off, maxCount, maxBytes})
	}
	return p
}

func (p Page) SerializedBytes() int { b, _ := json.MarshalIndent(p, "", "  "); return len(b) }
