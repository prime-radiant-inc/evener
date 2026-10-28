package agent

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/internal/artifactstore"
	"primeradiant.com/evener/agent/internal/tool"
	"primeradiant.com/evener/llm"
)

type artifactStore interface {
	Put([]byte) (string, error)
	Open(string) (*os.File, error)
	Close() error
}

var _ artifactStore = (*artifactstore.Store)(nil)

// sessionArtifactStoreCacheDir is the second base the store falls back to, the
// same way session scratch does (sandbox.sessionScratchBase). It is a seam only
// so a test can name it; production reads the user cache dir.
var sessionArtifactStoreCacheDir = os.UserCacheDir

// newSessionArtifactStore creates the store that retains full tool output. The
// process temp dir is tried first and the user cache dir only after it fails,
// so a working temp dir costs one call and no cache-directory filesystem probe
// (which session scratch likewise refuses to make on the fast path). The live
// reason the temp dir fails is an inherited TMPDIR pointing at a session
// container that has been deleted.
func newSessionArtifactStore() (artifactStore, error) {
	return sessionArtifactStoreFactory()
}

// sessionArtifactStoreFactory is the seam a test replaces to make store
// creation fail. Production resolves the bases itself.
var sessionArtifactStoreFactory = func() (artifactStore, error) {
	store, tempErr := artifactstore.New("")
	if tempErr == nil {
		return store, nil
	}
	cache, cacheErr := sessionArtifactStoreCacheDir()
	if cacheErr != nil {
		return nil, errors.Join(tempErr, cacheErr)
	}
	store, cacheErr = artifactstore.New(cache)
	if cacheErr != nil {
		return nil, errors.Join(tempErr, cacheErr)
	}
	return store, nil
}

// resolveArtifactStore resolves the store a session retains full tool output
// in, reporting whether the session owns what it returns. A non-nil supplied
// store is adopted as-is; an inherited nil means the tree's retention is
// degraded, so no new store is created. Full-output retention is a
// convenience — retainToolArtifact already degrades to retention_failed when a
// Put fails — so a store that cannot be created leaves the session running
// without one, with the cause returned for its session-start warning. The live
// cause is a TMPDIR that outlived the directory it named.
func resolveArtifactStore(supplied artifactStore, inherited bool) (store artifactStore, owns bool, err error) {
	if supplied != nil {
		// A supplied store always wins: the session adopts it without owning it,
		// whether a caller configured it or a parent runtime handed it down.
		return supplied, false, nil
	}
	if inherited {
		// The parent handed this session its store and has none: the tree's
		// retention is degraded, and the child must not mint its own.
		return nil, false, nil
	}
	created, err := newSessionArtifactStore()
	if err != nil {
		return nil, false, err
	}
	return created, true, nil
}

// bufferArtifactStoreWarning queues the session-start notice for a store that
// could not be created. Like every other construction-time diagnostic it waits
// for SESSION_START, the stream's promised first event.
func (s *Session) bufferArtifactStoreWarning(err error) {
	if err == nil {
		return
	}
	s.pendingTranscriptWarnings = append(s.pendingTranscriptWarnings, events.WarningData{
		Message: fmt.Sprintf("full tool output will not be retained: %v", err),
		Code:    "artifact_store_unavailable",
	})
}

func (s *Session) retainToolArtifact(res *tool.ExecResult) string {
	if !res.Truncated {
		return ""
	}
	// A nil store is the degraded session: its retention is unavailable for the
	// same reason a failed Put is, so both take the same tail.
	ref, err := s.putArtifact(res.RecoverableOutput)
	if err != nil {
		res.Output += "\n[retention_failed: full output could not be retained]"
		return ""
	}
	res.Output += artifactRecoveryFooter(ref)
	return ref
}

func artifactRecoveryFooter(ref string) string {
	return "\nFull output: " + ref +
		"\nRead with: read_transcript(transcript_ref=\"" + ref + "\")"
}

// retainedDelegateInlineBodyPresented verifies the existing registry's exact
// 50,000-rune TruncTail presentation, not arbitrary shaping or a ref alone.
// The capability must still resolve to the complete aggregate in this tree.
func (s *Session) retainedDelegateInlineBodyPresented(result *llm.ToolResultData, complete string) bool {
	if s.artifactStore == nil || result.Name != "delegate_send" || result.ToolCallID == "" || result.IsError {
		return false
	}
	body, ok := result.Content.(string)
	if !ok {
		return false
	}
	runes := []rune(complete)
	if len(runes) <= delegateReportWholeMaxChars {
		return false
	}
	const recoveryPrefix = "\nFull output: "
	footerAt := strings.LastIndex(body, recoveryPrefix)
	if footerAt < 0 {
		return false
	}
	footer := body[footerAt+len(recoveryPrefix):]
	ref, _, _ := strings.Cut(footer, "\n")
	if !validArtifactTranscriptRef(ref) {
		return false
	}
	// Keep this equality aligned with internal/tool's TruncTail framing. The
	// entire body, including both recovery lines, must survive unchanged.
	want := fmt.Sprintf("[Output truncated: %d characters removed from the beginning.]\n\n%s\n[Tool output was truncated.]", len(runes)-delegateReportWholeMaxChars, string(runes[len(runes)-delegateReportWholeMaxChars:])) + artifactRecoveryFooter(ref)
	if body != want {
		return false
	}
	file, err := s.artifactStore.Open(ref)
	if err != nil {
		return false
	}
	defer func() { _ = file.Close() }()
	data, err := io.ReadAll(io.LimitReader(file, int64(len(complete))+1))
	return err == nil && bytes.Equal(data, []byte(complete))
}

// putArtifact stores recoverable output, reporting a degraded session (no
// store) as the same failure a missing store produces on write.
func (s *Session) putArtifact(data string) (string, error) {
	if s.artifactStore == nil {
		return "", artifactstore.ErrExpired
	}
	return s.artifactStore.Put([]byte(data))
}
