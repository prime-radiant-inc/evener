package memory

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	topicLimit     = 65536
	indexLimit     = 16384
	operationLimit = 16
	submittedLimit = 262144
	referenceLimit = 16
)

var pageIDPattern = regexp.MustCompile(`^[a-z][a-z0-9]*(?:-[a-z0-9]+)*$`)
var operationIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`)

func validPageID(id string) bool {
	if len(id) < 1 || len(id) > 64 || !pageIDPattern.MatchString(id) {
		return false
	}
	switch id {
	case "index", "metadata", "receipts", "changes", "pending", "lock":
		return false
	}
	return true
}
func invalidInput() *Error { return &Error{Code: "invalid_input"} }
func limitError(kind string, actual, maximum int) *Error {
	return &Error{Code: "limit", Limit: &LimitDetail{Kind: kind, Actual: actual, Maximum: maximum}}
}
func hashBytes(data []byte) string { sum := sha256.Sum256(data); return hex.EncodeToString(sum[:]) }
func after(name string, data []byte) afterImage {
	return afterImage{Name: name, Bytes: data, Hash: hashBytes(data)}
}

// buildBatch computes content after-images only. Store owns the trusted scope,
// scoped raw-request digest, receipt lookup and receipt/change-file publication.
// No caller-supplied field is promoted into scope authority here.
func buildBatch(current Snapshot, request ApplyRequest, actor Actor, now time.Time) (pendingTransaction, bool, error) {
	tx, changed, err := planBatch(current, request, actor, now.UTC())
	if e, ok := err.(*Error); ok {
		e.Revision = current.Meta.Revision
	}
	return tx, changed, err
}
func planBatch(current Snapshot, request ApplyRequest, actor Actor, now time.Time) (pendingTransaction, bool, error) {
	if !operationIDPattern.MatchString(request.OperationID) {
		return pendingTransaction{}, false, invalidInput()
	}
	if request.ExpectedRevision != current.Meta.Revision {
		return pendingTransaction{}, false, &Error{Code: "conflict"}
	}
	if len(request.Pages) > operationLimit {
		return pendingTransaction{}, false, limitError("operations", len(request.Pages), operationLimit)
	}
	submitted := 0
	if request.Index != nil {
		submitted = len(*request.Index)
	}
	for _, operation := range request.Pages {
		if operation.Body != nil {
			submitted += len(*operation.Body)
		}
	}
	if submitted > submittedLimit {
		return pendingTransaction{}, false, limitError("submitted", submitted, submittedLimit)
	}
	operations := map[string]PageOperation{}
	for _, operation := range request.Pages {
		if !validPageID(operation.PageID) {
			return pendingTransaction{}, false, invalidInput()
		}
		if _, duplicate := operations[operation.PageID]; duplicate {
			return pendingTransaction{}, false, invalidInput()
		}
		switch operation.Kind {
		case "put":
			if operation.Body == nil || !utf8.ValidString(*operation.Body) {
				return pendingTransaction{}, false, invalidInput()
			}
			if len(*operation.Body) > topicLimit {
				return pendingTransaction{}, false, limitError("topic", len(*operation.Body), topicLimit)
			}
		case "delete", "review":
			if operation.Body != nil {
				return pendingTransaction{}, false, invalidInput()
			}
			if _, exists := current.Pages[operation.PageID]; !exists {
				return pendingTransaction{}, false, &Error{Code: "missing_page"}
			}
		default:
			return pendingTransaction{}, false, invalidInput()
		}
		operations[operation.PageID] = operation
	}
	bodies := map[string]string{}
	for id, page := range current.Pages {
		bodies[id] = page.Body
	}
	for id, operation := range operations {
		switch operation.Kind {
		case "put":
			bodies[id] = *operation.Body
		case "delete":
			delete(bodies, id)
		}
	}
	source := current.Index
	if request.Index != nil {
		source = *request.Index
	}
	document, err := parseIndex(source)
	if err != nil {
		return pendingTransaction{}, false, err
	}
	entries := map[string]indexEntry{}
	for _, entry := range document.Entries {
		if _, duplicate := entries[entry.PageID]; duplicate {
			return pendingTransaction{}, false, invalidInput()
		}
		if _, exists := bodies[entry.PageID]; !exists {
			return pendingTransaction{}, false, invalidInput()
		}
		entries[entry.PageID] = entry
	}
	if len(entries) != len(bodies) {
		return pendingTransaction{}, false, invalidInput()
	}
	oldDocument, err := parseIndex(current.Index)
	if err != nil {
		return pendingTransaction{}, false, err
	}
	oldEntries := map[string]indexEntry{}
	for _, entry := range oldDocument.Entries {
		oldEntries[entry.PageID] = entry
	}
	ids := make([]string, 0, len(bodies))
	for id := range bodies {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	metadata := Metadata{Format: 1, Revision: current.Meta.Revision, ChangeSequence: current.Meta.ChangeSequence, Pages: map[string]PageMeta{}}
	affected := map[string]AffectedPage{}
	changed := false
	tx := pendingTransaction{Format: 1, OperationID: request.OperationID, ExpectedRevision: request.ExpectedRevision, ResultRevision: current.Meta.Revision, Writes: []afterImage{}, Deletes: []string{}}
	for _, id := range ids {
		body := bodies[id]
		if !validPageID(id) || !utf8.ValidString(body) {
			return pendingTransaction{}, false, invalidInput()
		}
		if len(body) > topicLimit {
			return pendingTransaction{}, false, limitError("topic", len(body), topicLimit)
		}
		oldPage, existed := current.Pages[id]
		meta := current.Meta.Pages[id]
		oldEntry, newEntry := oldEntries[id], entries[id]
		substantive := !existed || oldPage.Body != body || oldEntry.Label != newEntry.Label || oldEntry.Summary != newEntry.Summary
		if !existed {
			meta.Created, meta.Updated, meta.Reviewed = now, now, nil
		} else if substantive {
			meta.Updated = now
		}
		operation, explicit := operations[id]
		reviewed := explicit && operation.Kind == "review"
		if reviewed {
			at := now
			meta.Reviewed = &at
		}
		meta.Hash = hashBytes([]byte(body))
		metadata.Pages[id] = meta
		if !existed || oldPage.Body != body {
			tx.Writes = append(tx.Writes, after(id+".md", []byte(body)))
		}
		if substantive || reviewed || explicit {
			kind := "put"
			if explicit {
				kind = operation.Kind
			}
			beforeHash := ""
			if existed {
				beforeHash = current.Meta.Pages[id].Hash
			}
			affected[id] = AffectedPage{PageID: id, Kind: kind, BeforeHash: beforeHash, AfterHash: meta.Hash}
		}
		if substantive || reviewed {
			changed = true
		}
	}
	for id, operation := range operations {
		if operation.Kind == "delete" {
			tx.Deletes = append(tx.Deletes, id+".md")
			affected[id] = AffectedPage{PageID: id, Kind: "delete", BeforeHash: current.Meta.Pages[id].Hash}
			changed = true
		}
	}
	sort.Strings(tx.Deletes)
	normalized := renderIndex(document, metadata.Pages)
	if len(normalized) > indexLimit {
		return pendingTransaction{}, false, limitError("index", len(normalized), indexLimit)
	}
	// Validate every active link against the complete after-state, including index
	// prose, rather than operation order or only newly submitted pages.
	references := []Reference{}
	sources := append([]string{"index"}, ids...)
	for _, id := range sources {
		body := normalized
		if id != "index" {
			body = bodies[id]
		}
		links, err := localLinks(body)
		if err != nil {
			return pendingTransaction{}, false, err
		}
		for _, reference := range links {
			destination, _, _ := strings.Cut(reference.Destination, "#")
			target := strings.TrimSuffix(destination, ".md")
			if _, exists := bodies[target]; target != "index" && !exists {
				reference.Source = id
				// An arbitrary fragment is not needed to locate a broken topic.
				// Keep diagnostics bounded and do not echo paths/body text in it.
				reference.Destination = destination
				references = append(references, reference)
			}
		}
	}
	if len(references) > 0 {
		sort.SliceStable(references, func(i, j int) bool {
			a, b := references[i], references[j]
			if a.Source != b.Source {
				return a.Source < b.Source
			}
			if a.Destination != b.Destination {
				return a.Destination < b.Destination
			}
			return a.Line < b.Line
		})
		more := len(references) > referenceLimit
		if more {
			references = references[:referenceLimit]
		}
		return pendingTransaction{}, false, &Error{Code: "invalid_input", References: references, MoreReferences: more}
	}
	metadata.IndexHash = hashBytes([]byte(normalized))
	if normalized != current.Index {
		changed = true
		tx.Writes = append(tx.Writes, after("index.md", []byte(normalized)))
	}
	if changed {
		metadata.Revision++
		metadata.ChangeSequence++
		data, err := json.Marshal(metadata)
		if err != nil {
			return pendingTransaction{}, false, err
		}
		tx.Writes = append(tx.Writes, after(".metadata.json", data))
		tx.ResultRevision = metadata.Revision
	}
	affectedIDs := make([]string, 0, len(affected))
	for id := range affected {
		affectedIDs = append(affectedIDs, id)
	}
	sort.Strings(affectedIDs)
	pages := make([]AffectedPage, 0, len(affectedIDs))
	for _, id := range affectedIDs {
		pages = append(pages, affected[id])
	}
	tx.Receipt = Receipt{OperationID: request.OperationID, ExpectedRevision: request.ExpectedRevision, ResultRevision: tx.ResultRevision, Changed: changed, Sequence: metadata.ChangeSequence, Actor: actor, At: now, Pages: pages, IndexBeforeHash: hashBytes([]byte(current.Index)), IndexAfterHash: metadata.IndexHash}
	return tx, changed, nil
}
