package hub

import (
	"crypto/ecdh"
	"crypto/rand"
	"crypto/sha512"
	"encoding/hex"
	"net"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/nacl/box"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
)

const devicePairingLifetime = 10 * time.Minute

type devicePairingEntry struct {
	invitation            appwire.DevicePairingInvitation
	claims                []appwire.DevicePairingClaim
	approvedKey, envelope string
}

// Pending invitations are process-local: restarting a Hub revokes them without
// touching existing client credentials. The mutex makes approval single-winner.
type devicePairings struct {
	mu      sync.Mutex
	entries map[string]*devicePairingEntry
	cfg     hubcore.WebConfig
	now     func() time.Time
}

func newDevicePairings(cfg hubcore.WebConfig) *devicePairings {
	return &devicePairings{entries: make(map[string]*devicePairingEntry), cfg: cfg, now: time.Now}
}
func (p *devicePairings) prune() {
	for key, entry := range p.entries {
		if p.now().UnixMilli() >= entry.invitation.ExpiresAt {
			delete(p.entries, key)
		}
	}
}
func (p *devicePairings) create(params appwire.DevicePairingCreateParams) (appwire.DevicePairingInvitation, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.prune()
	origin := params.Origin
	if p.cfg.MobileBaseURL != "" {
		origin = p.cfg.MobileBaseURL
	}
	u, err := url.Parse(origin)
	if err != nil || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") || (u.Scheme != "http" && u.Scheme != "https") {
		return appwire.DevicePairingInvitation{}, appwire.InvalidParams("pairing requires a Hub HTTP(S) origin")
	}
	// Authenticated operators may select a MagicDNS name for a native tailnet
	// connection. Validate its spelling and loopback status using the HTTPS rule;
	// public plaintext delivery otherwise retains the mobile origin restriction.
	check := *u
	if params.Tailnet {
		check.Scheme = "https"
	}
	_, ok := safeMobileOrigin(check.String())
	if !ok {
		return appwire.DevicePairingInvitation{}, appwire.InvalidParams("choose a reachable Hub origin")
	}
	// The invitation encoder uses the browser's URL spelling. Seal the same
	// canonical ASCII destination so clients can compare bytes after decryption.
	host := strings.TrimRight(u.Hostname(), ".")
	if address, parseErr := netip.ParseAddr(host); parseErr == nil {
		host = address.Unmap().String()
	} else {
		host, err = mobileHostnameProfile.ToASCII(host)
		if err != nil {
			return appwire.DevicePairingInvitation{}, appwire.InvalidParams("choose a reachable Hub origin")
		}
		host = strings.ToLower(strings.TrimRight(host, "."))
	}
	if len(host) == 0 || len(host) > 255 || strings.Trim(host, "abcdefghijklmnopqrstuvwxyz0123456789.:-") != "" {
		return appwire.DevicePairingInvitation{}, appwire.InvalidParams("choose a reachable Hub origin")
	}
	port := u.Port()
	if (u.Scheme == "http" && port == "80") || (u.Scheme == "https" && port == "443") {
		port = ""
	}
	if port != "" {
		host = net.JoinHostPort(host, port)
	} else if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	origin = (&url.URL{Scheme: u.Scheme, Host: host}).String()
	if len(p.cfg.AuthToken) == 0 || len(p.cfg.AuthToken) > 511 || strings.IndexFunc(p.cfg.AuthToken, func(c rune) bool { return c < '!' || c > '~' }) >= 0 {
		return appwire.DevicePairingInvitation{}, appwire.Conflict("pairing requires a Hub credential of 1–511 non-space printable ASCII characters")
	}
	if len(p.entries) >= 16 {
		return appwire.DevicePairingInvitation{}, appwire.Conflict("too many active invitations; cancel one or wait for expiry")
	}
	var secret [24]byte
	if _, err = rand.Read(secret[:]); err != nil {
		return appwire.DevicePairingInvitation{}, err
	}
	invite := appwire.DevicePairingInvitation{Invitation: hex.EncodeToString(secret[:]), Origin: origin, Tailnet: params.Tailnet, ExpiresAt: p.now().Add(devicePairingLifetime).UnixMilli()}
	p.entries[invite.Invitation] = &devicePairingEntry{invitation: invite, claims: []appwire.DevicePairingClaim{}}
	return invite, nil
}
func pairingStatus(entry *devicePairingEntry) appwire.DevicePairingStatus {
	if entry == nil {
		return appwire.DevicePairingStatus{State: "expired", Claims: []appwire.DevicePairingClaim{}}
	}
	state := "pending"
	if entry.approvedKey != "" {
		state = "approved"
	}
	return appwire.DevicePairingStatus{State: state, Claims: append([]appwire.DevicePairingClaim{}, entry.claims...), ApprovedKey: entry.approvedKey}
}
func (p *devicePairings) read(params appwire.DevicePairingParams) (appwire.DevicePairingStatus, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.prune()
	return pairingStatus(p.entries[params.Invitation]), nil
}
func (p *devicePairings) cancel(params appwire.DevicePairingParams) (appwire.DevicePairingStatus, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.entries, params.Invitation)
	return pairingStatus(nil), nil
}
func (p *devicePairings) approve(params appwire.DevicePairingApproveParams) (appwire.DevicePairingStatus, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.prune()
	entry := p.entries[params.Invitation]
	if entry == nil {
		return pairingStatus(nil), appwire.Conflict("pairing invitation expired")
	}
	if entry.approvedKey != "" && entry.approvedKey != params.PublicKey {
		return pairingStatus(entry), appwire.Conflict("invitation already approved for another device")
	}
	if entry.approvedKey != "" {
		return pairingStatus(entry), nil
	}
	comparison := ""
	for _, claim := range entry.claims {
		if claim.PublicKey == params.PublicKey {
			comparison = claim.Comparison
			break
		}
	}
	if comparison == "" {
		return pairingStatus(entry), appwire.InvalidParams("unknown device claim")
	}
	for _, claim := range entry.claims {
		if claim.PublicKey != params.PublicKey && claim.Comparison == comparison {
			return pairingStatus(entry), appwire.Conflict("ambiguous device fingerprints; create a new invitation")
		}
	}
	envelope, err := sealDeviceCredential(entry.invitation, params.PublicKey, p.cfg.AuthToken)
	if err != nil {
		return pairingStatus(entry), err
	}
	entry.envelope = envelope
	entry.approvedKey = params.PublicKey
	return pairingStatus(entry), nil
}
func (p *devicePairings) claim(params appwire.DevicePairingClaimParams) (appwire.DevicePairingClaimResponse, int) {
	// Bound all attacker-controlled inputs before key agreement or state mutation.
	invitation, err := hex.DecodeString(params.Invitation)
	if err != nil || len(invitation) != 24 || hex.EncodeToString(invitation) != params.Invitation {
		return appwire.DevicePairingClaimResponse{}, 400
	}
	public, err := hex.DecodeString(params.PublicKey)
	if err != nil || len(public) != 32 || hex.EncodeToString(public) != params.PublicKey || !validDeviceLabel(params.Client) {
		return appwire.DevicePairingClaimResponse{}, 400
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.prune()
	entry := p.entries[params.Invitation]
	if entry == nil {
		return appwire.DevicePairingClaimResponse{State: "expired"}, 200
	}
	for _, claim := range entry.claims {
		if claim.PublicKey == params.PublicKey {
			result := appwire.DevicePairingClaimResponse{State: "pending", Comparison: claim.Comparison}
			if entry.approvedKey == params.PublicKey {
				result.State = "approved"
				result.Envelope = entry.envelope
			} else if entry.approvedKey != "" {
				result.State = "rejected"
			}
			return result, 200
		}
	}
	if entry.approvedKey != "" {
		return appwire.DevicePairingClaimResponse{State: "rejected"}, 200
	}
	if len(entry.claims) >= 8 {
		return appwire.DevicePairingClaimResponse{}, 429
	}
	key, err := ecdh.X25519().NewPublicKey(public)
	if err != nil {
		return appwire.DevicePairingClaimResponse{}, 400
	}
	private, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return appwire.DevicePairingClaimResponse{}, 500
	}
	if _, err = private.ECDH(key); err != nil {
		return appwire.DevicePairingClaimResponse{}, 400
	}
	// Domain separation and the full invitation bind the displayed fingerprint to
	// exactly one device key. Clients calculate it independently before approval.
	digest := sha512.Sum512(append(append([]byte("evener-pairing-v1"), invitation...), public...))
	var groups [8]string
	for i := range groups {
		groups[i] = hex.EncodeToString(digest[i*2 : i*2+2])
	}
	comparison := strings.Join(groups[:], "-")
	entry.claims = append(entry.claims, appwire.DevicePairingClaim{PublicKey: params.PublicKey, Client: params.Client, Comparison: comparison})
	return appwire.DevicePairingClaimResponse{State: "pending", Comparison: comparison}, 200
}
func validDeviceLabel(label string) bool {
	if len(label) == 0 || len(label) > 48 {
		return false
	}
	for _, r := range label {
		if r < 32 || r > 126 {
			return false
		}
	}
	return true
}

