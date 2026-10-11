package hub

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/appsource"
	"primeradiant.com/evener/cmd/evener-hub/internal/fspaths"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
)

// docOpen opens a document for reading, confined to its session folder, and
// docStat stats the open file. Both are variables so coverage tests can fail
// them.
var docOpen = openDocInRoot
var docStat = (*os.File).Stat

// docFileMaxBytes caps how much of a file we read into a document pane. A pane
// is a quick read-only reference, not a pager; large files are truncated with
// a notice rather than streamed in full.
const docFileMaxBytes = 512 * 1024

// docRevisionMaxBytes bounds how much of a file a read hashes for its revision.
// Hashing costs a full read of the file (about 7 ms for 16 MiB, measured with
// sha256 on an M4 Max); the largest markdown file measured across 66,387 in
// ~/git was 3.2 MB. A larger file is served without a revision.
const docRevisionMaxBytes = 16 * 1024 * 1024

// handleDocFile serves a session file's literal bytes for the React
// doc-viewer pane, which renders the content itself. The route has a single
// mode, ?format=raw; a request that omits format or sends any other value is a
// client error (400 with a hint naming the parameter). A host-qualified session
// id names a session on another host, so its file is read by the owning host
// (serveRemoteSessionDocument); a local id reads this hub's own filesystem.
//
// For a local session the guard chain below (session/path presence, cwd
// containment) runs before the format check, so a raw and a non-raw request
// reject the same out-of-cwd or unknown-session input identically — only a
// contained path reaches the format gate, where a raw request is served
// (writeDocFileRaw) and anything else is refused. The format gate runs before
// the file is read and hashed, so a request the route would refuse never pays
// for the read. A host-qualified session checks the format first, so the host
// is never asked for a request this route would refuse.
//
// Security: the only file paths we serve are ones that resolve to a location
// inside the session's cwd. We clean the request path, reject any residual
// traversal, and confirm the symlink-resolved absolute path is contained by
// the symlink-resolved cwd. Anything that escapes the cwd is refused. The
// read itself opens through an os.Root at the cwd (openDocInRoot), so a
// symlink swapped in after the check cannot lead it out either.
func (s *WebServer) handleDocFile(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET required", http.StatusMethodNotAllowed)
		return
	}
	session := canonicalRouteID(r.URL.Query().Get("session"))
	rel := r.URL.Query().Get("path")
	if session == "" || rel == "" {
		http.NotFound(w, r)
		return
	}
	if ref, ok := hostQualifiedRouteRef(session); ok {
		// Checked before the host is asked: a request this route would refuse
		// is never forwarded.
		if r.URL.Query().Get("format") != "raw" {
			http.Error(w, "format=raw required", http.StatusBadRequest)
			return
		}
		s.serveRemoteSessionDocument(w, r, ref, rel)
		return
	}

	cwd, err := s.localSessionCWD(r.Context(), session)
	if err != nil {
		serveSessionRootError(w, r, err)
		return
	}

	// Resolve before the format gate so raw and non-raw reject the same
	// out-of-cwd or missing path identically; the gate precedes the read.
	abs, err := fspaths.ResolveInRoot(cwd, rel)
	if err != nil {
		// A path that escapes the cwd, or that doesn't resolve, is refused.
		// 403 for an escape attempt; 404 for a missing file.
		if errors.Is(err, fspaths.ErrPathEscapesRoot) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		http.NotFound(w, r)
		return
	}

	if r.URL.Query().Get("format") != "raw" {
		http.Error(w, "format=raw required", http.StatusBadRequest)
		return
	}

	doc, err := readDocFile(cwd, abs)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	writeDocFileRaw(w, r, doc)
}

