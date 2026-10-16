package hubendpoint

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync/atomic"
	"testing"
	"time"
)

func TestHubBearerOriginIsolation(t *testing.T) {
	t.Parallel()
	const token = "fixture-bearer-credential"
	var healthCalls atomic.Int32
	var targetAuth, otherAuth, tlsAuth atomic.Value
	targetAuth.Store("")
	otherAuth.Store("")
	tlsAuth.Store("")
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	record := testRecord(t.TempDir())
	record.BoundAddr = ln.Addr().String()
	record.LocalURL = "http://" + record.BoundAddr
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		otherAuth.Store(r.Header.Get("Authorization"))
		w.WriteHeader(http.StatusNoContent)
	}))
	defer other.Close()
	tls := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tlsAuth.Store(r.Header.Get("Authorization"))
		w.WriteHeader(http.StatusNoContent)
	}))
	defer tls.Close()
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/health":
			healthCalls.Add(1)
			if r.Header.Get("Authorization") != "" {
				t.Error("health probe disclosed credential")
			}
			_ = json.NewEncoder(w).Encode(VerifiedHealth{BootID: record.BootID, HubAddr: record.BoundAddr, StartedAt: record.StartedAt})
		case "/redirect-other":
			targetAuth.Store(r.Header.Get("Authorization"))
			http.Redirect(w, r, other.URL, http.StatusFound)
		case "/redirect-tls":
			targetAuth.Store(r.Header.Get("Authorization"))
			http.Redirect(w, r, tls.URL, http.StatusFound)
		case "/ok":
			targetAuth.Store(r.Header.Get("Authorization"))
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	})}
	done := make(chan error, 1)
	go func() { done <- srv.Serve(ln) }()
	defer func() { _ = srv.Close(); <-done }()
	port := strconv.Itoa(ln.Addr().(*net.TCPAddr).Port)
	origin, err := ParseOrigin("HTTP://LOCALHOST:" + port + "/")
	if err != nil {
		t.Fatal(err)
	}
	base := &http.Client{Transport: &http.Transport{Proxy: http.ProxyFromEnvironment}}
	client, err := HTTPClientWithBearer(base, token, origin, &record)
	if err != nil {
		t.Fatal(err)
	}
	if base.Transport == client.Transport {
		t.Fatal("changed shared transport")
	}
	for _, path := range []string{"/ok", "/redirect-other", "/redirect-tls"} {
		resp, err := client.Get(origin.String() + path)
		if path == "/ok" {
			if err != nil {
				t.Fatal(err)
			}
			_ = resp.Body.Close()
			if resp.StatusCode != http.StatusNoContent {
				t.Fatalf("status %d", resp.StatusCode)
			}
		} else if err == nil {
			_ = resp.Body.Close()
			t.Fatal("authenticated redirect followed")
		}
	}
	if got := targetAuth.Load().(string); got != "Bearer "+token {
		t.Fatalf("selected origin auth = %q", got)
	}
	if got := otherAuth.Load().(string); got != "" {
		t.Fatal("different-port server received credential")
	}
	if got := tlsAuth.Load().(string); got != "" {
		t.Fatal("TLS destination received credential")
	}
	for _, destination := range []string{other.URL + "/other", tls.URL + "/tls"} {
		resp, err := client.Get(destination)
		if err == nil {
			_ = resp.Body.Close()
			t.Fatal("different origin accepted by pinned local transport")
		}
	}
	if got := healthCalls.Load(); got != 1 {
		t.Fatalf("health verification calls = %d, want one reused connection", got)
	}
}

