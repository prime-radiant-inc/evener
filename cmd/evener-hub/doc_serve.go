package hub

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"mime"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
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

// errSessionFileNotFound is resolveSessionFile's answer for a path that names
// no readable file in any root that may serve it.
var errSessionFileNotFound = errors.New("session file not found")

// resolveSessionFile resolves a local session id and a path to a served root and
// a contained absolute path. It is the one rule behind every doc and image
// route, local and remote; each caller maps the typed failure to its own
// status or wire error: fspaths.ErrPathEscapesRoot for an escape,
// errSessionFileNotFound for a miss, and otherwise the session's own error
// (a deletion refusal, an unresolvable session folder).
//
// A relative path resolves against the session's cwd only. An absolute path
// may also name a file inside the session's retained artifacts directory:
// agents file reports there and name them by file:// URL, and the transcript
// turns those into document-pane links, so the routes have to serve them.
// The artifacts root is one more boundary beside cwd, never a widening of it:
// both are resolved by fspaths.ResolveInRoot, and the root that accepted the
// path is returned so the read opens through that same root.
func resolveSessionFile(ctx context.Context, cfg hubcore.WebConfig, sources *appsource.Registry, session, rel string) (root, abs string, err error) {
	cwd, cwdErr := sessionCWD(ctx, cfg, sources, session)
	absolute := filepath.IsAbs(strings.TrimSpace(rel))
	var cwdResolveErr error
	if cwdErr == nil {
		resolved, resolveErr := fspaths.ResolveInRoot(cwd, rel)
		if resolveErr == nil {
			return cwd, resolved, nil
		}
		// A relative path can only mean the working directory, so a cwd
		// failure is the whole answer for it.
		if !absolute {
			return "", "", sessionFileMiss(resolveErr)
		}
		cwdResolveErr = resolveErr
	}
	// A retained artifact lives under the session's state directory, which
	// outlives the working directory: that is why it is tried for an absolute
	// path even when the cwd itself cannot be resolved at all (a removed
	// worktree, or a session whose live source is momentarily unavailable).
	// A deletion refusal is not one of those cases: it says this session's
	// files must not be served, so it stands before anything is tried.
	if absolute && !isTargetDeletedError(cwdErr) {
		if root, resolved, contained, ok := artifactsDocTarget(ctx, cfg, session, rel); ok {
			return root, resolved, nil
		} else if contained {
			// The path belongs to the artifacts root but names no readable
			// file there. That is a miss, not the cwd's escape refusal.
			return "", "", errSessionFileNotFound
		}
	}
	if cwdErr != nil {
		return "", "", cwdErr
	}
	// The cwd refused this absolute path as an escape and no served root
	// accepted it, so the refusal stands rather than softening to a miss.
	return "", "", sessionFileMiss(cwdResolveErr)
}

// sessionFileMiss classifies a failed ResolveInRoot as an escape or a miss.
func sessionFileMiss(resolveErr error) error {
	if errors.Is(resolveErr, fspaths.ErrPathEscapesRoot) {
		return fspaths.ErrPathEscapesRoot
	}
	return errSessionFileNotFound
}

// resolveLocalDocTarget is resolveSessionFile for the local doc routes. It
// answers the request itself when it cannot, so every local doc route refuses
// an unknown session, an escape attempt and a missing path identically: 503
// for a session whose folder cannot be resolved, 403 for an escape, 404
// otherwise.
func (s *WebServer) resolveLocalDocTarget(w http.ResponseWriter, r *http.Request, session, rel string) (string, string, bool) {
	root, abs, err := resolveSessionFile(r.Context(), s.cfg, s.sources, session, rel)
	switch {
	case err == nil:
		return root, abs, true
	case errors.Is(err, fspaths.ErrPathEscapesRoot):
		http.Error(w, "forbidden", http.StatusForbidden)
	case errors.Is(err, errSessionFileNotFound):
		http.NotFound(w, r)
	default:
		serveSessionRootError(w, r, err)
	}
	return "", "", false
}

// resolveArtifactsDocTarget resolves an absolute request path against the
// session's retained artifacts directory. A relative path never reaches a
// second root: only an absolute one can name a file outside the working
// directory, and letting a relative path fall through here would silently
// retarget it. An unknown session, a missing root and a path outside it all
// report not-served, so the caller answers them as any other miss.
// artifactsDocTarget is resolveArtifactsDocTarget plus whether the path was
// contained by the artifacts root but named no readable file there. Callers
// use that to answer a contained miss with not-found instead of turning it
// into the cwd's escape refusal, which would call a missing report forbidden.
func (s *WebServer) resolveArtifactsDocTarget(ctx context.Context, session, rel string) (string, string, bool) {
	root, abs, _, ok := artifactsDocTarget(ctx, s.cfg, session, rel)
	return root, abs, ok
}

