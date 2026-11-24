package hub

import (
	"container/list"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"golang.org/x/sync/singleflight"
	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/appsource"
	"primeradiant.com/evener/cmd/evener-hub/internal/fspaths"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/envvars"
)

const (
	fileSearchDefaultLimit         = 25
	fileSearchMaxLimit             = 50
	fileSearchMaxQueryRunes        = 256
	fileSearchLocalWalkTimeout     = 1500 * time.Millisecond
	fileSearchRemoteForwardTimeout = 3000 * time.Millisecond
	fileSearchCacheTTL             = 5 * time.Second
	fileSearchCacheCapacity        = 8
)

type fileSearchCacheKey struct {
	root            string
	includeDotfiles bool
}

type fileSearchCacheValue struct {
	items     []appwire.FileSearchItem
	truncated bool
}

type fileSearchCacheEntry struct {
	key     fileSearchCacheKey
	value   fileSearchCacheValue
	builtAt time.Time
}

// fileSearchService serves evener/files/search with live sessionCWD resolution,
// single-hop remote session forwarding, a 5s canonical-root LRU cache, and
// singleflight walk deduplication.
type fileSearchService struct {
	cfg         hubcore.WebConfig
	sources     *appsource.Registry
	ignoreCache *execenv.ProjectFilesIgnoreCache
	now         func() time.Time
	homeDir     func() string
	walk        func(context.Context, string, execenv.ProjectFilesOptions) (execenv.ProjectFilesResult, error)

	sf    singleflight.Group
	mu    sync.Mutex
	lru   *list.List
	cache map[fileSearchCacheKey]*list.Element
}

func newFileSearchService(cfg hubcore.WebConfig, sources *appsource.Registry) *fileSearchService {
	return &fileSearchService{
		cfg:         cfg,
		sources:     sources,
		ignoreCache: execenv.NewProjectFilesIgnoreCache(),
		now:         time.Now,
		homeDir:     envvars.Home.Getenv,
		walk:        execenv.WalkProjectFiles,
		lru:         list.New(),
		cache:       make(map[fileSearchCacheKey]*list.Element, fileSearchCacheCapacity),
	}
}

