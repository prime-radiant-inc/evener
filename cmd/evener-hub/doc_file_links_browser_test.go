package hub

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/appsource"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
)

var documentFileLinksBrowser = flag.Bool("document-file-links-browser", false, "run isolated document file-links browser fixture")

const documentFileLinksMissingPath = "docs/recovered.md"
const documentFileLinksRecoveredContents = "Q1 RECOVERED CURRENT FILE\n"

func documentFileLinksCreateHandler(cwd string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "POST required", http.StatusMethodNotAllowed)
			return
		}
		if r.URL.RawQuery != "" {
			http.Error(w, "fixture accepts no query parameters", http.StatusBadRequest)
			return
		}
		fixedPath := filepath.Join(cwd, documentFileLinksMissingPath)
		if err := os.MkdirAll(filepath.Dir(fixedPath), 0700); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if err := os.WriteFile(fixedPath, []byte(documentFileLinksRecoveredContents), 0600); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
}

func TestDocumentFileLinksFixtureCreatePost(t *testing.T) {
	t.Parallel()
	cwd := t.TempDir()
	recorder := httptest.NewRecorder()
	documentFileLinksCreateHandler(cwd).ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/doc/fixture/create-missing", nil))
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("POST status = %d, body = %q", recorder.Code, recorder.Body.String())
	}
	contents, err := os.ReadFile(filepath.Join(cwd, "docs", "recovered.md"))
	if err != nil {
		t.Fatal(err)
	}
	if string(contents) != "Q1 RECOVERED CURRENT FILE\n" {
		t.Fatalf("created bytes = %q", contents)
	}
}

func TestDocumentFileLinksFixtureCreateGet(t *testing.T) {
	t.Parallel()
	cwd := t.TempDir()
	recorder := httptest.NewRecorder()
	documentFileLinksCreateHandler(cwd).ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/doc/fixture/create-missing", nil))
	if recorder.Code != http.StatusMethodNotAllowed || recorder.Body.String() != "POST required\n" {
		t.Fatalf("GET status = %d, body = %q", recorder.Code, recorder.Body.String())
	}
	if _, err := os.Stat(filepath.Join(cwd, "docs", "recovered.md")); !os.IsNotExist(err) {
		t.Fatalf("GET created fixed file: %v", err)
	}
}

func TestDocumentFileLinksFixtureCreateQuery(t *testing.T) {
	t.Parallel()
	for _, query := range []string{"path=docs/other.md", "root=other", "content=other"} {
		t.Run(query, func(t *testing.T) {
			t.Parallel()
			cwd := t.TempDir()
			recorder := httptest.NewRecorder()
			documentFileLinksCreateHandler(cwd).ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/doc/fixture/create-missing?"+query, nil))
			if recorder.Code != http.StatusBadRequest || recorder.Body.String() != "fixture accepts no query parameters\n" {
				t.Fatalf("query status = %d, body = %q", recorder.Code, recorder.Body.String())
			}
			for _, name := range []string{"docs/recovered.md", "docs/other.md"} {
				if _, err := os.Stat(filepath.Join(cwd, name)); !os.IsNotExist(err) {
					t.Fatalf("query created %s: %v", name, err)
				}
			}
		})
	}
}

// Only the daemon read boundary is scripted. Document routes read real files.
func TestDocumentFileLinksFixtureDistinctOwner(t *testing.T) {
	t.Parallel()
	const parentID = "02wMz5Txv1C3Hut0M8GCeB"
	const childID = "02wMz5TxvEMoJEDTDGOTil"
	source := &documentFileLinksSource{retirementBrowserRelaySource: newRetirementBrowserRelaySource(parentID, "local:"+parentID), cwd: "/parent",
		child: newRetirementBrowserRelaySource(childID, "local:"+childID), childCWD: "/child"}
	ref := appwire.Ref{SourceID: "local", ThreadID: childID}
	lease, err := source.AcquireRelaySession(ref)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	result, err := lease.Read(t.Context(), appwire.ThreadReadParams{Ref: ref.String()})
	if err != nil {
		t.Fatal(err)
	}
	if result.Response.Thread.ID != childID || result.Response.Thread.Evener.Ref != ref.String() {
		t.Fatalf("child read borrowed parent identity: ID=%q ref=%q", result.Response.Thread.ID, result.Response.Thread.Evener.Ref)
	}
	if result.Response.Thread.CWD != "/child" || result.Response.Thread.Evener.ParentRef != "local:"+parentID {
		t.Fatalf("child binding = %+v", result.Response.Thread)
	}
}

