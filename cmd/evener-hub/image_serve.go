package hub

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"

	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/fspaths"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
)

// imageShaRegexp limits paths to lowercase hex sha256 (64 chars). The browser
// only ever sees shas the hub computed, so anything else is a bad request.
var imageShaRegexp = regexp.MustCompile(`^[0-9a-f]{64}$`)

// handleSessionImage streams the image whose sha256 matches the URL fragment
// for a given session. Used by the renderer when replaying a past USER_INPUT
// turn — the replay path strips inline image bytes and references each image
// by sha so live transcript payloads stay small. The bytes still live in the
// session transcript, so we re-scan to find them.
//
// This is loopback-only and a session-scoped read, so we don't index or
// cache. Each fetch re-scans; the browser caches by URL via standard
// HTTP caching headers.
func (s *WebServer) handleSessionImage(w http.ResponseWriter, r *http.Request, sessionID, sha string) {
	if !imageShaRegexp.MatchString(sha) {
		http.Error(w, "bad sha", http.StatusBadRequest)
		return
	}
	// A host-qualified route id names a session on another host: the bytes live
	// on that host's filesystem, so the request is proxied to the owning source
	// instead of resolved here. A bare or "local:" id resolves exactly as
	// before.
	if ref, ok := hostQualifiedImageRef(sessionID); ok {
		s.serveRemoteSessionImage(w, r, ref, appwire.SessionImageParams{SessionID: ref.ThreadID, SHA: sha})
		return
	}
	if s.cfg.Past == nil {
		http.NotFound(w, r)
		return
	}
	entry, ok := s.cfg.Past.Find(sessionID)
	if !ok {
		http.NotFound(w, r)
		return
	}
	transcriptPath := filepath.Join(entry.StateDir, "sessions", entry.Meta.ID+".transcript.jsonl")
	data, mediaType, ok, err := findImageInTranscript(transcriptPath, sha)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if !ok {
		http.NotFound(w, r)
		return
	}
	if mediaType == "" {
		mediaType = "application/octet-stream"
	}
	w.Header().Set("Content-Type", mediaType)
	// Sha-addressed content: safe to cache aggressively.
	w.Header().Set("Cache-Control", "public, max-age=86400, immutable")
	w.Header().Set("ETag", `"`+sha+`"`)
	w.Write(data) //nolint:errcheck
}

// findImageInTranscript scans the transcript for any USER_INPUT turn
// containing an image part with the given sha256. Returns the raw bytes and
// media type. Image bytes can be large; we only buffer the matching one.
func findImageInTranscript(path, wantSha string) ([]byte, string, bool, error) {
	return scanTranscriptForImage(path, wantSha, transcriptJSONLMaxLineBytes, 0)
}

// sessionImageRecordOverhead is the space one transcript record may spend on
// everything that is not its own image — turn text, sibling tool results, JSON
// scaffolding — before the bounded scan refuses to decode it. It is the slack
// between the transport-sized line cap and the 8 MiB image bound.
const sessionImageRecordOverhead = 4 * 1024 * 1024

// sessionImageBounds is the bound the evener/session/image method reads under:
// an image of at most outputImageMaxBytes inside a record long enough to hold
// its base64 form plus sessionImageRecordOverhead. The method is the one path
// by which a hub reads bytes out of another hub's filesystem, so the bound
// governs the read: an over-bound record is refused before DecodeEntry
// materializes its image bytes, and an over-bound image is refused even when
// the record decoded.
func sessionImageBounds() (maxRecordBytes int, maxImageBytes int64) {
	return base64.StdEncoding.EncodedLen(outputImageMaxBytes) + sessionImageRecordOverhead, outputImageMaxBytes
}

