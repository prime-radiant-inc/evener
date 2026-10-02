package agent

import (
	"container/list"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"primeradiant.com/evener/agent/internal/delegatestore"
	"primeradiant.com/evener/agent/internal/jobstore"
	"primeradiant.com/evener/appwire"
)

const sessionActivityPageBytes = 256 << 10
const sessionActivityCacheRoots = 32
const sessionActivityWalks = 128

type sessionActivityKey struct {
	At              string
	ID              string
	SourceSessionID string
}

func (key sessionActivityKey) before(other sessionActivityKey) bool {
	if key.At != other.At {
		return key.At < other.At
	}
	if key.ID != other.ID {
		return key.ID < other.ID
	}
	return key.SourceSessionID < other.SourceSessionID
}
func sessionActivityCreationKey(at time.Time, id string) sessionActivityKey {
	return sessionActivityKey{At: at.UTC().Format("2006-01-02T15:04:05.000000000Z"), ID: id}
}

type sessionActivityToken struct {
	Epoch     string
	SessionID string
	Resource  appwire.SessionActivityResource
	Scope     appwire.SessionActivityScope
	Walk      string
	After     sessionActivityKey
	Progress  uint64
}
type sessionActivityWalk struct {
	UnavailableSources map[string]bool
	Issues             []appwire.SessionActivityIssue
	Cutoffs            map[string]int64
	Highwater          sessionActivityKey
	Admission          uint64
	Ready              bool
	Owners             []string
	SourcesReady       bool
	SourcePosition     int
}
type sessionActivitySource struct {
	Info   os.FileInfo
	Tail   []byte
	Offset int64
}
type sessionActivityJobIndex struct {
	Version uint64

	Pending         []jobstore.Event
	PendingEnds     []int64
	PendingComplete bool
	PendingPosition int

	Source          sessionActivitySource
	Cursor          jobstore.PageCursor
	Jobs            map[string]*jobstore.JobRecord
	Watches         map[string]*jobstore.WatchRecord
	Configs         map[string]*jobstore.WatchConfigSnapshot
	Created         map[string]time.Time
	CreationOffsets map[string]int64
	JobKeys         []sessionActivityKey
	WatchKeys       []sessionActivityKey
	Delivered       map[string]bool
	DeliveryTimes   map[string][]string
	DeliveryCounts  map[string]int
	Ended           []string
	Complete        bool
	// Established proves a complete fold in the current source incarnation,
	// even while a later append is being folded in bounded steps.
	Established bool
}
type sessionActivityIndex struct {
	scanCalls uint64
	rawBytes  uint64

	revision atomic.Uint64

	delegatePending         []delegatestore.Event
	delegatePendingEnds     []int64
	delegatePendingComplete bool
	delegatePendingPosition int
	delegateSequence        uint64

	gate             chan struct{}
	key              string
	epoch            string
	secret           [32]byte
	element          *list.Element
	controller       *delegateTreeController
	delegateSource   sessionActivitySource
	delegateCursor   delegatestore.PageCursor
	delegates        delegatestore.State
	delegateKeys     []sessionActivityKey
	delegateOffsets  map[string]int64
	delegateComplete bool
	jobs             map[string]*sessionActivityJobIndex
	summaryOwner     string
	walks            map[string]*sessionActivityWalk
	walkOrder        []string
	progress         uint64
}

var sessionActivityIndexes = struct {
	sync.Mutex
	entries map[string]*sessionActivityIndex
	order   list.List
}{entries: make(map[string]*sessionActivityIndex)}