// sessionDocumentFromHub serves the evener/session/document AppWire method: one
// document out of THIS hub's own local session state, for the controller's
// /doc/file proxy (S7). It is the AppWire counterpart of handleDocFile and
// resolves the path by the same rule (sessionCWD, which refuses a session id
// naming another source, then fspaths.ResolveInRoot), so a remote read is
// confined to the session's folder exactly as a local one is.
func sessionDocumentFromHub(ctx context.Context, cfg hubcore.WebConfig, sources *appsource.Registry, params appwire.SessionDocumentParams) (appwire.SessionDocumentResponse, error) {
	if params.SessionID == "" || params.Path == "" {
		return appwire.SessionDocumentResponse{}, appwire.InvalidParams("sessionId and path are required")
	}
	cwd, err := sessionCWD(ctx, cfg, sources, canonicalRouteID(params.SessionID))
	if err != nil {
		return appwire.SessionDocumentResponse{}, err
	}
	doc, err := readSessionDocument(cwd, params.Path)
	if errors.Is(err, fspaths.ErrPathEscapesRoot) {
		return appwire.SessionDocumentResponse{}, appwire.PathOutsideSession("path must resolve inside the session's working directory")
	}
	if err != nil {
		return appwire.SessionDocumentResponse{}, appwire.ResourceNotFound("document not found")
	}
	return appwire.SessionDocumentResponse{
		Data:       doc.Data,
		TotalSize:  doc.TotalSize,
		Revision:   doc.Revision,
		ModifiedAt: docModifiedMillis(doc.ModifiedAt),
	}, nil
}

// readSessionDocument reads the document rel names inside a session's working
// directory cwd, for evener/session/document. It fails with
// fspaths.ErrPathEscapesRoot when rel, or a symlink along it, leads outside cwd;
// any other failure means the document cannot be read.
func readSessionDocument(cwd, rel string) (docFileRead, error) {
	abs, err := fspaths.ResolveInRoot(cwd, rel)
	if err != nil {
		return docFileRead{}, err
	}
	return readDocFile(cwd, abs)
}

// handleDocImage serves a validated image file inside a session's working
// directory. It mirrors /doc/file's containment boundary, but only streams v1
// supported image media types for inline output-image previews. A
// host-qualified session id names a session on another host, so its file is
// fetched from the owning host instead; a local id reads this hub's own
// filesystem exactly as before.
func (s *WebServer) handleDocImage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET required", http.StatusMethodNotAllowed)
		return
	}
	session := canonicalRouteID(r.URL.Query().Get("session"))
	rel := r.URL.Query().Get("path")
	if session == "" || rel == "" {
		http.NotFound(w, r)
		return
	}
	if ref, ok := hostQualifiedRouteRef(session); ok {
		s.serveRemoteSessionImage(w, r, ref, appwire.SessionImageParams{SessionID: ref.ThreadID, Path: rel})
		return
	}

	cwd, err := s.localSessionCWD(r.Context(), session)
	if err != nil {
		serveSessionRootError(w, r, err)
		return
	}

	abs, err := fspaths.ResolveInRoot(cwd, rel)
	if err != nil {
		if errors.Is(err, fspaths.ErrPathEscapesRoot) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		http.NotFound(w, r)
		return
	}

	data, _, ok := readOutputImageInRoot(cwd, abs)
	if !ok {
		http.NotFound(w, r)
		return
	}
	mediaType, ok := supportedOutputImageMedia(data, filepath.Base(abs))
	if !ok {
		http.NotFound(w, r)
		return
	}

	w.Header().Set("Content-Type", mediaType)
	w.Header().Set("Cache-Control", "private, max-age=60")
	w.Header().Set("ETag", `"`+imageSha(data)+`"`)
	_, _ = w.Write(data)
}

// localSessionCWD resolves a local session's current working directory from its
// owning live source or freshly loaded archived metadata. /doc/image is
// local-session-only; non-local refs are not found.
func (s *WebServer) localSessionCWD(ctx context.Context, session string) (string, error) {
	return sessionCWD(ctx, s.cfg, s.sources, session)
}