// artifactsDocTarget answers the containment question resolveArtifactsDocTarget
// cannot: whether a refused path was inside the artifacts root.
func artifactsDocTarget(ctx context.Context, cfg hubcore.WebConfig, session, rel string) (root, abs string, contained, ok bool) {
	if !filepath.IsAbs(strings.TrimSpace(rel)) {
		return "", "", false, false
	}
	// A session fenced for deletion must not serve retained files through this
	// root either. The cwd path gets this from sessionCWD's deletion check;
	// artifacts resolve from the roster or the past index, which do not.
	//
	// A stable workspace ref can resolve to a different current session, so
	// the fence covers both identities: a deletion recorded against the session
	// the ref now names must not be bypassed by asking through the ref.
	_, resolvedSession, resolved := artifactsSessionScope(ctx, cfg, session)
	fenced := []string{canonicalRouteID(session)}
	if resolved && resolvedSession != "" && resolvedSession != fenced[0] {
		fenced = append(fenced, resolvedSession)
	}
	for _, identity := range fenced {
		if err := deletionFenceError(cfg, appRefFromRouteID(identity), identity, ""); err != nil {
			return "", "", false, false
		}
	}
	root = sessionArtifactsRoot(ctx, cfg, session)
	if root == "" {
		return "", "", false, false
	}
	// A root that does not exist contains nothing, so every path is outside
	// it. Without this, ResolveInRoot fails while resolving the root itself
	// and the failure reads as a contained miss. This only chooses the refusal;
	// the open enforces the boundary on its own.
	if _, statErr := os.Stat(root); statErr != nil {
		return "", "", false, false
	}
	// ResolveInRoot rejects an escaping path with ErrPathEscapesRoot and every
	// other failure (a missing file, an unreadable directory) with its own
	// error, which is exactly the containment/existence split callers need.
	abs, err := fspaths.ResolveInRoot(root, rel)
	if err != nil {
		return root, "", !errors.Is(err, fspaths.ErrPathEscapesRoot), false
	}
	return root, abs, true, true
}

// sessionArtifactsRoot is the local session's retained artifacts directory, or
// "" when this hub serves none for it. This is the one answer both the doc
// routes and the thread snapshot use, so a link the wire advertises is exactly
// a link the route serves.
//
// The argument may be a session id or the stable workspace ref a thread keeps
// across Clear: the roster keys live entries by their current session id, so a
// stable ref is first resolved to the session that owns it. A running
// delegate's artifacts sit under its owner's project state directory, so a
// child id resolves through the live entry that reports it. A remote session
// names no local path.
func sessionArtifactsRoot(ctx context.Context, cfg hubcore.WebConfig, ref string) string {
	stateDir, session, ok := artifactsSessionScope(ctx, cfg, ref)
	if !ok {
		return ""
	}
	root, err := schema.SessionArtifactsDir(stateDir, session)
	if err != nil {
		return ""
	}
	declareArtifactRoot(root)
	return root
}

// artifactsSessionScope resolves the state directory and the current session id
// an artifacts path belongs to. A stable workspace ref follows to the live
// session that owns it, because the roster keys entries by the current id. The
// document fence needs that resolved identity as well as the requested one.
func artifactsSessionScope(ctx context.Context, cfg hubcore.WebConfig, ref string) (stateDir, sessionID string, ok bool) {
	ref = canonicalRouteID(ref)
	if !isLocalRouteID(ref) {
		return "", "", false
	}
	session := ref
	if cfg.Roster != nil {
		if live, liveOK := liveDaemonForThread(cfg.Roster, ref); liveOK && !live.Crashed {
			if live.SessionID != "" {
				session = live.SessionID
			}
		}
	}
	stateDir = sessionStateDir(ctx, cfg, session)
	if stateDir == "" {
		return "", "", false
	}
	return stateDir, session, true
}