func (s *fileSearchService) Search(ctx context.Context, params appwire.FilesSearchParams) (appwire.FilesSearchResponse, error) {
	if err := ctx.Err(); err != nil {
		return appwire.FilesSearchResponse{}, err
	}
	ref := strings.TrimSpace(params.Ref)
	cwd := strings.TrimSpace(params.CWD)
	if (ref == "" && cwd == "") || (ref != "" && cwd != "") {
		return appwire.FilesSearchResponse{}, appwire.InvalidParams("exactly one of ref or cwd must be set")
	}

	if utf8.RuneCountInString(params.Query) > fileSearchMaxQueryRunes {
		return appwire.FilesSearchResponse{}, appwire.InvalidParams("query exceeds maximum length")
	}
	query := stripASCIIControlChars(params.Query)

	limit := params.Limit
	if limit <= 0 {
		limit = fileSearchDefaultLimit
	} else if limit > fileSearchMaxLimit {
		limit = fileSearchMaxLimit
	}

	var canonicalRoot string
	if ref != "" {
		parsed, err := appwire.ParseRef(ref)
		if err != nil {
			return appwire.FilesSearchResponse{}, appwire.InvalidParams("invalid ref")
		}
		if parsed.SourceID != appwire.LocalSourceID {
			if err := refuseRemoteOrigin(ctx, "forward remote file search"); err != nil {
				return appwire.FilesSearchResponse{}, err
			}
			remote, err := attachedRemoteHostSource(s.sources, parsed.SourceID)
			if err != nil {
				return appwire.FilesSearchResponse{}, err
			}
			fwdCtx, cancel := context.WithTimeout(ctx, fileSearchRemoteForwardTimeout)
			defer cancel()
			return remote.SearchFiles(fwdCtx, appwire.FilesSearchParams{
				Ref:   appwire.LocalRef(parsed.ThreadID).String(),
				Query: query,
				Limit: limit,
			})
		}
		dir, err := sessionCWD(ctx, s.cfg, s.sources, parsed.ThreadID)
		if err != nil {
			return appwire.FilesSearchResponse{}, err
		}
		resolved, err := fspaths.CanonicalizeDir(dir)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return emptyFilesSearchResponse(), nil
			}
			return appwire.FilesSearchResponse{}, appwire.SessionUnavailable("session working directory unavailable")
		}
		canonicalRoot = resolved
	} else {
		expanded := cwd
		if expanded == "~" || strings.HasPrefix(expanded, "~/") {
			home := strings.TrimSpace(s.homeDir())
			if home == "" {
				return appwire.FilesSearchResponse{}, appwire.InvalidParams("home directory is not set")
			}
			expanded = filepath.Join(home, strings.TrimPrefix(expanded, "~"))
		}
		resolved, err := fspaths.CanonicalizeDir(expanded)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return emptyFilesSearchResponse(), nil
			}
			return appwire.FilesSearchResponse{}, appwire.InvalidParams(err.Error())
		}
		canonicalRoot = resolved
	}

	normQuery := appwire.NormalizeFileSearchQuery(query)
	includeDotfiles := appwire.FileSearchQueryIncludesDotfiles(normQuery)
	key := fileSearchCacheKey{root: canonicalRoot, includeDotfiles: includeDotfiles}

	if cached, ok := s.getCached(key); ok {
		return rankFilesSearchResult(cached, normQuery, limit), nil
	}

	sfKey := strconv.FormatBool(includeDotfiles) + "\x00" + canonicalRoot
	ch := s.sf.DoChan(sfKey, func() (any, error) {
		if cached, ok := s.getCached(key); ok {
			return cached, nil
		}
		walkCtx, cancel := context.WithTimeout(context.Background(), fileSearchLocalWalkTimeout)
		defer cancel()
		walked, err := s.walk(walkCtx, canonicalRoot, execenv.ProjectFilesOptions{
			IncludeDotfiles: includeDotfiles,
			HomeDir:         s.homeDir(),
			IgnoreCache:     s.ignoreCache,
		})
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return fileSearchCacheValue{items: []appwire.FileSearchItem{}}, nil
			}
			return nil, err
		}
		items := make([]appwire.FileSearchItem, len(walked.Entries))
		for i, entry := range walked.Entries {
			kind := "file"
			if entry.IsDir {
				kind = "dir"
			}
			items[i] = appwire.FileSearchItem{
				Path: entry.Path,
				Kind: kind,
			}
		}
		val := fileSearchCacheValue{items: items, truncated: walked.Truncated}
		s.putCached(key, val)
		return val, nil
	})

	select {
	case <-ctx.Done():
		return appwire.FilesSearchResponse{}, ctx.Err()
	case res := <-ch:
		if res.Err != nil {
			return appwire.FilesSearchResponse{}, res.Err
		}
		val := res.Val.(fileSearchCacheValue)
		return rankFilesSearchResult(val, normQuery, limit), nil
	}
}

func (s *fileSearchService) getCached(key fileSearchCacheKey) (fileSearchCacheValue, bool) {
	now := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()
	elem, ok := s.cache[key]
	if !ok {
		return fileSearchCacheValue{}, false
	}
	entry := elem.Value.(*fileSearchCacheEntry)
	if now.Sub(entry.builtAt) >= fileSearchCacheTTL {
		s.lru.Remove(elem)
		delete(s.cache, key)
		return fileSearchCacheValue{}, false
	}
	s.lru.MoveToFront(elem)
	return entry.value, true
}

func (s *fileSearchService) putCached(key fileSearchCacheKey, val fileSearchCacheValue) {
	builtAt := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()
	if elem, ok := s.cache[key]; ok {
		entry := elem.Value.(*fileSearchCacheEntry)
		entry.value = val
		entry.builtAt = builtAt
		s.lru.MoveToFront(elem)
		return
	}
	if s.lru.Len() >= fileSearchCacheCapacity {
		oldest := s.lru.Back()
		if oldest != nil {
			s.lru.Remove(oldest)
			delete(s.cache, oldest.Value.(*fileSearchCacheEntry).key)
		}
	}
	elem := s.lru.PushFront(&fileSearchCacheEntry{
		key:     key,
		value:   val,
		builtAt: builtAt,
	})
	s.cache[key] = elem
}

func rankFilesSearchResult(val fileSearchCacheValue, normQuery string, limit int) appwire.FilesSearchResponse {
	return appwire.FilesSearchResponse{
		Items:     appwire.RankFileSearchItems(val.items, normQuery, limit),
		Truncated: val.truncated,
	}
}

func emptyFilesSearchResponse() appwire.FilesSearchResponse {
	return appwire.FilesSearchResponse{Items: []appwire.FileSearchItem{}}
}

func stripASCIIControlChars(s string) string {
	hasCtrl := false
	for i := 0; i < len(s); i++ {
		if s[i] < 0x20 || s[i] == 0x7f {
			hasCtrl = true
			break
		}
	}
	if !hasCtrl {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}
