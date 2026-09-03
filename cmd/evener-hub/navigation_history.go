package hub

import (
	"container/list"
	"encoding/json"
	"errors"
	"fmt"
	"sync"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/hubapi"
)

type navigationHistoryEntry struct {
	view  string
	base  appwire.NavigationReadBase
	data  []byte
	gone  bool
	bytes int64
}

type navigationHistory struct {
	mu         sync.Mutex
	maxEntries int
	maxBytes   int64
	bytes      int64
	order      *list.List
	entries    map[string]*list.Element
}

func newNavigationHistory(maxEntries int, maxBytes int64) *navigationHistory {
	return &navigationHistory{maxEntries: maxEntries, maxBytes: maxBytes, order: list.New(), entries: map[string]*list.Element{}}
}
func (h *navigationHistory) key(view navigationResourceKey, base appwire.NavigationReadBase) string {
	return fmt.Sprintf("%s|%s|%d|%s", view.View().String(), base.GenerationID, base.Revision, base.ETag)
}
func (h *navigationHistory) Remember(view navigationResourceKey, version appwire.NavigationReadBase, snapshot *hubapi.NavigationSnapshot, gone bool) error {
	if version.GenerationID == "" || version.ETag == "" {
		return errors.New("invalid navigation history version")
	}
	var data []byte
	if snapshot != nil {
		if err := validateNavigationResourceSnapshot(view, version.GenerationID, version.Revision, *snapshot); err != nil {
			return err
		}
		encoded, err := json.Marshal(snapshot)
		if err != nil {
			return err
		}
		data = encoded
	} else if !gone {
		return errors.New("navigation history snapshot is nil")
	}
	size := int64(len(data)) + int64(len(version.GenerationID)+len(version.ETag))
	if size < 0 {
		return errors.New("navigation history size overflow")
	}
	entry := navigationHistoryEntry{view: view.View().String(), base: version, data: append([]byte(nil), data...), gone: gone, bytes: size}
	key := h.key(view, version)
	h.mu.Lock()
	defer h.mu.Unlock()
	if old, ok := h.entries[key]; ok {
		h.bytes -= old.Value.(navigationHistoryEntry).bytes
		h.order.Remove(old)
		delete(h.entries, key)
	}
	if h.maxEntries <= 0 || size > h.maxBytes {
		return nil
	}
	for h.order.Len() >= h.maxEntries || size > h.maxBytes-h.bytes {
		old := h.order.Back()
		if old == nil {
			break
		}
		value := old.Value.(navigationHistoryEntry)
		h.bytes -= value.bytes
		delete(h.entries, h.keyFromEntry(value))
		h.order.Remove(old)
	}
	element := h.order.PushFront(entry)
	h.entries[key] = element
	h.bytes += size
	return nil
}
func (h *navigationHistory) keyFromEntry(entry navigationHistoryEntry) string {
	return fmt.Sprintf("%s|%s|%d|%s", entry.view, entry.base.GenerationID, entry.base.Revision, entry.base.ETag)
}
func (h *navigationHistory) Lookup(view navigationResourceKey, base appwire.NavigationReadBase) (hubapi.NavigationSnapshot, bool) {
	key := h.key(view, base)
	h.mu.Lock()
	defer h.mu.Unlock()
	element, ok := h.entries[key]
	if !ok {
		return hubapi.NavigationSnapshot{}, false
	}
	h.order.MoveToFront(element)
	entry := element.Value.(navigationHistoryEntry)
	if entry.gone {
		return hubapi.NavigationSnapshot{}, true
	}
	var snapshot hubapi.NavigationSnapshot
	if json.Unmarshal(entry.data, &snapshot) != nil {
		return hubapi.NavigationSnapshot{}, false
	}
	return snapshot, true
}