// Only the daemon read boundary is scripted, with independent owner identities.
type documentFileLinksSource struct {
	*retirementBrowserRelaySource
	cwd      string
	child    *retirementBrowserRelaySource
	childCWD string
}

func (s *documentFileLinksSource) AcquireRelaySession(ref appwire.Ref) (appsource.RelaySessionRoutePublicationLease, error) {
	owner, cwd := s.retirementBrowserRelaySource, s.cwd
	if s.child != nil && ref.String() == s.child.ref {
		owner, cwd = s.child, s.childCWD
	} else if ref.String() != s.ref {
		return nil, fmt.Errorf("unknown file-links fixture owner %q", ref.String())
	}
	lease := &scriptedRelaySessionLease{
		readFunc: func(params appwire.ThreadReadParams) (appsource.RelayReadResult, error) {
			thread := owner.buildThread("file-links-instance-" + owner.threadID)
			thread.CWD = cwd
			paragraph := "A retained transcript paragraph for real wheel scrolling.\n\n"
			if owner == s.child {
				thread.Name = "File links delegate child"
				thread.Evener.ParentRef = s.ref
				thread.Evener.Kind = "subagent"
				paragraph = "The delegate inspected its own working directory and retained this reading context.\n\n"
			}
			thread.Turns = []appwire.Turn{{ID: "links", Status: "completed", ItemsView: appwire.TurnItemsViewFragment, Items: []appwire.ThreadItem{{
				Type: "agentMessage", ID: "links-item", TurnID: "links", Status: "completed", TranscriptKey: "links/0", Position: &appwire.ThreadItemPosition{},
				Text: strings.Repeat(paragraph, 45) + "Reading checkpoint: docs/joined-return.md\n\n" + strings.Repeat(paragraph, 15) + "Spec: docs/superpowers/specs/2026-10-02-web-session-overview-design.md\n\nReview: docs/superpowers/specs/2026-10-02-web-session-overview-review.md\n\nImage: docs/current.png\n\nMissing: docs/recovered.md",
			}}}}
			return appsource.RelayReadResult{Response: appwire.ThreadReadResponse{Thread: thread}, Handoff: &guardedRelayHandoff{prepareAllowed: true, commitAllowed: true}}, nil
		}, deliveries: owner.gen1.deliveries,
	}
	return routeAwareTestLease(lease), nil
}