func sessionActivityRandomID() string {
	var value [18]byte
	_, _ = rand.Read(value[:])
	return base64.RawURLEncoding.EncodeToString(value[:])
}
func acquireSessionActivityIndex(ctx context.Context, key string, controller *delegateTreeController) (*sessionActivityIndex, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	sessionActivityIndexes.Lock()
	index := sessionActivityIndexes.entries[key]
	if index == nil || index.controller != controller {
		if index != nil {
			sessionActivityIndexes.order.Remove(index.element)
		}
		index = &sessionActivityIndex{gate: make(chan struct{}, 1), key: key, controller: controller}
		index.reset()
		index.element = sessionActivityIndexes.order.PushFront(index)
		sessionActivityIndexes.entries[key] = index
	} else {
		sessionActivityIndexes.order.MoveToFront(index.element)
	}
	for len(sessionActivityIndexes.entries) > sessionActivityCacheRoots {
		oldest := sessionActivityIndexes.order.Back()
		cached := oldest.Value.(*sessionActivityIndex)
		delete(sessionActivityIndexes.entries, cached.key)
		sessionActivityIndexes.order.Remove(oldest)
	}
	sessionActivityIndexes.Unlock()
	select {
	case index.gate <- struct{}{}:
		return index, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}
func (index *sessionActivityIndex) release() { <-index.gate }
func (index *sessionActivityIndex) reset() {
	index.epoch = sessionActivityRandomID()
	_, _ = rand.Read(index.secret[:])
	index.delegateSource = sessionActivitySource{}
	index.delegateCursor = delegatestore.PageCursor{}
	index.delegatePending = nil
	index.delegatePendingEnds = nil
	index.delegatePendingComplete = false
	index.delegatePendingPosition = 0
	index.delegateSequence = 0
	index.delegateKeys = nil
	index.delegates = make(delegatestore.State)
	index.delegateOffsets = make(map[string]int64)
	index.delegateComplete = false
	index.jobs = make(map[string]*sessionActivityJobIndex)
	index.summaryOwner = ""
	index.walks = make(map[string]*sessionActivityWalk)
	index.walkOrder = nil
	index.progress = 0
}
func (index *sessionActivityIndex) token(params appwire.SessionActivityListParams, resource appwire.SessionActivityResource, sessionID string) (sessionActivityToken, *sessionActivityWalk, error) {
	token := sessionActivityToken{Epoch: index.epoch, SessionID: sessionID, Resource: resource, Scope: params.Scope}
	if params.Cursor != "" {
		raw, err := base64.RawURLEncoding.DecodeString(params.Cursor)
		if err != nil || len(raw) < sha256.Size || len(raw) > activityMaxTokenBytes {
			return token, nil, appwire.InvalidParams("invalid session activity cursor")
		}
		if err = json.Unmarshal(raw[sha256.Size:], &token); err != nil {
			return token, nil, appwire.InvalidParams("invalid session activity cursor")
		}
		if token.SessionID != sessionID || token.Resource != resource || token.Scope != params.Scope {
			return token, nil, appwire.InvalidParams("cursor belongs to another session, resource or scope")
		}
		if token.Epoch != index.epoch {
			return token, nil, appwire.SessionActivityCursorStale()
		}
		mac := hmac.New(sha256.New, index.secret[:])
		_, _ = mac.Write(raw[sha256.Size:])
		if !hmac.Equal(raw[:sha256.Size], mac.Sum(nil)) {
			return token, nil, appwire.InvalidParams("invalid session activity cursor")
		}
		walk := index.walks[token.Walk]
		if walk == nil {
			return token, nil, appwire.SessionActivityCursorStale()
		}
		return token, walk, nil
	}
	token.Walk = sessionActivityRandomID()
	walk := &sessionActivityWalk{Cutoffs: make(map[string]int64)}
	index.walks[token.Walk] = walk
	index.walkOrder = append(index.walkOrder, token.Walk)
	if len(index.walkOrder) > sessionActivityWalks {
		delete(index.walks, index.walkOrder[0])
		index.walkOrder = index.walkOrder[1:]
	}
	return token, walk, nil
}
func (index *sessionActivityIndex) encode(token sessionActivityToken) string {
	token.Progress = index.progress
	raw, _ := json.Marshal(token)
	mac := hmac.New(sha256.New, index.secret[:])
	_, _ = mac.Write(raw)
	return base64.RawURLEncoding.EncodeToString(append(mac.Sum(nil), raw...))
}

// Source checks follow the existing foldcache incarnation rule: file identity,
// shrinkage, and a small tail probe. Ordinary appends retain the epoch. Like
// foldcache, an earlier rewrite reproducing the sampled tail is not detected.
func (index *sessionActivityIndex) checkSource(path string, source *sessionActivitySource) (os.FileInfo, error) {
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		if source.Info != nil {
			index.reset()
			return nil, appwire.SessionActivityCursorStale()
		}
		return nil, nil
	}
	if err != nil {
		return nil, sessionActivitySourceReadError("session activity source unavailable", err)
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil {
		return nil, sessionActivitySourceReadError("session activity source unavailable", err)
	}
	if info.IsDir() {
		return nil, sessionActivitySourceUnavailable("session activity source is not readable as a file")
	}
	changed := source.Info != nil && (!os.SameFile(source.Info, info) || info.Size() < source.Offset)
	if !changed && len(source.Tail) > 0 {
		probe := make([]byte, len(source.Tail))
		n, readErr := file.ReadAt(probe, source.Offset-int64(len(probe)))
		index.rawBytes += uint64(n)
		if _, pathError := errors.AsType[*os.PathError](readErr); pathError {
			return nil, sessionActivitySourceReadError("session activity source unavailable", readErr)
		}
		changed = readErr != nil || n != len(probe) || !hmac.Equal(probe, source.Tail)
	}
	if changed {
		index.reset()
		return nil, appwire.SessionActivityCursorStale()
	}
	source.Info = info
	return info, nil
}
func (read *sessionActivityRead) captureTail(path string, source *sessionActivitySource, offset int64) error {
	count, err := captureSessionActivityTail(path, source, offset)
	read.index.rawBytes += uint64(count)
	if err == nil {
		return nil
	}
	if errors.Is(err, os.ErrNotExist) || errors.Is(err, io.EOF) {
		read.index.reset()
		return appwire.SessionActivityCursorStale()
	}
	return sessionActivitySourceReadError("session activity source unavailable", err)
}
func captureSessionActivityTail(path string, source *sessionActivitySource, offset int64) (int, error) {
	n := min(offset, 64)
	if n == 0 {
		source.Offset = offset
		source.Tail = nil
		return 0, nil
	}
	file, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer func() { _ = file.Close() }()
	tail := make([]byte, int(n))
	count, err := file.ReadAt(tail, offset-n)
	if err == nil {
		source.Offset = offset
		source.Tail = tail
	}
	return count, err
}