// sessionStateDir resolves the project state directory that holds a local
// session's own state: the live entry's own StateDir, else the live entry that
// runs it as a subagent, else the past index's trusted locator. It reads the
// roster's keyed maps rather than a cloned snapshot, and never a cached launch
// root.
func sessionStateDir(ctx context.Context, cfg hubcore.WebConfig, session string) string {
	if ctx.Err() != nil {
		return ""
	}
	if cfg.Roster != nil {
		if live, ok := cfg.Roster.Find(session); ok && !live.Crashed {
			if stateDir := strings.TrimSpace(live.StateDir); stateDir != "" {
				return stateDir
			}
		}
		if owner, ok := cfg.Roster.SubagentOwner(session); ok {
			return strings.TrimSpace(owner.StateDir)
		}
	}
	if cfg.Past == nil {
		return ""
	}
	entry, ok := cfg.Past.Find(session)
	if !ok {
		return ""
	}
	return strings.TrimSpace(entry.StateDir)
}

// The representations /doc/file serves: the capped head a pane renders, and
// the whole file a client saves or shares.
const (
	docFormatRaw      = "raw"
	docFormatDownload = "download"
)

// parseDocFormat answers the request itself when `format` names no
// representation, so every caller refuses the same way.
func parseDocFormat(w http.ResponseWriter, r *http.Request) (string, bool) {
	format := r.URL.Query().Get("format")
	if format != docFormatRaw && format != docFormatDownload {
		http.Error(w, "format=raw or format=download required", http.StatusBadRequest)
		return "", false
	}
	return format, true
}

// handleDocFile serves one session file's bytes. `format` names the
// representation, and a request that omits it or sends any other value is a
// client error (400 with a hint naming the parameter):
//
//   - raw: the head a doc pane renders, capped at docFileMaxBytes with the
//     truncation and revision headers writeDocFileRaw describes.
//   - download: the whole file, as an attachment, streamed through
//     http.ServeContent so Range and HEAD work and a large file resumes.
//
// Both modes share one guard chain: session/path presence, then cwd
// containment, so an unknown session or an escaping path is refused identically
// whatever the format. Containment runs before the format gate for exactly that
// reason; the gate then runs before the file is opened or hashed.
//
// A host-qualified session id names a session on another host, so its file is
// read by the owning host (serveRemoteSessionDocument); a local id reads this
// hub's own filesystem. The host-to-host read carries only the raw head, so a
// download of a remote session is refused rather than served short.
//
// Security: the only file paths we serve are ones that resolve to a location
// inside the session's cwd. We clean the request path, reject any residual
// traversal, and confirm the symlink-resolved absolute path is contained by
// the symlink-resolved cwd. Anything that escapes the cwd is refused. The
// read itself opens through an os.Root at the cwd (openDocInRoot), so a
// symlink swapped in after the check cannot lead it out either.
func (s *WebServer) handleDocFile(w http.ResponseWriter, r *http.Request) {
	// HEAD is the download representation answering its own metadata; raw keeps
	// GET, checked once the representation is known.
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
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
		format, ok := parseDocFormat(w, r)
		if !ok {
			return
		}
		// The host-to-host read answers only the raw head, so a whole file is
		// refused here instead of being served short.
		if format != docFormatRaw {
			http.Error(w, "remote session not supported", http.StatusNotImplemented)
			return
		}
		// A remote read costs a round trip, so the raw representation's own
		// method rule applies before the host is asked.
		if r.Method != http.MethodGet {
			http.Error(w, "GET required", http.StatusMethodNotAllowed)
			return
		}
		s.serveRemoteSessionDocument(w, r, ref, rel)
		return
	}

	cwd, abs, ok := s.resolveLocalDocTarget(w, r, session, rel)
	if !ok {
		return
	}

	format, ok := parseDocFormat(w, r)
	if !ok {
		return
	}
	if format == docFormatDownload {
		s.writeDocDownload(w, r, cwd, abs)
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "GET required", http.StatusMethodNotAllowed)
		return
	}

	doc, err := readDocFile(cwd, abs)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	writeDocFileRaw(w, r, doc)
}

