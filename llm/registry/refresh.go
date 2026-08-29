package registry

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

// UpstreamURL is where the catalog comes from (spec §6.4).
const UpstreamURL = "https://models.dev/api.json"

const (
	cacheMaxAge  = 24 * time.Hour
	minKeepRatio = 0.9
)

// cachePaths returns the runtime cache files under <state-root>/catalog/.
func cachePaths(stateRoot string) (jsonPath, metaPath string) {
	dir := filepath.Join(stateRoot, "catalog")
	return filepath.Join(dir, "models.dev.json"), filepath.Join(dir, "models.dev.meta.json")
}

// readCache returns the cached snapshot and its meta when both exist and
// parse. Validation against the converter happens in Load.
func readCache(stateRoot string) ([]byte, Meta, bool) {
	jsonPath, metaPath := cachePaths(stateRoot)
	raw, err := os.ReadFile(jsonPath)
	if err != nil {
		return nil, Meta{}, false
	}
	metaRaw, err := os.ReadFile(metaPath)
	if err != nil {
		return nil, Meta{}, false
	}
	meta, err := ParseMeta(metaRaw)
	if err != nil {
		return nil, Meta{}, false
	}
	return raw, meta, true
}

func writeAtomic(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmp.Name())
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// HTTPFetcher returns a Fetcher that GETs UpstreamURL with If-None-Match.
func HTTPFetcher(client *http.Client) Fetcher {
	return func(ctx context.Context, etag string) ([]byte, string, bool, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, UpstreamURL, nil)
		if err != nil {
			return nil, "", false, err
		}
		if etag != "" {
			req.Header.Set("If-None-Match", etag)
		}
		resp, err := client.Do(req)
		if err != nil {
			return nil, "", false, err
		}
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode == http.StatusNotModified {
			return nil, etag, true, nil
		}
		if resp.StatusCode != http.StatusOK {
			return nil, "", false, fmt.Errorf("models.dev: HTTP %d", resp.StatusCode)
		}
		body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
		if err != nil {
			return nil, "", false, err
		}
		return body, resp.Header.Get("ETag"), false, nil
	}
}

// RefreshOptions configures Refresh. Baseline is the snapshot the sanity
// floors compare against (the embedded one when nil).
type RefreshOptions struct {
	StateRoot string
	Fetcher   Fetcher
	Force     bool
	Now       func() time.Time
	Baseline  []byte
}

// RefreshResult reports what Refresh did.
type RefreshResult struct {
	Skipped         bool
	NotModified     bool
	Updated         bool
	Path            string
	Etag            string
	ProvidersBefore int
	ProvidersAfter  int
	ModelsBefore    int
	ModelsAfter     int
}

func countCatalog(raw []byte) (providers, models int, err error) {
	provs, err := FromModelsDev(raw)
	if err != nil {
		return 0, 0, err
	}
	for _, p := range provs {
		models += len(p.Models)
	}
	return len(provs), models, nil
}

// Refresh fetches models.dev into the cache (spec §6.4): skipped when the
// cache is fresh and Force is false; a 304 only bumps fetched_at; a new
// body must convert, keep ≥ 90% of the baseline's providers and models, and
// load under the curated overlay before it is written atomically. A
// failure leaves the previous cache untouched.
func Refresh(ctx context.Context, opts RefreshOptions) (RefreshResult, error) {
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	if opts.Fetcher == nil {
		return RefreshResult{}, errors.New("refresh: no fetcher")
	}
	jsonPath, metaPath := cachePaths(opts.StateRoot)
	res := RefreshResult{Path: jsonPath}
	_, meta, cached := readCache(opts.StateRoot)
	if cached && !opts.Force && now().Sub(meta.FetchedAt) < cacheMaxAge {
		res.Skipped = true
		return res, nil
	}
	baseline := opts.Baseline
	if baseline == nil {
		var err error
		if baseline, _, err = EmbeddedSnapshot(); err != nil {
			return res, err
		}
	}
	var err error
	if res.ProvidersBefore, res.ModelsBefore, err = countCatalog(baseline); err != nil {
		return res, fmt.Errorf("refresh: baseline: %w", err)
	}
	etag := ""
	if cached {
		etag = meta.Etag
	}
	body, newEtag, notModified, err := opts.Fetcher(ctx, etag)
	if err != nil {
		return res, fmt.Errorf("refresh: fetch: %w", err)
	}
	if notModified && cached {
		meta.FetchedAt = now()
		metaRaw, _ := json.MarshalIndent(meta, "", "  ")
		if err := writeAtomic(metaPath, append(metaRaw, '\n')); err != nil {
			return res, err
		}
		res.NotModified, res.Etag = true, meta.Etag
		res.ProvidersAfter, res.ModelsAfter = res.ProvidersBefore, res.ModelsBefore
		return res, nil
	}
	if res.ProvidersAfter, res.ModelsAfter, err = countCatalog(body); err != nil {
		return res, fmt.Errorf("refresh: rejected: %w", err)
	}
	if float64(res.ProvidersAfter) < float64(res.ProvidersBefore)*minKeepRatio || float64(res.ModelsAfter) < float64(res.ModelsBefore)*minKeepRatio {
		return res, fmt.Errorf("refresh: rejected: upstream shrank to %d providers / %d models (baseline %d / %d)", res.ProvidersAfter, res.ModelsAfter, res.ProvidersBefore, res.ModelsBefore)
	}
	if _, err := Load(WithSnapshot(body), WithoutCache(), WithNoUserLayer(), WithOffline(true), WithEnv(func(string) (string, bool) { return "", false }), WithStateRoot(opts.StateRoot)); err != nil {
		return res, fmt.Errorf("refresh: rejected: overlay does not load on the new snapshot: %w", err)
	}
	newMeta := Meta{FetchedAt: now(), Etag: newEtag, Source: UpstreamURL}
	metaRaw, _ := json.MarshalIndent(newMeta, "", "  ")
	if err := writeAtomic(jsonPath, body); err != nil {
		return res, err
	}
	if err := writeAtomic(metaPath, append(metaRaw, '\n')); err != nil {
		return res, err
	}
	res.Updated, res.Etag = true, newEtag
	return res, nil
}