func TestDocumentFileLinksBrowser(t *testing.T) {
	if !*documentFileLinksBrowser {
		t.Skip("run with -document-file-links-browser")
	}
	web, cwd, sessionID := docServeTestServer(t)
	const childID = "02wMz5TxvEMoJEDTDGOTil"
	childCWD := t.TempDir()
	metadataRoot := t.TempDir()
	project := filepath.Join(metadataRoot, "projects", "project-docs-0000000000")
	if err := os.MkdirAll(filepath.Join(project, "sessions"), 0700); err != nil {
		t.Fatal(err)
	}
	for id, dir := range map[string]string{sessionID: cwd, childID: childCWD} {
		if err := schema.SaveSessionMeta(project, schema.SessionMeta{ID: id, UpdatedAt: time.Now(),
			OriginalPrompt: "file-links owning session", EnvInfo: schema.EnvironmentInfo{WorkingDir: dir}}); err != nil {
			t.Fatal(err)
		}
	}
	web.cfg.Past = hubcore.NewPastIndex(filepath.Join(metadataRoot, "projects", "*"))
	if _, err := web.cfg.Past.Rebuild(); err != nil {
		t.Fatal(err)
	}
	for dir, contents := range map[string]string{cwd: "PRIMARY CURRENT CWD FILE\n", childCWD: "DELEGATE CURRENT CWD FILE\n"} {
		if err := os.MkdirAll(filepath.Join(dir, "docs"), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "docs", "joined-return.md"), []byte(contents), 0600); err != nil {
			t.Fatal(err)
		}
	}
	const token = "isolated-file-links-fixture-token"
	web.cfg.AuthToken = token
	imagePath := filepath.Join(cwd, "docs", "current.png")
	writeImage := func(c color.RGBA) error {
		if err := os.MkdirAll(filepath.Dir(imagePath), 0700); err != nil {
			return err
		}
		f, err := os.Create(imagePath)
		if err != nil {
			return err
		}
		img := image.NewRGBA(image.Rect(0, 0, 2, 2))
		for y := range 2 {
			for x := range 2 {
				img.SetRGBA(x, y, c)
			}
		}
		err = png.Encode(f, img)
		closeErr := f.Close()
		if err != nil {
			return err
		}
		return closeErr
	}
	if err := writeImage(color.RGBA{R: 255, A: 255}); err != nil {
		t.Fatal(err)
	}
	for name, contents := range map[string]string{
		"2026-10-02-web-session-overview-design.md": "SPEC CURRENT FILE",
		"2026-10-02-web-session-overview-review.md": "REVIEW CURRENT FILE",
	} {
		path := filepath.Join(cwd, "docs", "superpowers", "specs", name)
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(contents), 0600); err != nil {
			t.Fatal(err)
		}
	}
	ref := "local:" + sessionID
	childRef := "local:" + childID
	source := &documentFileLinksSource{retirementBrowserRelaySource: newRetirementBrowserRelaySource(sessionID, ref), cwd: cwd,
		child: newRetirementBrowserRelaySource(childID, childRef), childCWD: childCWD}
	sources := appsource.NewRegistry()
	sources.Add(source)
	navSource := newTestNavigationSource(time.Unix(1700000000, 0).UTC())
	navSource.inputs.Tree.Projects[0].Current = []hubcore.TreeNode{{ID: sessionID, Title: "File links parent", Project: "p1", Kind: "session", State: "idle",
		Children: []hubcore.TreeNode{{ID: childID, Title: "File links delegate child", Project: "p1", Kind: "subagent", State: "idle"}}}}
	nav := newTestNavigationService(t, navSource)
	if _, err := nav.readV3(t.Context(), navigationResourceKey{Kind: navigationResourceManifest}, nil); err != nil {
		t.Fatal(err)
	}
	app := newHubAppServerWithNavigation(web.cfg, sources, nav, nil)
	mux := http.NewServeMux()
	mux.HandleFunc("/rpc", app.ServeWebSocket)
	mux.Handle("/doc/fixture/create-missing", documentFileLinksCreateHandler(cwd))
	var requests atomic.Int64
	mux.HandleFunc("/doc/fixture/requests", func(w http.ResponseWriter, r *http.Request) { _ = json.NewEncoder(w).Encode(requests.Load()) })
	mux.HandleFunc("/doc/fixture/image", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		c := color.RGBA{R: 255, A: 255}
		if r.URL.Query().Get("color") == "blue" {
			c = color.RGBA{B: 255, A: 255}
		}
		if err := writeImage(c); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	handler := web.Handler()
	mux.Handle("/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/doc/file" || r.URL.Path == "/doc/image" {
			requests.Add(1)
		}
		handler.ServeHTTP(w, r)
	}))
	server := httptest.NewServer(mux)
	defer server.Close()
	artifacts := os.Getenv("DOCUMENT_FILE_LINKS_ARTIFACT_DIR")
	if artifacts == "" {
		artifacts = filepath.Join(t.TempDir(), "artifacts")
	}
	if err := os.MkdirAll(artifacts, 0700); err != nil {
		t.Fatal(err)
	}
	raw, err := os.Create(filepath.Join(artifacts, "guard.log"))
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	cmd := exec.CommandContext(context.Background(), "node", "frontend/scripts/documentfilelinksguard/run.mjs")
	cmd.Env = append(os.Environ(), "EVENER_HUB_ADDR="+server.URL, "DOCUMENT_FILE_LINKS_CWD="+cwd, "DOCUMENT_FILE_LINKS_REF="+ref,
		"DOCUMENT_FILE_LINKS_CHILD_CWD="+childCWD, "DOCUMENT_FILE_LINKS_CHILD_REF="+childRef,
		"DOCUMENT_FILE_LINKS_ARTIFACT_DIR="+artifacts, "DOCUMENT_FILE_LINKS_TOKEN="+token)
	cmd.Stdout = io.MultiWriter(os.Stdout, raw)
	cmd.Stderr = io.MultiWriter(os.Stderr, raw)
	if err := cmd.Run(); err != nil {
		t.Fatalf("documentfilelinksguard: %v, artifacts %s", err, artifacts)
	}
	result, err := os.ReadFile(filepath.Join(artifacts, "result.json"))
	if err != nil {
		t.Fatal(err)
	}
	var record struct {
		Assertions string `json:"assertions"`
	}
	if err := json.Unmarshal(result, &record); err != nil {
		t.Fatal(err)
	}
	if record.Assertions != "pass" {
		t.Fatalf("guard result %s", result)
	}
}
