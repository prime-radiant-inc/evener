package hub

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/nacl/box"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
)

func TestDevicePairingRejectsMalformedNumericHosts(t *testing.T) {
	for _, host := range []string{
		"192.168.1.256", "192.168.1.999999999999999999999", "1.2.3.4.5", "1..2.3",
		"08.1.2.3", "0x100.1.2.3", "0x100000000", "hub.123", "hub.0xff", "hub.0x",
		"１９２．１６８．１．２５６",
	} {
		for _, viaConfig := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/config=%t", host, viaConfig), func(t *testing.T) {
				cfg := hubcore.WebConfig{AuthToken: "synthetic-credential"}
				params := appwire.DevicePairingCreateParams{Origin: "http://" + host + ":9180", Tailnet: true}
				if viaConfig {
					cfg.MobileBaseURL = "https://" + host + ":9180"
					params.Origin = ""
				}
				p := newDevicePairings(cfg)
				invite, err := p.create(params)
				var wireErr appwire.WireError
				if !errors.As(err, &wireErr) || wireErr.Code != appwire.CodeInvalidParams || invite.Invitation != "" || len(p.entries) != 0 {
					t.Fatalf("malformed host created an invitation: code=%d entries=%d err=%v", wireErr.Code, len(p.entries), err)
				}
				p.cfg.MobileBaseURL = ""
				if _, err := p.create(appwire.DevicePairingCreateParams{Origin: "http://magic-kingdom:9180", Tailnet: true}); err != nil || len(p.entries) != 1 {
					t.Fatalf("valid destination did not recover pairing: entries=%d err=%v", len(p.entries), err)
				}
			})
		}
	}
}

func TestDevicePairingUsesSelectedOrigin(t *testing.T) {
	for _, tc := range []struct{ name, proposed, configured, bound, want string }{
		{"automatic bind", "", "", "100.87.98.83:9180", "http://100.87.98.83:9180"},
		{"configured default", "", "https://hub.example.test", "", "https://hub.example.test"},
		{"configured MagicDNS", "", "http://magic-kingdom:9180", "", "http://magic-kingdom:9180"},
		{"direct HTTP", "http://hub.example.test:9180", "", "", "http://hub.example.test:9180"},
		{"edited destination", "http://magic-kingdom:9180", "https://old.example.test", "", "http://magic-kingdom:9180"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := newDevicePairings(hubcore.WebConfig{AuthToken: "synthetic-credential", HubAddr: tc.bound, MobileBaseURL: tc.configured})
			invite, err := p.create(appwire.DevicePairingCreateParams{Origin: tc.proposed})
			if err != nil {
				t.Fatal(err)
			}
			if invite.Origin != tc.want {
				t.Fatalf("origin = %q, want %q", invite.Origin, tc.want)
			}
		})
	}
}

func TestDevicePairingRejectsUnsupportedCredentials(t *testing.T) {
	for _, tc := range []struct{ name, token string }{
		{"empty", ""}, {"too long", strings.Repeat("a", 512)},
		{"embedded space", "synthetic credential"}, {"tab", "synthetic\tcredential"},
		{"control", "synthetic\x7fcredential"}, {"unicode", "synthetic-é"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := newDevicePairings(hubcore.WebConfig{AuthToken: tc.token})
			invite, err := p.create(appwire.DevicePairingCreateParams{Origin: "http://magic-kingdom:9180", Tailnet: true})
			if err == nil || invite.Invitation != "" || len(p.entries) != 0 {
				t.Fatal("created an invitation for a credential the receiver cannot save")
			}
		})
	}
	p := newDevicePairings(hubcore.WebConfig{AuthToken: "!" + strings.Repeat("~", 510)})
	if _, err := p.create(appwire.DevicePairingCreateParams{Origin: "http://magic-kingdom:9180", Tailnet: true}); err != nil {
		t.Fatal(err)
	}
}