// downloadTypes names the media types a download serves, by extension. It is
// the whole table, not a supplement to Go's mime package: that package consults
// the host's own tables, so its answer differs between macOS and Linux, and it
// names text/html for .htm. A download is an attachment the receiving app
// decides what to do with, so an unlisted extension gets
// application/octet-stream rather than a type guessed from the host.
var downloadTypes = map[string]string{
	".mp4":  "video/mp4",
	".m4v":  "video/x-m4v",
	".mov":  "video/quicktime",
	".webm": "video/webm",
	".mp3":  "audio/mpeg",
	".m4a":  "audio/mp4",
	".wav":  "audio/wav",
	".pdf":  "application/pdf",
	".zip":  "application/zip",
	".gz":   "application/gzip",
	".tar":  "application/x-tar",
	".csv":  "text/csv",
	".txt":  "text/plain; charset=utf-8",
	".md":   "text/markdown; charset=utf-8",
	".json": "application/json",
	".png":  "image/png",
	".jpg":  "image/jpeg",
	".jpeg": "image/jpeg",
	".gif":  "image/gif",
	".webp": "image/webp",
}

// downloadMediaType names what a download's bytes are, without trusting them.
// The extension is the only input: sniffing a video's header is unreliable, and
// anything unlisted is served as opaque bytes rather than a page.
func downloadMediaType(name string) string {
	ext := strings.ToLower(filepath.Ext(name))
	if mediaType, ok := downloadTypes[ext]; ok {
		return mediaType
	}
	return "application/octet-stream"
}

// writeDocDownload streams abs, an already-contained session file, as an
// attachment. A pane shows a head; a file handed to another app must be the
// whole thing, so this reads with no cap of its own. The open goes through the
// session's cwd root (docOpen), and a non-regular file is refused: a FIFO would
// make the copy wait forever.
func (s *WebServer) writeDocDownload(w http.ResponseWriter, r *http.Request, cwd, abs string) {
	f, err := docOpen(cwd, abs)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close() //nolint:errcheck // read-only file; close error is not actionable
	info, err := docStat(f)
	if err != nil || !info.Mode().IsRegular() {
		http.NotFound(w, r)
		return
	}
	name := filepath.Base(abs)
	w.Header().Set("Content-Type", downloadMediaType(name))
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": name}))
	// A download is a snapshot of the file now, not a cacheable resource.
	w.Header().Set("Cache-Control", "private, no-store")
	// ServeContent streams the open file: it answers HEAD and Range, and a
	// read error aborts the transfer instead of sending a short body under a
	// full Content-Length. A type is already set, so it does not sniff.
	http.ServeContent(w, r, name, info.ModTime(), f)
}