// sessionCWD resolves a session's current working directory. A roster-owned
// session is read through its owning Source; failure is transient and never
// authorizes a cached launch root. An archived session uses the PastIndex only
// for trusted ID and StateDir locators, then reloads its metadata from disk.
// A non-local route id is not found, so a caller can never aim this local read
// at another host's session.
func sessionCWD(ctx context.Context, cfg hubcore.WebConfig, sources *appsource.Registry, session string) (string, error) {
	session = canonicalRouteID(session)
	if !isLocalRouteID(session) {
		return "", appwire.ResourceNotFound("session not found")
	}
	ref := appRefFromRouteID(session)
	if rosterOwnsLiveSession(cfg.Roster, session) {
		source, err := sourceForThreadWithDeletionFence(ctx, cfg, sources, ref, session)
		if err != nil {
			return "", liveSessionRootError(ctx, err)
		}
		response, err := source.ReadThread(ctx, appwire.ThreadReadParams{Ref: ref, IncludeTurns: false})
		if err != nil {
			return "", liveSessionRootError(ctx, err)
		}
		cwd := strings.TrimSpace(response.Thread.CWD)
		if cwd == "" {
			return "", appwire.SessionUnavailable("live session working directory unavailable")
		}
		return cwd, nil
	}
	if cfg.Past == nil {
		return "", appwire.ResourceNotFound("session not found")
	}
	entry, ok := cfg.Past.Find(session)
	if !ok {
		return "", appwire.ResourceNotFound("session not found")
	}
	return withDeletionTargetOwnership(ctx, cfg, ref, session, "", func() (string, error) {
		meta, err := schema.LoadSessionMeta(entry.StateDir, entry.ID)
		if errors.Is(err, os.ErrNotExist) {
			return "", appwire.ResourceNotFound("session not found")
		}
		if err != nil {
			return "", appwire.SessionUnavailable("archived session metadata unavailable")
		}
		cwd := strings.TrimSpace(meta.EnvInfo.WorkingDir)
		if cwd == "" {
			return "", appwire.ResourceNotFound("session working directory unavailable")
		}
		return cwd, nil
	})
}

func rosterOwnsLiveSession(roster *hubcore.Roster, session string) bool {
	if roster == nil {
		return false
	}
	if live, ok := roster.Find(session); ok && !live.Crashed {
		return true
	}
	_, ok := roster.SubagentState(session)
	return ok
}

func liveSessionRootError(ctx context.Context, err error) error {
	if ctxErr := ctx.Err(); ctxErr != nil {
		return ctxErr
	}
	if isTargetDeletedError(err) || isSessionUnavailableError(err) {
		return err
	}
	return appwire.SessionUnavailable("live session unavailable: " + err.Error())
}

func serveSessionRootError(w http.ResponseWriter, r *http.Request, err error) {
	if sessionDocumentProxyStatus(err) == http.StatusNotFound {
		http.NotFound(w, r)
		return
	}
	http.Error(w, "session unavailable", http.StatusServiceUnavailable)
}

// docFileRead is one read of a document: the head a pane shows and what the
// whole file is. Revision is the lowercase hex sha256 of the whole file, empty
// when the file is larger than docRevisionMaxBytes. TotalSize and Revision come
// from the same pass over the file, so they describe one version even while the
// file is being written.
type docFileRead struct {
	Data       []byte
	TotalSize  int64
	Revision   string
	ModifiedAt time.Time
}

// readDocFile reads the first docFileMaxBytes of abs, a path
// fspaths.ResolveInRoot accepted for root, with the file's size and revision.
// The open goes through root again (openDocInRoot), so a symlink swapped in
// after the check cannot lead it out, and it does not wait on a FIFO. The
// stat is of the open file, so directories and other non-regular files are
// refused whatever the path names by then.
func readDocFile(root, abs string) (docFileRead, error) {
	f, err := docOpen(root, abs)
	if err != nil {
		return docFileRead{}, err
	}
	defer f.Close() //nolint:errcheck // read-only file; close error is not actionable
	info, err := docStat(f)
	if err != nil {
		return docFileRead{}, err
	}
	if !info.Mode().IsRegular() {
		return docFileRead{}, os.ErrInvalid
	}
	head := make([]byte, docFileMaxBytes)
	n, err := io.ReadFull(f, head)
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
		return docFileRead{}, err
	}
	read := docFileRead{Data: head[:n], TotalSize: info.Size(), ModifiedAt: info.ModTime()}
	// The bytes read, never the stat's size, decide the revision: the file
	// may have grown or shrunk since the stat. Reading stops one byte past the
	// limit, so a huge file costs at most docRevisionMaxBytes of hashing.
	hash := sha256.New()
	hash.Write(read.Data)
	rest, err := io.Copy(hash, io.LimitReader(f, docRevisionMaxBytes-int64(n)+1))
	if err != nil {
		return docFileRead{}, err
	}
	total := int64(n) + rest
	if total > docRevisionMaxBytes {
		// Too large to hash: no revision, and a size of at least what the
		// read found.
		read.TotalSize = max(read.TotalSize, total)
		return read, nil
	}
	read.TotalSize, read.Revision = total, hex.EncodeToString(hash.Sum(nil))
	return read, nil
}