func sealDeviceCredential(invite appwire.DevicePairingInvitation, key, token string) (string, error) {
	u, _ := url.Parse(invite.Origin)
	port := 80
	if u.Scheme == "https" {
		port = 443
	}
	if u.Port() != "" {
		port, _ = strconv.Atoi(u.Port())
	}
	host, path := u.Hostname(), "/rpc"
	flags := byte(0)
	if invite.Tailnet {
		flags |= 1
	}
	if u.Scheme == "https" {
		flags |= 2
	}
	plain := []byte{1, flags, byte(port >> 8), byte(port), byte(len(host)), byte(len(path)), byte(len(token) >> 8), byte(len(token))}
	plain = append(plain, host...)
	plain = append(plain, path...)
	plain = append(plain, token...)
	public, secret, err := box.GenerateKey(rand.Reader)
	if err != nil {
		return "", err
	}
	var nonce [24]byte
	if _, err = rand.Read(nonce[:]); err != nil {
		return "", err
	}
	var recipient [32]byte
	raw, _ := hex.DecodeString(key)
	copy(recipient[:], raw)
	invitation, _ := hex.DecodeString(invite.Invitation)
	envelope := append([]byte("EVP1"), invitation[:8]...)
	envelope = append(envelope, public[:]...)
	envelope = append(envelope, nonce[:]...)
	envelope = box.Seal(envelope, plain, &nonce, &recipient, secret)
	return hex.EncodeToString(envelope), nil
}