// scanTranscriptForImage is the shared body of the transcript image scan.
// maxRecordBytes caps one decoded record; maxImageBytes, when > 0, is the
// largest image the caller will serve, and it also turns an over-long record
// into a refusal (not-found) instead of a read error: a record that cannot
// hold an image within the bound cannot answer the request. With
// maxImageBytes == 0 the scan serves whatever the transcript holds and reports
// every read failure, which is the local route's contract.
func scanTranscriptForImage(path, wantSha string, maxRecordBytes int, maxImageBytes int64) ([]byte, string, bool, error) {
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, "", false, nil
		}
		return nil, "", false, fmt.Errorf("open transcript: %w", err)
	}
	defer f.Close() //nolint:errcheck // read-only file; close error is not actionable
	reader := bufio.NewReaderSize(f, 64*1024)
	headerRead := false
	var matchedData []byte
	var matchedMediaType string
	// Compare raw sums rather than re-encoding every candidate to hex: an image
	// that does not match must not allocate a 64-byte string per pass. A
	// malformed wantSha is simply one that cannot match.
	wantSum, haveWant := decodeHexSha256(wantSha)
	// matchImage records the first image whose bytes hash to wantSha, reporting
	// true when that image is over the caller's bound: the request cannot be
	// served under it, so the read is refused rather than answered with a
	// different image. One closure for both content kinds keeps the bound and
	// the comparison in one place.
	matchImage := func(data []byte, mediaType string) bool {
		if !haveWant || matchedData != nil || len(data) == 0 {
			return false
		}
		if sha256.Sum256(data) != wantSum {
			return false
		}
		if maxImageBytes > 0 && int64(len(data)) > maxImageBytes {
			return true
		}
		matchedData = data
		matchedMediaType = mediaType
		return false
	}
	for {
		line, complete, _, readErr := transcript.ReadLine(reader, maxRecordBytes)
		if readErr != nil {
			if maxImageBytes > 0 && errors.Is(readErr, transcript.ErrLineTooLong) {
				// An over-bound record cannot hold a servable image, and ReadLine
				// consumed it whole before reporting, so the scan resumes at the
				// next record instead of abandoning the images after it.
				continue
			}
			return nil, "", false, readErr
		}
		if !complete {
			break
		}
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		if !headerRead {
			if _, err := transcript.DecodeHeader(line); err != nil {
				return nil, "", false, fmt.Errorf("parse transcript header: %w", err)
			}
			headerRead = true
			continue
		}
		rec, err := transcript.DecodeEntry(line)
		if err != nil {
			return nil, "", false, fmt.Errorf("parse transcript entry: %w", err)
		}
		overBound := false
		for _, p := range rec.Turn.Message.Content {
			switch p.Kind {
			case "image":
				if p.Image != nil {
					overBound = matchImage(p.Image.Data, p.Image.MediaType)
				}
			case "tool_result":
				if p.ToolResult != nil {
					overBound = matchImage(p.ToolResult.ImageData, p.ToolResult.ImageMediaType)
				}
			}
			if overBound {
				return nil, "", false, nil
			}
		}
	}
	if !headerRead {
		return nil, "", false, fmt.Errorf("%w: missing transcript header", transcript.ErrUnsupportedFormat)
	}
	return matchedData, matchedMediaType, matchedData != nil, nil
}

// decodeHexSha256 parses a lowercase hex sha256 into the raw sum the scan
// compares, reporting false for a value that is not one. The canonical-spelling
// check keeps the comparison's meaning exactly: an upper-case or otherwise
// non-canonical spelling matched nothing before and matches nothing now.
func decodeHexSha256(raw string) ([sha256.Size]byte, bool) {
	decoded, err := hex.DecodeString(raw)
	if err != nil || len(decoded) != sha256.Size {
		return [sha256.Size]byte{}, false
	}
	sum := [sha256.Size]byte(decoded)
	if hex.EncodeToString(sum[:]) != raw {
		return [sha256.Size]byte{}, false
	}
	return sum, true
}

// imageSha returns the lowercase hex sha256 of raw image bytes.
func imageSha(data []byte) string {
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:])
}