// sessionDocumentFromHub serves the evener/session/document AppWire method: one
// document out of THIS hub's own local session state, for the controller's
// /doc/file proxy (S7). It is the AppWire counterpart of handleDocFile and
// resolves the path by the same rule, so a remote read is confined to the same
// served roots a local one is: the session's folder, and an absolute path in
// its retained artifacts directory.
func sessionDocumentFromHub(ctx context.Context, cfg hubcore.WebConfig, sources *appsource.Registry, params appwire.SessionDocumentParams) (appwire.SessionDocumentResponse, error) {
	if params.SessionID == "" || params.Path == "" {
		return appwire.SessionDocumentResponse{}, appwire.InvalidParams("sessionId and path are required")
	}
	session := canonicalRouteID(params.SessionID)
	root, abs, err := resolveSessionFile(ctx, cfg, sources, session, params.Path)
	if err != nil {
		return appwire.SessionDocumentResponse{}, sessionDocumentWireError(err)
	}
	doc, err := readDocFile(root, abs)
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

// sessionDocumentWireError maps a resolveSessionFile failure to the wire error
// evener/session/document answers with; a session error passes through.
func sessionDocumentWireError(err error) error {
	switch {
	case errors.Is(err, fspaths.ErrPathEscapesRoot):
		return appwire.PathOutsideSession("path must resolve inside the session's working directory")
	case errors.Is(err, errSessionFileNotFound):
		return appwire.ResourceNotFound("document not found")
	}
	return err
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

	cwd, abs, ok := s.resolveLocalDocTarget(w, r, session, rel)
	if !ok {
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
	// An artifacts root is opened through an anchored traversal of the trusted
	// state directory, so a link planted in place of any component of the
	// session layout is refused rather than followed. The kind comes from the
	// resolver that chose this root, not from the pathname: an ordinary working
	// directory can be shaped like a session layout, and guessing from shape
	// would apply artifact traversal to a folder that has nothing to do with
	// one.
	if stateDir, layout, ok := declaredArtifactsLayout(root); ok {
		return openArtifactsFile(stateDir, layout, abs, root)
	}
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

// declaredArtifactRoots records the artifacts roots this process resolved from
// a session, so the open applies artifact traversal only to one that really is
// an artifacts root. An ordinary working directory can be shaped exactly like
// <stateDir>/sessions/<id>/artifacts, and deciding by shape would treat it as
// one.
//
// A root is remembered only after the session resolution that produced it
// succeeded, which no user-supplied working directory can reach.
var declaredArtifactRoots sync.Map // cleaned root -> struct{}

// declareArtifactRoot marks root as an artifacts root for a later open.
func declareArtifactRoot(root string) {
	if cleaned := filepath.Clean(root); cleaned != "" {
		declaredArtifactRoots.Store(cleaned, struct{}{})
	}
}

// declaredArtifactsLayout answers the artifacts layout for a root the resolver
// declared, and false for every other directory.
func declaredArtifactsLayout(root string) (stateDir, layout string, ok bool) {
	if _, declared := declaredArtifactRoots.Load(filepath.Clean(root)); !declared {
		return "", "", false
	}
	return artifactsLayout(root)
}

// artifactsLayout expresses artifactsDir as the trusted state directory it
// hangs off and the layout path beneath it: <stateDir> and
// sessions/<id>/artifacts. It answers false for any directory that is not that
// shape.
func artifactsLayout(artifactsDir string) (stateDir, layout string, ok bool) {
	sessionDir := filepath.Dir(artifactsDir)
	sessionsDir := filepath.Dir(sessionDir)
	stateDir = filepath.Dir(sessionsDir)
	if filepath.Base(sessionsDir) != "sessions" || filepath.Base(artifactsDir) != "artifacts" {
		return "", "", false
	}
	if stateDir == sessionsDir || stateDir == "." || stateDir == string(filepath.Separator) {
		return "", "", false
	}
	layout = path.Join("sessions", filepath.Base(sessionDir), "artifacts")
	return stateDir, layout, true
}

// openArtifactsFile opens abs through one handle anchored at stateDir. Each
// component of layout must be a real directory on that handle, so a symlink
// where `sessions`, the session directory, or `artifacts` belongs is refused
// instead of redirecting this session's read into another session's files.
// Traversal and the open share the handle, so replacing the path between them
// cannot redirect the read.
func openArtifactsFile(stateDir, layout, abs, artifactsDir string) (*os.File, error) {
	realAbs, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return nil, err
	}
	// The state directory may itself sit under a symlinked ancestor (macOS's
	// /var, for one), while abs was resolved. Both sides are resolved so the
	// relative path describes one spelling of the same tree.
	realStateDir, err := filepath.EvalSymlinks(stateDir)
	if err != nil {
		return nil, err
	}
	realArtifactsDir := filepath.Join(realStateDir, filepath.FromSlash(layout))
	rel, err := filepath.Rel(realArtifactsDir, realAbs)
	if err != nil || rel == "." || isParentPath(rel) {
		return nil, fspaths.ErrPathEscapesRoot
	}
	current, err := os.OpenRoot(realStateDir)
	if err != nil {
		return nil, err
	}
	// The handle is closed once the walk is done: the opened file outlives it.
	for component := range strings.SplitSeq(filepath.ToSlash(layout), "/") {
		info, statErr := current.Lstat(component)
		if statErr != nil {
			current.Close() //nolint:errcheck // read-only traversal
			return nil, statErr
		}
		if !info.IsDir() {
			current.Close() //nolint:errcheck // read-only traversal
			return nil, fspaths.ErrPathEscapesRoot
		}
		next, openErr := current.OpenRoot(component)
		current.Close() //nolint:errcheck // read-only traversal
		if openErr != nil {
			return nil, openErr
		}
		// The open follows a link inside the enclosing root, so the directory
		// it reached must be the one just inspected. A component swapped for a
		// link between the two calls fails here instead of being followed into
		// a sibling session.
		opened, openedErr := next.Lstat(".")
		if openedErr != nil || !os.SameFile(info, opened) {
			next.Close() //nolint:errcheck // read-only traversal
			return nil, fspaths.ErrPathEscapesRoot
		}
		current = next
	}
	defer current.Close() //nolint:errcheck // the opened file outlives this handle
	return current.OpenFile(rel, os.O_RDONLY|docOpenNonblock, 0)
}

// isParentPath reports whether rel climbs out of its base directory. It tests
// for a real parent segment, so a contained name that merely begins with two
// dots, such as "..report.md" or ".../plan.md", is still served.
func isParentPath(rel string) bool {
	return rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator))
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