func TestDevicePairingIncompleteBodyTimesOut(t *testing.T) {
	requestBody := `{"invitation":"` + strings.Repeat("01", 24) + `","publicKey":"9bd266e486eedd293ba03546df3a37b7666c2cc62e5d5063944d50eea04cb749","client":"mobile"}`
	for _, tc := range []struct{ name, body string }{{"partial JSON", `{"invitation":`}, {"missing body EOF", requestBody}} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			web := NewWebServer(hubcore.WebConfig{AuthToken: "synthetic-credential"})
			server := httptest.NewServer(web.Handler())
			defer server.Close()
			conn, err := net.Dial("tcp", server.Listener.Addr().String())
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			if err := conn.SetDeadline(time.Now().Add(10 * time.Second)); err != nil {
				t.Fatal(err)
			}
			// Leave the declared body unfinished, including after a complete JSON value.
			if _, err := fmt.Fprintf(conn, "POST /api/device-pairing HTTP/1.1\r\nHost: hub\r\nContent-Type: application/json\r\nContent-Length: %d\r\n\r\n%s", len(tc.body)+1, tc.body); err != nil {
				t.Fatal(err)
			}
			response, err := http.ReadResponse(bufio.NewReader(conn), nil)
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			if response.StatusCode != http.StatusRequestTimeout || !response.Close {
				t.Fatalf("incomplete request: status=%d close=%t", response.StatusCode, response.Close)
			}
			if _, err := io.Copy(io.Discard, response.Body); err != nil {
				t.Fatal(err)
			}
			// A timed-out sender must not strand subsequent pairing requests.
			retry, err := server.Client().Post(server.URL+"/api/device-pairing", "application/json", strings.NewReader(requestBody))
			if err != nil {
				t.Fatal(err)
			}
			defer retry.Body.Close()
			var reply appwire.DevicePairingClaimResponse
			if err := json.NewDecoder(retry.Body).Decode(&reply); err != nil {
				t.Fatal(err)
			}
			if retry.StatusCode != http.StatusOK || reply.State != "expired" {
				t.Fatalf("retry: status=%d reply=%+v", retry.StatusCode, reply)
			}
		})
	}
}

