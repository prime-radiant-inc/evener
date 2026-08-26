// Package modelavailability captures and serves the startup-frozen model choices
// advertised by delegate.model. It deliberately has no provider knowledge: callers
// supply the already-resolved provider-instance fetch function.
package modelavailability

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
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
	Kind   StatusKind
	Detail string
}
type Snapshot struct {
	Version  string
	Complete bool
	Choices  []string
	Status   map[string]ProviderStatus
	key      []byte
	mu       *sync.Mutex
	used     map[string]bool
}
type Page struct {
	Choices  []string
	Next     string
	Version  string
	Complete bool
	Status   map[string]ProviderStatus
}

// Inline limits keep the delegate schema prompt bounded; larger snapshots use
// the read-only paginated surface instead of truncating a complete catalog.
const (
	DefaultInlineMaxCount = 128
	DefaultInlineMaxBytes = 4096
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
		go func(name string) { m, e := fetch(ctx, name); out <- result{name, m, e} }(name)
	}
	status := map[string]ProviderStatus{}
	set := map[string]bool{}
	for received := 0; received < len(providers); received++ {
		var r result
		select {
		case r = <-out:
		case <-ctx.Done():
			// A provider that ignores context must not extend startup past the
			// single overall budget. Its result is deliberately not trusted.
			received = len(providers)
			for _, p := range providers {
				if _, ok := status[p]; !ok {
					status[p] = ProviderStatus{StatusTimeout, "model enumeration timed out"}
				}
			}
		}
		if r.name == "" {
			break
		}
		if r.err != nil {
			k := StatusFailure
			if errors.Is(r.err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
				k = StatusTimeout
			}
			status[r.name] = ProviderStatus{k, "model enumeration did not complete"}
			continue
		}
		if len(r.models) == 0 {
			status[r.name] = ProviderStatus{StatusEmpty, "provider verified no models"}
		} else {
			status[r.name] = ProviderStatus{StatusSuccess, "provider model enumeration succeeded"}
		}
		for _, m := range r.models {
			if strings.TrimSpace(m.ID) != "" {
				set[r.name+"/"+strings.TrimSpace(m.ID)] = true
			}
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
	h.Write([]byte("model-snapshot-v1\x00"))
	for _, c := range choices {
		h.Write([]byte(c))
		h.Write([]byte{0})
	}
	version := hex.EncodeToString(h.Sum(nil)[:12])
	return Snapshot{Version: version, Complete: complete, Choices: choices, Status: status, key: key, mu: &sync.Mutex{}, used: map[string]bool{}}
}

func (s Snapshot) Inline(maxCount, maxBytes int) (string, bool) {
	if !s.Complete || len(s.Choices) > maxCount {
		return "", false
	}
	text := "Verified startup snapshot " + s.Version + ": " + strings.Join(s.Choices, ", ") + "."
	if len([]byte(text)) > maxBytes {
		return "", false
	}
	return text, true
}

type cursor struct {
	Version string
	Offset  int
	Nonce   uint64
}

func (s *Snapshot) Page(token string, maxCount, maxBytes int) (Page, error) {
	if s == nil || s.mu == nil || maxCount <= 0 || maxBytes <= 0 {
		return Page{}, errors.New("invalid page bounds")
	}
	off := 0
	if token != "" {
		raw, e := base64.RawURLEncoding.DecodeString(token)
		if e != nil || len(raw) <= 32 {
			return Page{}, errors.New("invalid cursor")
		}
		payload, mac := raw[:len(raw)-32], raw[len(raw)-32:]
		sum := sha256.Sum256(append(append([]byte(nil), s.key...), payload...))
		if subtle.ConstantTimeCompare(mac, sum[:]) != 1 {
			return Page{}, errors.New("invalid cursor")
		}
		var c cursor
		if json.Unmarshal(payload, &c) != nil || c.Version != s.Version || c.Offset < 0 || c.Offset > len(s.Choices) {
			return Page{}, errors.New("stale cursor")
		}
		token = string(raw)
		s.mu.Lock()
		if s.used[token] {
			s.mu.Unlock()
			return Page{}, errors.New("cursor already used")
		}
		s.used[token] = true
		s.mu.Unlock()
		off = c.Offset
	}
	end := off
	used := 0
	for end < len(s.Choices) && used+len([]byte(s.Choices[end]))+func() int {
		if end > off {
			return 1
		}
		return 0
	}() <= maxBytes && end-off < maxCount {
		if end > off {
			used++
		}
		used += len([]byte(s.Choices[end]))
		end++
	}
	if end == off {
		return Page{}, errors.New("model choice exceeds byte budget")
	}
	p := Page{Choices: append([]string(nil), s.Choices[off:end]...), Version: s.Version, Complete: s.Complete, Status: s.Status}
	if end < len(s.Choices) {
		c, _ := json.Marshal(cursor{s.Version, end, 0})
		sum := sha256.Sum256(append(append([]byte(nil), s.key...), c...))
		p.Next = base64.RawURLEncoding.EncodeToString(append(c, sum[:]...))
	}
	return p, nil
}