// openDocInRoot opens abs for reading through an os.Root at root, which
// refuses any path, symlinks included, that resolves outside root at the
// moment of the open. Both root and abs are symlink-resolved before the
// relative path is taken, so an abs expressed in terms of an unresolved root
// (on macOS t.TempDir sits under /var, a symlink to /private/var) is still
// expressed relative to the resolved root. docOpenNonblock keeps the open from
// waiting on a FIFO; a regular file reads the same.
func openDocInRoot(root, abs string) (*os.File, error) {
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return nil, err
	}
	realAbs, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return nil, err
	}
	rel, err := filepath.Rel(realRoot, realAbs)
	if err != nil {
		return nil, err
	}
	dir, err := os.OpenRoot(realRoot)
	if err != nil {
		return nil, err
	}
	defer dir.Close() //nolint:errcheck // a file opened through it stays open
	return dir.OpenFile(rel, os.O_RDONLY|docOpenNonblock, 0)
}

// looksBinaryBytes reports whether a byte slice looks like binary content. A
// NUL byte in the head is the standard heuristic; we only sniff the first few
// KiB.
func looksBinaryBytes(data []byte) bool {
	head := data
	if len(head) > 8192 {
		head = head[:8192]
	}
	return bytes.IndexByte(head, 0) >= 0
}

// writeDocFileRaw serves a document pane's literal file bytes for the native
// React doc-viewer pane (?format=raw), which renders the content itself
// instead of consuming the server-rendered HTML page. Content-Type reflects
// the same binary/text classification the HTML variant already computes via
// looksBinaryBytes, rather than sniffing the bytes: sniffing (e.g.
// http.DetectContentType) would classify an HTML-like text file as
// text/html, and a browser that ever loads this URL directly (not just via
// fetch) would then execute it same-origin. text/plain and
// application/octet-stream are both honest about the content and never
// browser-executable.
//
// doc.Data is capped at docFileMaxBytes; doc.TotalSize is the file's true byte
// size. When the file is larger than the cap the body is only its head, so an
// explicit X-Doc-Truncated / X-Doc-Total-Size pair lets the pane render an
// exact notice instead of inferring truncation from the body length (which is
// ambiguous at exactly the cap). A file of exactly the cap size is complete,
// hence not truncated.
//
// The revision rides as a strong ETag and the modification time as
// X-Doc-Modified-At (Unix milliseconds). "no-cache" makes every cache
// revalidate before reuse, and a request whose If-None-Match names the
// revision is answered 304 without the body.
func writeDocFileRaw(w http.ResponseWriter, r *http.Request, doc docFileRead) {
	w.Header().Set("Cache-Control", "private, no-cache")
	if ms := docModifiedMillis(doc.ModifiedAt); ms != 0 {
		w.Header().Set("X-Doc-Modified-At", strconv.FormatInt(ms, 10))
	}
	etag := ""
	if doc.Revision != "" {
		etag = `"` + doc.Revision + `"`
		w.Header().Set("ETag", etag)
	}
	if ifNoneMatchNames(r.Header.Get("If-None-Match"), etag) {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	if looksBinaryBytes(doc.Data) {
		w.Header().Set("Content-Type", "application/octet-stream")
	} else {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	}
	if doc.TotalSize > docFileMaxBytes {
		w.Header().Set("X-Doc-Truncated", "true")
		w.Header().Set("X-Doc-Total-Size", strconv.FormatInt(doc.TotalSize, 10))
	}
	_, _ = w.Write(doc.Data)
}

// docModifiedMillis is a modification time in Unix milliseconds, or 0 for a
// time at or before the epoch, which is sent as no time at all.
func docModifiedMillis(modified time.Time) int64 {
	return max(modified.UnixMilli(), 0)
}

// ifNoneMatchNames reports whether an If-None-Match header lists etag, or is
// "*", which matches any current version, even one with no revision (an empty
// etag). The comparison is weak, as RFC 9110 section 13.1.2 has it: a W/
// prefix on a listed tag is ignored.
func ifNoneMatchNames(header, etag string) bool {
	for listed := range strings.SplitSeq(header, ",") {
		listed = strings.TrimSpace(listed)
		if listed == "*" || (etag != "" && strings.TrimPrefix(listed, "W/") == etag) {
			return true
		}
	}
	return false
}