func pairingRPC[T any](t *testing.T, web *WebServer, method string, params any) (T, error) {
	t.Helper()
	raw, err := json.Marshal(params)
	if err != nil {
		t.Fatal(err)
	}
	result, err := web.appRPC.Router().Dispatch(context.Background(), appwire.Request{ID: appwire.NewIntID(1), Method: method, Params: raw})
	var out T
	if err == nil {
		var ok bool
		out, ok = result.(T)
		if !ok {
			t.Fatalf("response type %T", result)
		}
	}
	return out, err
}
func pairingClaimHTTP(t *testing.T, web *WebServer, invitation, publicKey string) (appwire.DevicePairingClaimResponse, int) {
	t.Helper()
	raw, _ := json.Marshal(appwire.DevicePairingClaimParams{Invitation: invitation, PublicKey: publicKey, Client: "mobile"})
	req := httptest.NewRequest(http.MethodPost, "/api/device-pairing", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	web.Handler().ServeHTTP(w, req)
	var out appwire.DevicePairingClaimResponse
	if w.Code == http.StatusOK {
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
	}
	return out, w.Code
}
func TestDevicePairingStrongFingerprint(t *testing.T) {
	t.Parallel()
	p := newDevicePairings(hubcore.WebConfig{AuthToken: "synthetic-credential"})
	invite := appwire.DevicePairingInvitation{Invitation: strings.Repeat("01", 24), Origin: "http://magic-kingdom:9180", Tailnet: true, ExpiresAt: p.now().Add(time.Minute).UnixMilli()}
	p.entries[invite.Invitation] = &devicePairingEntry{invitation: invite}
	// Both public keys reduce to the same six-digit value, 877735.
	for _, tc := range []struct{ public, comparison string }{
		{"9bd266e486eedd293ba03546df3a37b7666c2cc62e5d5063944d50eea04cb749", "2be9-271d-6222-d455-d2d1-c217-62b2-3b82"},
		{"6721d39d41d5b7af37fb001fe6c206f5ba0014588d9453b354efc12b98708054", "1ca6-e724-b024-83d3-5bba-fbcb-6c57-d342"},
	} {
		reply, status := p.claim(appwire.DevicePairingClaimParams{Invitation: invite.Invitation, PublicKey: tc.public, Client: "Phone"})
		if status != http.StatusOK || reply.Comparison != tc.comparison {
			t.Fatalf("fingerprint: %+v, status %d; want %s", reply, status, tc.comparison)
		}
	}
}

func TestDevicePairingAmbiguousFingerprintCannotReleaseCredential(t *testing.T) {
	t.Parallel()
	p := newDevicePairings(hubcore.WebConfig{AuthToken: "synthetic-credential"})
	invite, err := p.create(appwire.DevicePairingCreateParams{Origin: "http://magic-kingdom:9180", Tailnet: true})
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		public, _, err := box.GenerateKey(rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		if _, status := p.claim(appwire.DevicePairingClaimParams{Invitation: invite.Invitation, PublicKey: hex.EncodeToString(public[:]), Client: "Phone"}); status != http.StatusOK {
			t.Fatal(status)
		}
	}
	entry := p.entries[invite.Invitation]
	// Represent a hash collision directly rather than searching a 128-bit space.
	entry.claims[1].Comparison = entry.claims[0].Comparison
	for _, claim := range entry.claims {
		if _, err := p.approve(appwire.DevicePairingApproveParams{Invitation: invite.Invitation, PublicKey: claim.PublicKey}); err == nil {
			t.Fatal("ambiguous approval succeeded")
		}
		if entry.approvedKey != "" || entry.envelope != "" {
			t.Fatal("ambiguous approval released a credential")
		}
	}
}

func TestDevicePairingApprovalBindsOneKeyAndRetries(t *testing.T) {
	t.Parallel()
	web := NewWebServer(hubcore.WebConfig{AuthToken: "private-hub-credential"})
	invite, err := pairingRPC[appwire.DevicePairingInvitation](t, web, "evener/devicePairing/create", appwire.DevicePairingCreateParams{Origin: "http://magic-kingdom:9180", Tailnet: true})
	if err != nil {
		t.Fatal(err)
	}
	public, secret, err := box.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	key := hex.EncodeToString(public[:])
	claim, code := pairingClaimHTTP(t, web, invite.Invitation, key)
	if code != 200 || claim.State != "pending" || claim.Envelope != "" {
		t.Fatalf("unapproved claim: %+v, %d", claim, code)
	}
	rival, rivalSecret, _ := box.GenerateKey(rand.Reader)
	rivalKey := hex.EncodeToString(rival[:])
	if _, status := pairingClaimHTTP(t, web, invite.Invitation, rivalKey); status != http.StatusOK {
		t.Fatalf("rival claim status: %d", status)
	}
	read, err := pairingRPC[appwire.DevicePairingStatus](t, web, "evener/devicePairing/read", appwire.DevicePairingParams{Invitation: invite.Invitation})
	if err != nil || len(read.Claims) != 2 {
		t.Fatalf("claims: %+v, %v", read, err)
	}
	// Short comparison codes can collide; full public keys own distinct claims.
	if read.Claims[0].PublicKey != key || read.Claims[1].PublicKey != rivalKey {
		t.Fatalf("claim ownership: %+v", read.Claims)
	}
	_, err = pairingRPC[appwire.DevicePairingStatus](t, web, "evener/devicePairing/approve", appwire.DevicePairingApproveParams{Invitation: invite.Invitation, PublicKey: key})
	if err != nil {
		t.Fatal(err)
	}
	approved, _ := pairingClaimHTTP(t, web, invite.Invitation, key)
	retry, _ := pairingClaimHTTP(t, web, invite.Invitation, key)
	if approved.State != "approved" || approved.Envelope == "" || approved != retry {
		t.Fatal("approved claim not recoverable")
	}
	denied, _ := pairingClaimHTTP(t, web, invite.Invitation, rivalKey)
	if denied.State != "rejected" || denied.Envelope != "" {
		t.Fatal("rival received credentials")
	}
	envelope, err := hex.DecodeString(approved.Envelope)
	if err != nil {
		t.Fatal(err)
	}
	var sender [32]byte
	copy(sender[:], envelope[12:44])
	var nonce [24]byte
	copy(nonce[:], envelope[44:68])
	plain, ok := box.Open(nil, envelope[68:], &nonce, &sender, secret)
	if !ok || !bytes.HasSuffix(plain, []byte("private-hub-credential")) {
		t.Fatal("credential not encrypted to approved key")
	}
	if _, ok := box.Open(nil, envelope[68:], &nonce, &sender, rivalSecret); ok {
		t.Fatal("rival decrypted credential")
	}
	_, err = pairingRPC[appwire.DevicePairingStatus](t, web, "evener/devicePairing/approve", appwire.DevicePairingApproveParams{Invitation: invite.Invitation, PublicKey: rivalKey})
	if err == nil {
		t.Fatal("approval changed winner")
	}
}
func TestDevicePairingExpiryCancelAndBounds(t *testing.T) {
	t.Parallel()
	web := NewWebServer(hubcore.WebConfig{AuthToken: "secret"})
	now := time.Unix(1700000000, 0)
	web.pairings.now = func() time.Time { return now }
	create := func() appwire.DevicePairingInvitation {
		v, err := pairingRPC[appwire.DevicePairingInvitation](t, web, "evener/devicePairing/create", appwire.DevicePairingCreateParams{Origin: "http://100.64.1.2:9180", Tailnet: true})
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	invite := create()
	if _, status := pairingClaimHTTP(t, web, invite.Invitation, "00"); status != 400 {
		t.Fatalf("bad key status %d", status)
	}
	if _, status := pairingClaimHTTP(t, web, invite.Invitation, string(bytes.Repeat([]byte("0"), 64))); status != 400 {
		t.Fatalf("low order key status %d", status)
	}
	for range 8 {
		pub, _, _ := box.GenerateKey(rand.Reader)
		if _, status := pairingClaimHTTP(t, web, invite.Invitation, hex.EncodeToString(pub[:])); status != 200 {
			t.Fatal(status)
		}
	}
	pub, _, _ := box.GenerateKey(rand.Reader)
	key := hex.EncodeToString(pub[:])
	if _, status := pairingClaimHTTP(t, web, invite.Invitation, key); status != 429 {
		t.Fatal(status)
	}
	now = now.Add(10 * time.Minute)
	expired, status := pairingClaimHTTP(t, web, invite.Invitation, key)
	if status != 200 || expired.State != "expired" {
		t.Fatalf("expiry %+v %d", expired, status)
	}
	invite = create()
	if _, err := pairingRPC[appwire.DevicePairingStatus](t, web, "evener/devicePairing/cancel", appwire.DevicePairingParams{Invitation: invite.Invitation}); err != nil {
		t.Fatal(err)
	}
	expired, _ = pairingClaimHTTP(t, web, invite.Invitation, key)
	if expired.State != "expired" {
		t.Fatal("cancel did not revoke")
	}
	// The bootstrap exception must not make the authenticated RPC transport public.
	w := httptest.NewRecorder()
	web.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/rpc", nil))
	if w.Code != 401 {
		t.Fatalf("RPC lost auth: %d", w.Code)
	}
	w = httptest.NewRecorder()
	web.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/device-pairing", nil))
	if w.Code != 405 {
		t.Fatalf("bootstrap GET: %d", w.Code)
	}
}

func TestDevicePairingSupportsGenericClientsAndIPv6(t *testing.T) {
	t.Parallel()
	p := newDevicePairings(hubcore.WebConfig{AuthToken: "credential"})
	invite, err := p.create(appwire.DevicePairingCreateParams{Origin: "https://[fd7a:115c:a1e0::1]:9180", Tailnet: true})
	if err != nil {
		t.Fatal(err)
	}
	pub, _, _ := box.GenerateKey(rand.Reader)
	reply, status := p.claim(appwire.DevicePairingClaimParams{Invitation: invite.Invitation, PublicKey: hex.EncodeToString(pub[:]), Client: "Kitchen terminal"})
	if status != 200 || reply.State != "pending" {
		t.Fatalf("generic client: %+v %d", reply, status)
	}
}

func TestDevicePairingCanonicalDestinationMatchesEnvelope(t *testing.T) {
	for _, tc := range []struct{ input, origin string }{
		{"http://MAGIC-KINGDOM:80/", "http://magic-kingdom"},
		{"https://BÜCHER.example:443", "https://xn--bcher-kva.example"},
		{"https://[fd7a:115c:a1e0:0:0:0:0:1]:9180", "https://[fd7a:115c:a1e0::1]:9180"},
	} {
		t.Run(tc.input, func(t *testing.T) {
			p := newDevicePairings(hubcore.WebConfig{AuthToken: "credential"})
			invite, err := p.create(appwire.DevicePairingCreateParams{Origin: tc.input, Tailnet: true})
			if err != nil || invite.Origin != tc.origin {
				t.Fatalf("canonical invitation: %+v, %v", invite, err)
			}
			pub, secret, _ := box.GenerateKey(rand.Reader)
			sealed, err := sealDeviceCredential(invite, hex.EncodeToString(pub[:]), "credential")
			if err != nil {
				t.Fatal(err)
			}
			envelope, _ := hex.DecodeString(sealed)
			var sender [32]byte
			copy(sender[:], envelope[12:44])
			var nonce [24]byte
			copy(nonce[:], envelope[44:68])
			plain, ok := box.Open(nil, envelope[68:], &nonce, &sender, secret)
			if !ok {
				t.Fatal("could not open envelope")
			}
			origin, _ := url.Parse(tc.origin)
			if string(plain[8:8+int(plain[4])]) != origin.Hostname() {
				t.Fatal("envelope destination differs from invitation")
			}
		})
	}
}