// sessionImageFromHub serves the evener/session/image AppWire method: one image
// out of THIS hub's own local session state, for the controller-side proxy that
// serves a remote session's host-qualified image routes (multi-host component
// 05). It is the AppWire counterpart of handleSessionImage and handleDocImage
// and shares their resolution discipline: the sha branch re-scans the session
// transcript under the 8 MiB bound, and the file-backed branch resolves a
// session-relative path inside the session's own working directory with
// fspaths.ResolveInRoot. SessionImageParams carries no source selector, so the
// method can only ever resolve against the recipient's own state and can never
// read an arbitrary host file; anything it cannot resolve is refused typed
// rather than guessed.
func sessionImageFromHub(cfg hubcore.WebConfig, params appwire.SessionImageParams) (appwire.SessionImageResponse, error) {
	switch {
	case params.SessionID == "":
		return appwire.SessionImageResponse{}, appwire.InvalidParams("sessionId is required")
	case (params.SHA == "") == (params.Path == ""):
		return appwire.SessionImageResponse{}, appwire.InvalidParams("exactly one of sha or path is required")
	case params.SHA != "":
		if !imageShaRegexp.MatchString(params.SHA) {
			return appwire.SessionImageResponse{}, appwire.InvalidParams("sha must be 64 lowercase hex characters")
		}
		return sessionImageBySha(cfg, params.SessionID, params.SHA)
	default:
		return sessionImageByPath(cfg, params.SessionID, params.Path)
	}
}

// sessionImageBySha answers the replayed-input form: the bytes are re-scanned
// out of the session transcript, and their media type is re-derived from those
// bytes — the transcript's stored media type is never trusted.
func sessionImageBySha(cfg hubcore.WebConfig, sessionID, sha string) (appwire.SessionImageResponse, error) {
	if cfg.Past == nil {
		return appwire.SessionImageResponse{}, appwire.ResourceNotFound("session not found")
	}
	entry, ok := cfg.Past.Find(sessionID)
	if !ok {
		return appwire.SessionImageResponse{}, appwire.ResourceNotFound("session not found")
	}
	maxRecordBytes, maxImageBytes := sessionImageBounds()
	data, _, ok, err := scanTranscriptForImage(pastTranscriptPath(entry), sha, maxRecordBytes, maxImageBytes)
	if err != nil {
		return appwire.SessionImageResponse{}, appwire.ResourceNotFound("session image unavailable")
	}
	if !ok || len(data) == 0 {
		return appwire.SessionImageResponse{}, appwire.ResourceNotFound("image not found")
	}
	mediaType, ok := supportedOutputImageMedia(data, "")
	if !ok {
		return appwire.SessionImageResponse{}, appwire.ResourceNotFound("image media type is not supported")
	}
	// A match means these are the bytes of the requested canonical sha: echo it
	// rather than hashing the whole buffer a second time.
	return appwire.SessionImageResponse{
		MediaType: mediaType,
		Size:      int64(len(data)),
		SHA:       sha,
		Data:      data,
	}, nil
}

// sessionImageByPath answers the file-backed form: a session-relative path
// inside the session's own working directory, refused on any escape and bounded
// by outputImageMaxBytes at stat time (readOutputImageFile).
func sessionImageByPath(cfg hubcore.WebConfig, sessionID, rel string) (appwire.SessionImageResponse, error) {
	// Path is session-relative by contract: an absolute path is refused even
	// when it happens to resolve inside the session root.
	if filepath.IsAbs(rel) {
		return appwire.SessionImageResponse{}, appwire.InvalidParams("path must be session-relative")
	}
	cwd, ok := sessionCWD(cfg, sessionID)
	if !ok {
		return appwire.SessionImageResponse{}, appwire.ResourceNotFound("session not found")
	}
	abs, err := fspaths.ResolveInRoot(cwd, rel)
	if err != nil {
		if errors.Is(err, fspaths.ErrPathEscapesRoot) {
			return appwire.SessionImageResponse{}, appwire.InvalidParams("path must resolve inside the session root")
		}
		return appwire.SessionImageResponse{}, appwire.ResourceNotFound("image not found")
	}
	data, info, ok := readOutputImageFile(abs)
	if !ok {
		return appwire.SessionImageResponse{}, appwire.ResourceNotFound("image not found")
	}
	mediaType, ok := supportedOutputImageMedia(data, filepath.Base(abs))
	if !ok {
		return appwire.SessionImageResponse{}, appwire.ResourceNotFound("image media type is not supported")
	}
	return appwire.SessionImageResponse{
		MediaType: mediaType,
		Size:      info.Size(),
		SHA:       outputImageSHA(data),
		Data:      data,
	}, nil
}