func TestHubBearerRefusesTLSDowngradeRedirect(t *testing.T) {
	t.Parallel()
	const bearer = "tls-downgrade-sentinel"
	var selectedBearer, downgradeRequests atomic.Int32
	downgrade := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		downgradeRequests.Add(1)
		if r.Header.Get("Authorization") != "" {
			t.Error("HTTP downgrade target received a bearer")
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer downgrade.Close()
	selected := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "Bearer "+bearer {
			selectedBearer.Add(1)
		}
		http.Redirect(w, r, downgrade.URL+"/target", http.StatusFound)
	}))
	defer selected.Close()
	origin, err := ParseOrigin(selected.URL)
	if err != nil {
		t.Fatal(err)
	}
	client, err := HTTPClientWithBearer(selected.Client(), bearer, origin, nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := client.Get(selected.URL + "/protected")
	if err == nil {
		_ = response.Body.Close()
		t.Fatal("followed selected TLS origin redirect")
	}
	if selectedBearer.Load() != 1 || downgradeRequests.Load() != 0 {
		t.Fatalf("selected bearer requests=%d, downgrade target requests=%d", selectedBearer.Load(), downgradeRequests.Load())
	}
}

func TestHubBearerVerifiesEveryConnectionAndRejectsPortReplacement(t *testing.T) {
	t.Parallel()
	const token = "port-replacement-sentinel"
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := ln.Addr().String()
	record := testRecord(t.TempDir())
	record.BoundAddr = address
	record.LocalURL = "http://" + address
	var authenticated atomic.Int32
	original := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/health" {
			_ = json.NewEncoder(w).Encode(VerifiedHealth{BootID: record.BootID, HubAddr: record.BoundAddr, StartedAt: record.StartedAt})
			return
		}
		if r.Header.Get("Authorization") == "Bearer "+token {
			authenticated.Add(1)
		}
		w.Header().Set("Connection", "close")
		w.WriteHeader(http.StatusNoContent)
	})}
	done := make(chan error, 1)
	go func() { done <- original.Serve(ln) }()
	origin, err := ParseOrigin(record.LocalURL)
	if err != nil {
		t.Fatal(err)
	}
	client, err := HTTPClientWithBearer(&http.Client{}, token, origin, &record)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := client.Get(origin.String() + "/first")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if authenticated.Load() != 1 {
		t.Fatal("first connection failed authentication")
	}
	// The first response closed its connection. Replace the listener before the next dial.
	_ = original.Close()
	<-done
	replacementListener, err := net.Listen("tcp", address)
	if err != nil {
		t.Fatal(err)
	}
	var seenAuth atomic.Int32
	replacement := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			seenAuth.Add(1)
		}
		if r.URL.Path == "/api/health" {
			_ = json.NewEncoder(w).Encode(VerifiedHealth{BootID: "wrong-instance", HubAddr: address, StartedAt: time.Now()})
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})}
	replacedDone := make(chan error, 1)
	go func() { replacedDone <- replacement.Serve(replacementListener) }()
	defer func() { _ = replacement.Close(); <-replacedDone }()
	resp, err = client.Get(origin.String() + "/second")
	if err == nil {
		_ = resp.Body.Close()
		t.Fatal("replacement accepted an authenticated request")
	}
	if !errors.Is(err, ErrIdentityMismatch) {
		t.Fatalf("replacement error = %v, want identity mismatch", err)
	}
	if seenAuth.Load() != 0 {
		t.Fatal("replacement observed bearer credential")
	}
}

func TestHubBearerExplicitScopeAndNoToken(t *testing.T) {
	t.Parallel()
	var seen atomic.Value
	seen.Store("")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen.Store(r.Header.Get("Authorization"))
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	origin, err := ParseOrigin(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	base := &http.Client{}
	plain, err := HTTPClientWithBearer(base, "", origin, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := plain.Get(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if got := seen.Load().(string); got != "" {
		t.Fatalf("empty token sent %q", got)
	}
	scoped, err := HTTPClientWithBearer(base, "explicit", origin, nil)
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, server.URL+"/", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err = scoped.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if got := seen.Load().(string); got != "Bearer explicit" {
		t.Fatalf("explicit token = %q", got)
	}
	if req.Header.Get("Authorization") != "" {
		t.Fatal("mutated caller request")
	}
}
