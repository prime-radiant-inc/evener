package hub

import (
	"cmp"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"

	"primeradiant.com/evener/appwire"
	authopenai "primeradiant.com/evener/auth/openai"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/llm/registry"
)

// hubInstancesController manages provider instance CRUD: Create, Edit,
// Remove, SetDefault, and List. It is the only writer of providers.toml
// (spec §11.3): every read comes from the registry, every write goes through
// the registry's config writer and is followed by a reload, and a file the
// registry could not parse is never rewritten.
type hubInstancesController struct {
	reg                 *hubcore.ProviderRegistry
	providersConfigPath string
	auth                *hubAuthController
	// mu is held exclusively across every mutation's read, write and reload,
	// and shared by List across its whole snapshot: a listing must not pair the
	// authored layer of one generation of providers.toml with the registry view
	// of another. Every controller lock is taken in the order mu then
	// auth.credMu, List included, so the read side adds no ordering.
	mu sync.RWMutex
}

func (c *hubInstancesController) read() (*registry.Layer, bool, error) {
	return registry.ReadConfigFile(c.providersConfigPath)
}

func (c *hubInstancesController) write(l *registry.Layer) error {
	return registry.WriteConfigFile(c.providersConfigPath, l)
}

// List returns every instance the registry currently holds, each with its
// credential status, plus the providers an add form can build on and the
// diagnostics the pane shows above them.
func (c *hubInstancesController) List() appwire.InstanceListResponse {
	// The fingerprint key is resolved once, before either lock is taken:
	// resolving it can repair the file (an inter-process lock and a write), and
	// holding the credential lock across that would let a listing stall every
	// credential writer behind a contended state root. One key for the whole
	// listing also keeps every row keyed the same way if a repair lands beside
	// it.
	key, keyErr := resolveEndpointFingerprintKey(c.authStateDir())
	// The registry snapshot, the reread of providers.toml and every row built
	// from both are one view (entryFor): a mutation's write lands the authored
	// fields before its reload commits the resolved endpoint, so a listing that
	// ran between them without this lock would serve a row carrying one
	// generation's credential fields beside the other's URL and fingerprint.
	c.mu.RLock()
	defer c.mu.RUnlock()
	// The shared side of credMu covers the rest of the same snapshot: a logout
	// or a providers.toml mutation holds it exclusively while it changes the
	// credential state and the registry view, so a listing that ran through that
	// section would pair one generation's credential status with another's
	// membership, endpoint and fingerprints. The order is the documented mu then
	// credMu, and a bare controller with no auth has nothing to hold.
	if c.auth != nil {
		c.auth.credMu.RLock()
		defer c.auth.credMu.RUnlock()
	}
	entries := make([]appwire.InstanceEntry, 0)
	providers := make([]appwire.ProviderDescriptor, 0)
	userLayer := ""
	r := c.reg.Get()
	if r != nil {
		// The authored layer is what the sheet's form edits, so the entry
		// carries the authored credential fields alongside the registry's
		// resolved view. A file that cannot be read right now simply
		// prefills nothing; the refusal itself is already in Diagnostics.
		var layer *registry.Layer
		if l, exists, err := c.read(); err == nil && exists {
			layer = l
		}
		for _, inst := range r.Instances() {
			var authored *registry.Provider
			if layer != nil {
				if p, ok := layer.Providers[inst.Name]; ok {
					authored = &p
				}
			}
			entries = append(entries, c.entryFor(r, inst, authored, key))
		}
		for _, id := range r.ProviderIDs() {
			p, ok := r.Provider(id)
			if !ok {
				continue
			}
			// Only the variables a URL template reads become inputs; the
			// rest of vars_env (a credential's own variable) has no
			// instance-level meaning the add form could give it.
			vars := r.TemplateVarsEnv(id)
			var setup *appwire.InstanceEntry
			inst, addressable := r.Instance(id)
			if !addressable {
				inst, addressable = resolvedInstanceFor(r, id, p.Hidden)
			}
			if addressable {
				var authored *registry.Provider
				if layer != nil {
					if p, ok := layer.Providers[id]; ok {
						authored = &p
					}
				}
				entry := c.entryFor(r, inst, authored, key)
				setup = &entry
			}
			providers = append(providers, appwire.ProviderDescriptor{
				ID:        id,
				Name:      p.Name,
				Protocol:  p.Protocol,
				Auth:      p.Transport.Auth,
				VarsEnv:   slices.Sorted(maps.Values(vars)),
				Vars:      vars,
				APIKeyEnv: append([]string(nil), p.APIKeyEnv...),
				Implicit:  registry.BoolValue(p.Implicit),
				AuthModes: authModesFor(p.Transport.Auth),
				Setup:     setup,
			})
		}
		userLayer = r.UserLayerNote()
	}
	diagnostics := c.reg.Diagnostics()
	// The pane has to be able to say why every fingerprint is missing: a state
	// root whose key cannot be read or written is what omits them, and the
	// writes that would otherwise assert one are refused (verifyEndpointFingerprint).
	if keyDiagnostic := fingerprintKeyDiagnostic(keyErr); keyDiagnostic != "" {
		diagnostics = append(diagnostics, keyDiagnostic)
	}
	return appwire.InstanceListResponse{
		Instances:          entries,
		AvailableProviders: providers,
		Diagnostics:        diagnostics,
		UserLayer:          userLayer,
		// The wire bit is the refusal the mutators would give, asked once, so
		// the pane cannot offer an edit this controller would reject.
		WritesRefused: c.refuseWhenBroken() != nil,
	}
}

// resolvedInstanceFor is the listing view of a curated provider that has no
// instance of its own - no credential yet, or no complete destination. Resolve
// reaches those; the copy carries listing metadata only, never the resolved
// credential value or either headers map. A hidden provider keeps an empty
// BaseURL, the same suppression the sanitized copy makes.
//
// The credential write's endpoint assertion reads this too: a client asserts
// the fingerprint of the entry it was shown, so the value it is checked against
// has to come from the same resolution - otherwise the first key for a
// credential-requiring provider would be refused as a moved endpoint.
func resolvedInstanceFor(r *registry.Registry, id string, hidden bool) (registry.Instance, bool) {
	resolved, err := r.ResolveInstance(id)
	if err != nil {
		return registry.Instance{}, false
	}
	inst := registry.Instance{
		Name: resolved.Instance, ProviderID: resolved.ProviderID,
		Protocol: resolved.Protocol, Surface: resolved.Surface,
		Auth: resolved.Transport.Auth, Implicit: true, Hidden: hidden,
		CredentialSource: resolved.Credential.Source,
		ShadowedEnvVar:   resolved.ShadowedEnvVar, Warnings: resolved.Warnings,
	}
	if !hidden {
		inst.BaseURL = resolved.Transport.BaseURL
	}
	return inst, true
}

// entryFor is the wire view of one instance: the registry's own description,
// plus the credential status the auth controller derives for it, and the
// credential fields from its authored entry — nil for an implicit instance,
// which has no entry in providers.toml and so prefills neither. key is the
// fingerprint key the caller resolved for the listing this entry belongs to
// (List resolves one for all rows; see fingerprintWithKey).
//
// r is the snapshot inst was read from, and it is what the endpoint
// fingerprint is derived from. Asking the holder for the current registry
// instead would let a reload land between the two: the row would then carry a
// displayed URL from one state of providers.toml and a fingerprint from
// another, and a credential write asserting that pair would describe a
// destination that never existed.
func (c *hubInstancesController) entryFor(r *registry.Registry, inst registry.Instance, authored *registry.Provider, key []byte) appwire.InstanceEntry {
	// A bare controller (a construction with no auth controller wired) still
	// describes the registry it holds: there is no credential layer to derive a
	// status from, so the row carries an empty one rather than dereferencing a
	// nil controller.
	var status appwire.AuthStatusResponse
	if c.auth != nil {
		status = c.auth.instanceStatus(inst)
	}
	entry := appwire.InstanceEntry{
		Name:                inst.Name,
		Base:                inst.Base,
		ProviderID:          inst.ProviderID,
		Protocol:            inst.Protocol,
		Surface:             inst.Surface,
		Auth:                inst.Auth,
		BaseURL:             sanitizeEndpointURL(inst.BaseURL),
		EndpointFingerprint: destinationFingerprintKeyed(key, r, inst),
		Vars:                inst.Vars,
		Implicit:            inst.Implicit,
		Hidden:              inst.Hidden,
		IsDefault:           inst.Default,
		AuthModes:           status.AuthModes,
		ActiveSource:        status.ActiveSource,
		HasStoredFile:       status.HasStoredFile,
		HasStoredOAuth:      status.HasStoredOAuth,
		EnvVar:              status.EnvVar,
		ShadowedEnvVar:      status.ShadowedEnvVar,
		StoredEmail:         status.StoredEmail,
		CredentialRequired:  inst.Auth != registry.AuthNone && inst.Auth != registry.AuthOptionalBearer,
		Warnings:            inst.Warnings,
		Models:              instanceModels(r, inst.Name),
	}
	if authored != nil {
		// api_key_env names an environment variable, and the loader takes
		// whatever string the TOML grammar spells, so a key pasted into that
		// field loads. It is omitted rather than sent, exactly as
		// credentialHeaderField omits a header the authoring rule refuses.
		if len(authored.APIKeyEnv) > 0 && registry.CheckAPIKeyEnvName(authored.APIKeyEnv[0]) == nil {
			entry.APIKeyEnv = authored.APIKeyEnv[0]
		}
		entry.CredentialHeader = credentialHeaderField(authored.CredentialHeaders)
	}
	return entry
}

// authStateDir is the state root the controllers share: the OAuth records and,
// beside them, the key the endpoint fingerprints are keyed with. A bare
// controller (a test that wired no auth) has none, and its fingerprints are
// then omitted rather than served unkeyed.
func (c *hubInstancesController) authStateDir() string {
	if c.auth == nil {
		return ""
	}
	return c.auth.stateDir
}

// credentialHeaderField renders the authored credential_headers map as the
// single NAME=VALUE field the forms use; credentialHeaderFrom is its
// inverse. Several headers are possible by hand-editing the file, never
// through the pane; the first in sorted order is the one the form edits, and
// editing that field replaces the whole map.
//
// A name or a value the authoring rule would refuse is omitted rather than
// sent. registry.CheckCredentialHeaderName and CheckCredentialHeaderValue
// guard evener's own authoring surfaces only — the loader's checkEnvRefs
// passes any value without a '$' and reads any name the TOML grammar spells
// — so a hand-written literal secret or a name carrying a CR/LF loads fine,
// and neither must reach a client. Prefilling one would also build a form
// Edit refuses to save.
func credentialHeaderField(headers map[string]string) string {
	if len(headers) == 0 {
		return ""
	}
	names := slices.Sorted(maps.Keys(headers))
	value := headers[names[0]]
	if registry.CheckCredentialHeaderName(names[0]) != nil || registry.CheckCredentialHeaderValue(value) != nil {
		return ""
	}
	return names[0] + "=" + value
}

// sanitizeEndpointURL keeps only the non-secret endpoint identity exposed to
// clients. Runtime requests continue to use the authored BaseURL; this copy is
// only for instance-list UI metadata and must not carry userinfo, query tokens,
// or fragments across the appwire boundary.
func sanitizeEndpointURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return ""
	}
	u.User = nil
	u.RawQuery = ""
	u.ForceQuery = false
	u.Fragment = ""
	u.RawFragment = ""
	return u.String()
}

// fingerprintWithKey digests one destination identity with an already-resolved
// key. The identity includes the parts sanitizeEndpointURL leaves out of the
// displayed copy - query parameters, userinfo and fragment - which a client
// cannot compare itself (they must not cross the appwire boundary, since a query
// string can carry a token); the digest is what lets it notice that the
// destination changed under an open form. It is over the identity's exact bytes,
// so two that differ only in a query parameter fingerprint differently.
//
// The digest is keyed with the hub's own secret, not a bare hash: the stripped
// parts can be low-entropy (a password in userinfo, a short query token), and
// an unkeyed digest of a guessable secret is a guessable function of it - a
// client holding the listing could recover the secret by brute force, which is
// exactly what the sanitized copy exists to prevent. An empty key (this hub
// could not resolve one) or an empty identity has no digest: the caller omits
// the fingerprint rather than serving an unkeyed one. List resolves one key for
// its whole listing and passes it here, so every row of that listing is keyed
// the same way even if a repair lands beside it.
func fingerprintWithKey(key []byte, identity string) string {
	if len(key) == 0 || strings.TrimSpace(identity) == "" {
		return ""
	}
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte(identity))
	return hex.EncodeToString(mac.Sum(nil))
}

// destinationIdentity is what destinationFingerprint digests: the base URL an
// instance resolves, the protocol that selects its request templates, and every
// request path those templates contribute. A credential-bearing request is
// built from exactly these, so a change to any of them moves where the secret
// is sent - and a change to the protocol or a path template can leave the
// sanitized URL the listing displays byte-identical, which is why the digest
// cannot be of that URL alone. Nothing secret-bearing is here: not the
// credential, not either header map, not vars.
func destinationIdentity(resolved registry.Resolved) string {
	t := resolved.Transport
	return strings.Join([]string{
		strings.TrimSpace(t.BaseURL),
		resolved.Protocol,
		strings.TrimSpace(t.Endpoint),
		strings.TrimSpace(t.StreamEndpoint),
		strings.TrimSpace(t.ModelsEndpoint),
		strings.TrimSpace(t.CountTokensEndpoint),
	}, "\x00")
}

// destinationInstance reports the instance behind inst when it has a
// destination this hub can name at all: not hidden, resolvable, and carrying a
// base URL. Whether the hub can *key* that destination's fingerprint is a
// separate question - the key file can be unreadable while the destination is
// perfectly real - and the credential guards must not read one as the other
// (endpointFingerprintFor's empty answer conflates them; app_auth.go's
// endpointHasDestination asks this question instead).
func destinationInstance(r *registry.Registry, inst registry.Instance) (registry.Resolved, bool) {
	if inst.Hidden || r == nil {
		return registry.Resolved{}, false
	}
	resolved, err := r.ResolveInstance(inst.Name)
	if err != nil || strings.TrimSpace(resolved.Transport.BaseURL) == "" {
		return registry.Resolved{}, false
	}
	return resolved, true
}

// destinationFingerprint is the value a listing row serves and a credential
// write is checked against: the digest of where name resolves now. Empty when
// it resolves no destination here - a hidden provider keeps its destination out
// of the listing, and an unresolvable one has nothing to describe - or when the
// hub has no key to digest with.
func destinationFingerprint(stateDir string, r *registry.Registry, inst registry.Instance) string {
	return destinationFingerprintKeyed(endpointFingerprintKey(stateDir), r, inst)
}

// destinationFingerprintKeyed is destinationFingerprint with the key already
// resolved (see fingerprintWithKey): List resolves one key for all its rows,
// and every other caller resolves one per call.
func destinationFingerprintKeyed(key []byte, r *registry.Registry, inst registry.Instance) string {
	resolved, ok := destinationInstance(r, inst)
	if !ok {
		return ""
	}
	return fingerprintWithKey(key, destinationIdentity(resolved))
}

// endpointFingerprintKeyFile is the key's name under the auth state root, the
// same root the OAuth records live in.
const endpointFingerprintKeyFile = "endpoint-fingerprint.key"

var (
	endpointFingerprintKeyMu sync.Mutex
	// endpointFingerprintKeyOwner is the uid a key file has to belong to. A
	// real uid cannot be varied the way the check needs to be exercised, so it
	// is a seam in the same sense as the auth store's file operations.
	endpointFingerprintKeyOwner = os.Getuid
	// endpointFingerprintKeyMode reports the permission bits a key file carries,
	// judged, where the platform records them as POSIX modes - unix does
	// (fileModePerm, fileowner_unix.go). Windows synthesizes 0666 (0444 when
	// read-only) for every file, so it answers unjudged and the check is skipped
	// there: judging the synthesized bits would refuse every key the hub writes,
	// and the repair would then rotate the key on every read - every fingerprint
	// a client was shown would stop matching. Another seam, for the same reason
	// as endpointFingerprintKeyOwner: the unjudged path is what a non-unix host
	// runs, and no mode a test writes on this host makes it answer that way.
	endpointFingerprintKeyMode = fileModePerm
	// endpointFingerprintKeyLink publishes the finished key: os.Link refuses the
	// publish while the path is taken, which is what keeps a second hub from
	// replacing the first hub's key. Filesystems without hard links (FAT/exFAT,
	// some FUSE/SMB mounts) cannot express that, and a key that cannot be
	// published is a hub that refuses every credential write - so the publish
	// falls back to an atomic rename there (see
	// publishFreshEndpointFingerprintKey). A seam, like the two above, so a test
	// can stand in for such a filesystem on one that supports links.
	endpointFingerprintKeyLink = os.Link
	// resolveEndpointFingerprintKey is the seam List resolves the listing's key
	// through: production reads (and, when the file is unusable, repairs) the key
	// here, and a test can count the resolutions and observe that they happen
	// before the credential lock is taken.
	resolveEndpointFingerprintKey = endpointFingerprintKeyState
)

// endpointFingerprintKey returns the key the endpoint fingerprints are keyed
// with, creating it under stateDir on first use. It is machine-local, 0600, and
// never sent anywhere: only a holder of the key can recompute a digest. A key
// that can neither be read nor created yields nil, and the caller then omits
// the fingerprint.
func endpointFingerprintKey(stateDir string) []byte {
	key, _ := endpointFingerprintKeyState(stateDir)
	return key
}

// endpointFingerprintKeyState is endpointFingerprintKey plus the reason no key
// is available, for the callers that have to say so: a credential write whose
// client asserted nothing is refused while a state root exists but cannot be
// keyed (hubAuthController.verifyEndpointFingerprint), and the listing carries
// the reason as a diagnostic (endpointFingerprintKeyDiagnostic).
//
// An empty stateDir is a bare controller with no state root at all: there is
// nothing to key with, so there is nothing to refuse on the write side and
// nothing to report - that case is (nil, nil), not an error.
//
// The key file is read fresh on every use - nothing is held between calls - so
// rotation is the answer to a key that may have leaked (readEndpointFingerprintKey
// refuses a file the hub did not write safely), and an operator rotating or
// deleting the file sees that take effect in a running hub immediately, not only
// after a restart. The lock is held across the read and the repair so concurrent
// hubs serialize on creating the one file they share.
func endpointFingerprintKeyState(stateDir string) ([]byte, error) {
	stateDir = strings.TrimSpace(stateDir)
	if stateDir == "" {
		return nil, nil
	}
	path := filepath.Join(stateDir, endpointFingerprintKeyFile)
	endpointFingerprintKeyMu.Lock()
	defer endpointFingerprintKeyMu.Unlock()
	key, err := readEndpointFingerprintKey(path)
	if err != nil {
		key, err = repairEndpointFingerprintKey(path)
		if err != nil {
			return nil, err
		}
	}
	if len(key) == 0 {
		return nil, fmt.Errorf("%s is not a usable endpoint fingerprint key", path)
	}
	return key, nil
}

// fingerprintKeyDiagnostic is what the listing says when a state root exists but
// cannot yield its key: every fingerprint is omitted (see endpointFingerprint)
// and every credential write that asserts nothing is refused (see
// hubAuthController.verifyEndpointFingerprint), so the pane has to say why
// rather than show a silently unkeyed hub. keyErr is the listing's own
// resolution error - it names the file and what is wrong with it, never key
// material - and a bare controller with no state root has nothing to report.
func fingerprintKeyDiagnostic(keyErr error) string {
	if keyErr == nil {
		return ""
	}
	return fmt.Sprintf("%s: %v (endpoint fingerprints are unavailable until it can be read or written)", endpointFingerprintKeyFile, keyErr)
}

// endpointFingerprintKeyModeAccepted reports whether a key file's mode keeps the
// key to its owner. The hub writes 0600, but a stricter mode is not corruption:
// an operator who tightens the file to 0400 (or narrows it to 0700) has made it
// no less secret, and refusing it would repair the file - rotating the key and
// invalidating every endpoint fingerprint clients already hold.
func endpointFingerprintKeyModeAccepted(perm os.FileMode) bool {
	return perm&0o077 == 0 && perm&0o400 != 0
}

// readEndpointFingerprintKey returns the key at path, refusing a file this hub
// must not treat as its own secret. The fingerprints' whole guarantee is that
// only a holder of the key can recompute them (see endpointFingerprint), so a
// key another user can read is one they can key digests with, and a file that
// is not this hub's own regular file is not a key it wrote. Every judgement and
// the read itself go through one descriptor: the open refuses a symlink at the
// path, and what is checked is exactly what is read, so nothing swapped in
// after the open can slip a different file past the checks. The caller replaces
// what is refused with a fresh 0600 key, which is the answer a key that may have
// leaked calls for: rotation is what stops it describing anything.
func readEndpointFingerprintKey(path string) ([]byte, error) {
	// The descriptor is the subject: what is at the path is judged and read,
	// not what it points at. A symlink here is replaced by the rotation below
	// rather than followed.
	f, err := openEndpointFingerprintKey(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file", path)
	}
	if perm, judged := endpointFingerprintKeyMode(info); judged && !endpointFingerprintKeyModeAccepted(perm) {
		return nil, fmt.Errorf("%s is %04o, want an owner-only mode with owner read set (0600 or stricter)", path, perm)
	}
	if uid, known := fileOwnerUID(info); known && uid != endpointFingerprintKeyOwner() {
		return nil, fmt.Errorf("%s is owned by uid %d, not by uid %d", path, uid, endpointFingerprintKeyOwner())
	}
	raw, err := io.ReadAll(f)
	if err != nil {
		return nil, err
	}
	key := []byte(strings.TrimSpace(string(raw)))
	if len(key) == 0 {
		// A file that is there but empty is a corrupt one (an operator's
		// placeholder, a truncated write). Reporting it as "no key" would fail
		// the endpoint-change protection open without a word, so the caller
		// replaces it instead.
		return nil, fmt.Errorf("%s is empty", path)
	}
	return key, nil
}

// repairEndpointFingerprintKey puts a usable key at path: it creates one where
// there is none, and replaces one that cannot be read. Repairs are serialized
// across the processes sharing the state root - the lock beside path is held
// across the judgement and the publish, and the re-read that precedes a
// replacement happens under it - so a usable key another process published is
// adopted rather than replaced: "a usable key is never replaced" is a property
// of the file, not only of this process's mutex. The state root is the
// registry's (reg.StateRoot(), app_rpc.go's hubAuthStateRoot), which
// hub_state_root does not move, so two hub processes with different
// hub_state_root - and so different hub.lock files - can still repair this one
// key file together. The fresh key is written
// to a temp file beside path and published atomically: os.Link creates it only
// while the path is still absent, so a key another hub wrote first is used
// as-is - two hubs must not each key their own digests - and a path that is
// present but unusable is replaced with os.Rename, so the path never stops
// holding a key. A filesystem that cannot hard-link at all uses that same
// atomic replace, because a key that cannot be published would refuse every
// credential write. The one thing removed is an empty directory, which a rename
// cannot replace and which can never be a key: a state root a stray `mkdir`
// planted in stays recoverable (a non-empty one is left as the obstacle it
// is). A usable key is never replaced.
func repairEndpointFingerprintKey(path string) ([]byte, error) {
	// The lock file is a sibling of the key, so a state root that does not
	// exist yet has to be created before the lock can be taken; otherwise the
	// lock open fails ENOENT ahead of publishFreshEndpointFingerprintKey's own
	// MkdirAll.
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	release, err := lockEndpointFingerprintKey(path)
	if err != nil {
		return nil, err
	}
	defer release()
	var lastErr error
	for range 2 {
		key, err := publishFreshEndpointFingerprintKey(path)
		if err == nil {
			return key, nil
		}
		lastErr = err
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("%s is not a usable endpoint fingerprint key", path)
	}
	return nil, lastErr
}

// publishFreshEndpointFingerprintKey writes one fresh key beside path and
// publishes it, returning the key at path afterwards: its own, or the usable
// one another hub published in the meantime. The temp file is removed on every
// path; after os.Link or os.Rename the published name is a second link to the
// same inode, not the file callers read.
func publishFreshEndpointFingerprintKey(path string) ([]byte, error) {
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return nil, err
	}
	key := []byte(base64.RawURLEncoding.EncodeToString(raw[:]))
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	var suffix [8]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		return nil, err
	}
	tmp := filepath.Join(dir, fmt.Sprintf(".%s.%s.tmp", filepath.Base(path), hex.EncodeToString(suffix[:])))
	defer func() { _ = os.Remove(tmp) }()
	// The temp is created through the same O_NOFOLLOW-aware helper the read path
	// uses: a link planted at a key path must not be able to redirect a write of
	// the hub's own secret.
	f, err := createEndpointFingerprintKey(tmp)
	if err != nil {
		return nil, err
	}
	if _, err := f.Write(key); err != nil {
		_ = f.Close()
		return nil, err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return nil, err
	}
	if err := f.Close(); err != nil {
		return nil, err
	}
	if err := endpointFingerprintKeyLink(tmp, path); err == nil {
		return key, nil
	}
	// The link did not publish the key: either the path is taken (another hub won
	// the race, or something unusable is in the way) or the filesystem cannot
	// hard-link at all (FAT/exFAT, some FUSE/SMB mounts), where no primitive keeps
	// the no-clobber property os.Link gives while publishing a finished file in
	// one step. A key that cannot be published is a hub that refuses every
	// credential write - verifyEndpointFingerprint refuses both an empty and a
	// non-empty assertion while the key state errors - so both cases land on the
	// same atomic replace below. The read is what keeps "a usable key is never
	// replaced" there, in every non-racing case; publication stays
	// complete-or-nothing for readers, which the failed-write test pins.
	if existing, readErr := readEndpointFingerprintKey(path); readErr == nil {
		return existing, nil
	}
	// An empty directory is not a key and cannot be renamed over, so it is the
	// one obstacle this removes; a non-empty one cannot be either and stays the
	// obstacle the diagnostics name.
	if info, statErr := os.Lstat(path); statErr == nil && info.IsDir() {
		if removeErr := os.Remove(path); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
			return nil, removeErr
		}
	}
	if err := os.Rename(tmp, path); err != nil {
		return nil, err
	}
	return key, nil
}

// writeLoadable is the invariant every mutation holds: a providers.toml the
// hub writes must be one the registry can read back. registry.WriteConfigFile
// re-parses what it marshals, so every rule the parser enforces — the
// protocol and surface vocabularies, the $VAR syntax in credential headers
// and api_key, unknown keys — refuses the write instead of landing on disk.
// Without that the write succeeds, the reload that follows fails,
// refuseWhenBroken flips, and the corrective edit is refused too: the pane
// locked out of its own recovery.
//
// Only that refusal is about the fields the caller sent, so only it comes
// back as invalid params; the parser never echoes a value it rejects, so its
// error is safe to return. A filesystem failure is the hub's problem, not the
// caller's, and is returned as it came.
func (c *hubInstancesController) writeLoadable(l *registry.Layer) error {
	err := c.write(l)
	if errors.Is(err, registry.ErrConfigUnloadable) {
		return appwire.InvalidParams(err.Error())
	}
	return err
}

// varNameRe is the placeholder grammar a transport template can name
// (llm/registry's placeholderRe: "{VAR}", uppercase only). A vars key in any
// other shape is one no substitution will ever reach, and the config writer's
// dry parse checks the $ENV syntax in the values, not the shape of the keys —
// so without this the entry lands in providers.toml and is silently ignored.
var varNameRe = regexp.MustCompile(`^[A-Z][A-Z0-9_]*$`)

// validVarName refuses a key the placeholder grammar cannot name.
func validVarName(name string) error {
	if varNameRe.MatchString(name) {
		return nil
	}
	return fmt.Errorf("invalid variable name %q: a transport placeholder is {UPPERCASE_NAME}, so nothing would substitute it", name)
}

// validVarNames holds every key of a vars map to the grammar. Create writes
// each of them, so each has to be one a substitution could reach.
func validVarNames(vars map[string]string) error {
	for name := range vars {
		if err := validVarName(name); err != nil {
			return err
		}
	}
	return nil
}

// trimmedVars is the vars map as it is stored: every value trimmed, the way
// Edit trims each value it sets and the way base_url and api_key_env are
// trimmed beside it, so the two authoring paths write the same value.
func trimmedVars(vars map[string]string) map[string]string {
	if vars == nil {
		return nil
	}
	trimmed := make(map[string]string, len(vars))
	for name, value := range vars {
		trimmed[name] = strings.TrimSpace(value)
	}
	return trimmed
}

// validVarSets holds only the entries that SET a value. An edit spells a
// delete as an empty value (appwire.InstanceEditParams) and a delete writes
// nothing, so the key it names need not be one a substitution could reach —
// and a hand-authored key the grammar refuses is exactly the one the sheet
// has to be able to remove.
func validVarSets(vars map[string]string) error {
	for name, value := range vars {
		if strings.TrimSpace(value) == "" {
			continue
		}
		if err := validVarName(name); err != nil {
			return err
		}
	}
	return nil
}

// credentialHeaderFrom reads the form's single NAME=VALUE credential header.
// The name must be an HTTP header token (registry.CheckCredentialHeaderName).
// The value must reference a $VARIABLE and carry no literal secret beside it:
// registry.CheckCredentialHeaderValue, shared with `evener providers add`, so
// neither authoring surface writes a key the other would refuse (spec §11.2)
// and this surface never saves a value the entry it broadcasts would have to
// omit. The refusal names the header, never its value.
func credentialHeaderFrom(field string) (map[string]string, error) {
	field = strings.TrimSpace(field)
	if field == "" {
		return nil, nil
	}
	name, value, ok := strings.Cut(field, "=")
	name, value = strings.TrimSpace(name), strings.TrimSpace(value)
	if !ok || name == "" {
		return nil, appwire.InvalidParams("credential header must be NAME=VALUE, as in Authorization=Bearer $PORTKEY_KEY")
	}
	if err := registry.CheckCredentialHeaderName(name); err != nil {
		return nil, appwire.InvalidParams(err.Error())
	}
	if err := registry.CheckCredentialHeaderValue(value); err != nil {
		return nil, appwire.InvalidParams(fmt.Sprintf("credential header %s: %v", name, err))
	}
	return map[string]string{name: value}, nil
}

// requireAuth refuses an instance change when this controller has no auth
// controller to check or move credentials with (a bare construction, which the
// tests use). List still describes the registry it holds; a change that reads or
// writes credentials cannot, and must refuse rather than dereference a nil
// controller.
func (c *hubInstancesController) requireAuth() error {
	if c.auth == nil {
		return appwire.InternalError("this hub has no credential controller: instance changes are unavailable")
	}
	return nil
}

// refuseWhenBroken stops every write while there is no registry to write
// against: a providers.toml that does not load (the hub has no way to rewrite
// a file it could not read without destroying what the user wrote — spec §10,
// §14.1), or a holder that has not loaded one yet. Every mutator asks this
// first, so none of them has to guard the reads that follow.
func (c *hubInstancesController) refuseWhenBroken() error {
	if c.reg.WritesRefused() {
		return fmt.Errorf("providers.toml cannot be edited until it loads: %w", c.reg.LoadError())
	}
	if c.reg.Get() == nil {
		return errors.New("providers.toml cannot be edited: the provider registry has not loaded")
	}
	return nil
}

// Create authors a new instance entry. APIKeyEnv is a variable name and
// CredentialHeader must reference a $VAR: a literal secret never crosses this
// boundary, and none is ever written to the file (spec §11.2).
//
// Every refusal that blames the fields the caller sent comes back as a wire
// error naming its class — InvalidParams for a field that is malformed or
// names something that does not exist, Conflict for a name already taken —
// matching how hubDirsCreate and the pin-section store classify the same
// shapes. A refusal about the hub's own state (the registry not loaded, a
// read or write failure) stays a plain error: that is not the caller's to
// fix.
func (c *hubInstancesController) Create(params appwire.InstanceCreateParams) error {
	if err := c.refuseWhenBroken(); err != nil {
		return err
	}
	if err := c.requireAuth(); err != nil {
		return err
	}
	name := strings.TrimSpace(params.Name)
	if !registry.ValidInstanceName(name) {
		return appwire.InvalidParams(fmt.Sprintf("invalid instance name %q (lowercase, no slash)", params.Name))
	}
	base := strings.TrimSpace(params.Base)
	if _, ok := c.reg.Get().Provider(base); !ok {
		return appwire.InvalidParams(fmt.Sprintf("unknown base provider %q", params.Base))
	}
	credentialHeaders, err := credentialHeaderFrom(params.CredentialHeader)
	if err != nil {
		return err
	}
	apiKeyEnv := strings.TrimSpace(params.APIKeyEnv)
	if apiKeyEnv != "" {
		if err := registry.CheckAPIKeyEnvName(apiKeyEnv); err != nil {
			return appwire.InvalidParams(err.Error())
		}
	}
	if err := validVarNames(params.Vars); err != nil {
		return appwire.InvalidParams(err.Error())
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	// The discipline every providers.toml mutation here follows
	// (hubAuthController.credMu): held across the read, the write and the
	// reload. A credential write's endpoint assertion is checked under this
	// lock, so only an instance set that cannot move out from under it
	// describes what the stored secret lands on - and authoring an entry can
	// move it, since a name that was a curated provider until now resolves to
	// the entry's endpoint afterwards.
	c.auth.credMu.Lock()
	defer c.auth.credMu.Unlock()
	// before is an independent parse from l below — a fresh read sharing no
	// maps with it — so a create whose config parses but cannot load (#711) can
	// be written back exactly as the file was, the way Edit restores its own.
	before, _, err := c.read()
	if err != nil {
		return err
	}
	l, _, err := c.read()
	if err != nil {
		return err
	}
	if _, exists := l.Providers[name]; exists {
		return appwire.Conflict(fmt.Sprintf("instance %q already exists", name))
	}
	p := registry.Provider{
		ID:       name,
		Base:     base,
		Protocol: strings.TrimSpace(params.Protocol),
		Surface:  strings.TrimSpace(params.Surface),
		Transport: registry.Transport{
			BaseURL: strings.TrimSpace(params.BaseURL),
			Vars:    trimmedVars(params.Vars),
		},
	}
	if apiKeyEnv != "" {
		p.APIKeyEnv = []string{apiKeyEnv}
	}
	p.CredentialHeaders = credentialHeaders
	l.Providers[name] = p
	if err := c.writeLoadable(l); err != nil {
		return err
	}
	if err := c.reg.Reload(); err != nil {
		// writeLoadable's dry parse only checks the layer against the registry
		// schema; Reload resolves it, so a config that parses can still fail to
		// load - a protocol or transport the base does not offer, for instance.
		// Leaving the entry in place would refuse every instance write until the
		// file is fixed by hand, with the pane locked out of its own recovery,
		// so the file this call just overwrote is restored instead and the
		// refusal names what could not load.
		if restoreErr := c.write(before); restoreErr != nil {
			return fmt.Errorf("%w (and restoring the previous config failed: %w)", err, restoreErr)
		}
		_ = c.reg.Reload() // best-effort: put the last-good registry view back
		return appwire.InvalidParams(fmt.Sprintf("instance %q cannot be loaded: %v", name, err))
	}
	return nil
}

// Edit applies the fields the form set, leaving every other authored key
// alone. Editing an instance that exists only from the environment authors a
// shadowing entry carrying those fields alone — never a base_url the form
// merely displayed, which would stop the instance inheriting its provider's
// key (spec §10, §11.3).
//
// A NewName re-keys the entry, follows the default pointer, and then moves
// the stored key and OAuth record (moveCredentials); it is refused for an
// invalid name, a name any instance already has, and a name still holding a
// credential of its own (credentialsUnder).
//
// An instance with no authored entry renames like any other: the rename
// authors the entry under the new name, pinning the base it was resolving
// against. For an instance a UI credential created (a signed-in Codex record,
// a stored key) that moves the whole instance, because the credential is a
// file under the old name. For one the environment supplies it does not:
// nothing in the rename can move a shell variable or the ADC file, so the old
// row stays alongside the new one.
//
// Refusals follow Create's convention (#717/#748): the ones that blame the
// fields the caller sent — an unknown name, an invalid vars key, an edit
// that would leave the instance unable to load — come back as
// appwire.InvalidParams; the hub's own faults (the registry not loaded, a
// read, write, or restore failure) stay plain errors.
func (c *hubInstancesController) Edit(params appwire.InstanceEditParams) error {
	if err := c.refuseWhenBroken(); err != nil {
		return err
	}
	if err := c.requireAuth(); err != nil {
		return err
	}
	name := strings.TrimSpace(params.Name)
	if err := validVarSets(params.Vars); err != nil {
		return appwire.InvalidParams(err.Error())
	}
	// Not parsed at all under the clear flag: the clear branch below wins, as
	// it does for base URL, protocol, surface and api_key_env, so the value
	// riding along with it is one this request discards. Parsing it anyway
	// turns a removal into a refusal — and the value a user reaches for the
	// clear over is often the invalid one the parse would refuse.
	var credentialHeaders map[string]string
	if !params.ClearCredentialHeader {
		parsed, err := credentialHeaderFrom(params.CredentialHeader)
		if err != nil {
			return err
		}
		credentialHeaders = parsed
	}
	// api_key_env names an environment variable, never the key itself, and is
	// checked here for the same reason and under the same clear-flag rule as
	// the credential header above.
	apiKeyEnv := strings.TrimSpace(params.APIKeyEnv)
	if !params.ClearAPIKeyEnv && apiKeyEnv != "" {
		if err := registry.CheckAPIKeyEnvName(apiKeyEnv); err != nil {
			return appwire.InvalidParams(err.Error())
		}
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	// Held for the rest of the call, so the providers.toml write and the
	// reload that follows it sit inside the same held lock
	// (hubAuthController.credMu). An edit that moves base_url is one step with
	// every credential write: the write's endpoint assertion is checked under
	// this lock and only describes the instance it lands on if no edit can
	// land between that check and the store.
	c.auth.credMu.Lock()
	defer c.auth.credMu.Unlock()
	// before is an independent parse from l below — a fresh read sharing no
	// maps with it — so if the edit parses fine but fails to load (#711),
	// writing it back restores exactly what was on disk before this call.
	before, _, err := c.read()
	if err != nil {
		return err
	}
	l, _, err := c.read()
	if err != nil {
		return err
	}
	p, authored := l.Providers[name]
	if !authored {
		if _, ok := c.reg.Get().Instance(name); !ok {
			return appwire.InvalidParams(fmt.Sprintf("instance %q not found", name))
		}
		p = registry.Provider{ID: name}
	}
	newName := strings.TrimSpace(params.NewName)
	renaming := newName != "" && newName != name
	if renaming {
		if !registry.ValidInstanceName(newName) {
			return appwire.InvalidParams(fmt.Sprintf("invalid instance name %q (lowercase, no slash)", params.NewName))
		}
		if _, taken := l.Providers[newName]; taken {
			return appwire.Conflict(fmt.Sprintf("instance %q already exists", newName))
		}
		if _, taken := c.reg.Get().Instance(newName); taken {
			return appwire.Conflict(fmt.Sprintf("instance %q already exists", newName))
		}
		// The destination check below and the move at the end of this call are
		// one step too: a credential written between them is one the check
		// never saw and the move would overwrite, and the lock that orders
		// this call against every credential write is already held.
		// Both checks above ask which instances exist, and a credential can
		// outlive the instance it belonged to: providers.toml hand-edited
		// while credentials.toml or the OAuth state kept its entry. Under a
		// name the registry does not curate that leftover resolves no
		// instance, so it is invisible to them, and moveCredentials would
		// overwrite it. The refusal belongs here rather than there: by the
		// time moveCredentials runs the file is re-keyed and the registry
		// reloaded, so there is no longer anything to refuse.
		if held := c.credentialsUnder(newName); len(held) > 0 {
			return appwire.Conflict(fmt.Sprintf("renaming %q to %q would overwrite %s; clear that first", name, newName, strings.Join(held, " and ")))
		}
		// moveCredentials rewrites the record's provider field to the new name,
		// which means it has to read the record after providers.toml is already
		// re-keyed. An unreadable record cannot be carried that way: the config
		// would name the new instance while the credential stayed under the old
		// one, leaving the rename persisted but the renamed instance without a
		// usable credential - and the caller told about it only after the write.
		// Read the record here, under the same lock and before anything is
		// written, so an unreadable one refuses the rename with nothing changed.
		// Not-found is the one read failure that is not a problem: an instance
		// with no record has nothing to carry.
		if _, err := c.auth.loadAuth(c.auth.stateDir, name); err != nil && !errors.Is(err, authopenai.ErrAuthNotFound) {
			return fmt.Errorf("renaming %q to %q needs its OAuth record to follow it to the new name, so the record must be readable, but it could not be read: %w", name, newName, err)
		}
	}
	if params.ClearBaseURL {
		// Drops the authored override and goes back to the registry
		// default, restoring spec §10's credential inheritance from the
		// base provider (#711). Additive over BaseURL's existing "empty
		// means unchanged" (v3): the two are never both meaningful in the
		// same request (appwire.InstanceEditParams doc comment).
		p.Transport.BaseURL = ""
	} else if v := strings.TrimSpace(params.BaseURL); v != "" {
		p.Transport.BaseURL = v
	}
	if params.ClearProtocol {
		p.Protocol = ""
	} else if v := strings.TrimSpace(params.Protocol); v != "" {
		p.Protocol = v
	}
	if params.ClearSurface {
		p.Surface = ""
	} else if v := strings.TrimSpace(params.Surface); v != "" {
		p.Surface = v
	}
	if params.ClearAPIKeyEnv {
		p.APIKeyEnv = nil
	} else if apiKeyEnv != "" {
		p.APIKeyEnv = []string{apiKeyEnv}
	}
	if params.ClearCredentialHeader {
		p.CredentialHeaders = nil
	} else if credentialHeaders != nil {
		p.CredentialHeaders = credentialHeaders
	}
	// An empty value deletes the variable (appwire.InstanceEditParams);
	// anything else is set over whatever was authored before, trimmed as
	// base_url and api_key_env are — the delete is decided on the trimmed
	// value, so storing the untrimmed one would let a value that only just
	// escaped the delete land as one padded with spaces.
	for key, value := range params.Vars {
		value = strings.TrimSpace(value)
		if value == "" {
			delete(p.Transport.Vars, key)
			continue
		}
		if p.Transport.Vars == nil {
			p.Transport.Vars = map[string]string{}
		}
		p.Transport.Vars[key] = value
	}
	if renaming {
		// An entry with no base inherits protocol, surface and models from the
		// curated provider its own name matches (spec §4.2). The new name
		// matches nothing, so the inheritance would vanish with the old name
		// and the reload below would refuse the rename. Writing down the base
		// the entry was already resolving against keeps the effective
		// configuration identical; it is not a re-base onto a different
		// provider, which spec §7 puts out of scope.
		if p.Base == "" {
			if _, curated := c.reg.Get().Provider(name); curated {
				p.Base = name
			}
		}
		// The same rule pointed at the new name: an entry the rule above left
		// without a base has none to pin its configuration, so under a curated
		// id it would start inheriting that provider's protocol, transport,
		// models and credential resolution instead of resolving its own
		// fields. Neither taken-name check refuses it — a curated provider
		// with no credential is not an instance — and nothing has moved yet,
		// so the refusal costs nothing to make here.
		if p.Base == "" {
			if _, curated := c.reg.Get().Provider(newName); curated {
				return appwire.InvalidParams(fmt.Sprintf("%q is a curated provider id; an instance named after it would inherit its configuration. Give the instance an explicit base or choose another name.", newName))
			}
		}
		// The map key is the instance name providers.toml is written under;
		// the default pointer follows so the file still loads.
		delete(l.Providers, name)
		p.ID = newName
		if l.Default == name {
			l.Default = newName
		}
		l.Providers[newName] = p
	} else {
		l.Providers[name] = p
	}
	// writeAndReload restores before when the reload fails (see #711
	// on its comment); a rename continues below on success.
	if err := c.writeAndReload(before, l, name, "edit"); err != nil {
		return err
	}
	if renaming {
		moveErr := c.moveCredentials(name, newName)
		// The reload above ran while the stored key and OAuth record still
		// sat under the old name, so a curated provider this instance had
		// shadowed could resolve a credential and reappear as a phantom
		// implicit instance — one that also makes renaming back a sticky
		// Conflict. Remove clears credentials before its reload; a rename
		// cannot, because a failed reload restores the file and the
		// credentials would already have moved.
		if err := c.reg.Reload(); err != nil && moveErr == nil {
			// Everything this rename writes is already written, so it is as
			// persisted as one that ended cleanly and is announced the same
			// way.
			return renamePersistedError{err}
		}
		return moveErr
	}
	return nil
}

// credentialsUnder names the credentials already filed under name, in the
// vocabulary describeImplicit uses for the same two sources. It is what a
// rename onto name would overwrite, so the caller can go clear the one it
// names. A record that exists but does not read back counts as present:
// not-found is the only signal that nothing is there, and overwriting a
// credential the hub merely failed to read is the same loss.
func (c *hubInstancesController) credentialsUnder(name string) []string {
	var held []string
	if _, ok := c.auth.creds.Get(name); ok {
		held = append(held, fmt.Sprintf("a credentials.toml entry for %q", name))
	}
	if _, err := c.auth.loadAuth(c.auth.stateDir, name); !errors.Is(err, authopenai.ErrAuthNotFound) {
		held = append(held, fmt.Sprintf("an OAuth record for %q", name))
	}
	return held
}

// renamePersistedError is a rename that reached the file: providers.toml
// carries the new name, and what is unfinished is either the credential move
// or the reload that would refresh the hub's own view of what the move
// changed. Every other client's instance list is stale by exactly as much as
// it would be after a clean rename, so the RPC handler broadcasts on it and
// still returns it, leaving the client that asked with the leftover to deal
// with.
type renamePersistedError struct{ err error }

func (e renamePersistedError) Error() string { return e.err.Error() }

func (e renamePersistedError) Unwrap() error { return e.err }

// removePersistedError is a removal the config carried out - the authored entry
// is out of providers.toml - even though the removal could not finish. What is
// unfinished is a copy the sweep could not delete (this removal's, or one an
// earlier removal stranded), or a rollback that could not be written after the
// reload failed, which leaves the credentials this call deleted restored under
// a name the file no longer configures. Either way the entry is gone from the
// file, so every other client's list is stale by exactly as much as it would be
// after a clean removal: the RPC handler broadcasts on it and still returns it,
// leaving the client that asked with what is unfinished to deal with - the same
// shape as renamePersistedError, for the same reason.
type removePersistedError struct{ err error }

func (e removePersistedError) Error() string { return e.err.Error() }

func (e removePersistedError) Unwrap() error { return e.err }

// moveCredentials carries an instance's stored key, OAuth record and any
// in-flight OAuth copies to its new name after a rename. It runs once
// providers.toml is written and reloaded, with credMu held by the caller: the
// config is already renamed, so a failure here is reported as what was left
// behind rather than undone — the list stays consistent with the file, and a
// leftover stays reachable under the old name through evener/auth/apiKey/clear
// or the state directory. That report is a renamePersistedError, which is what
// tells the RPC handler the rename is on disk however this call ends.
// Nothing it calls takes credMu, which the caller still holds.
func (c *hubInstancesController) moveCredentials(oldName, newName string) error {
	var problems []string
	// One persist, so the key is never briefly filed under both names or
	// neither: a copy-then-clear pair whose second half failed would leave
	// the old name resolving a credential the config no longer names.
	if err := c.auth.creds.Move(oldName, newName); err != nil {
		problems = append(problems, fmt.Sprintf("stored key not copied: %v", err))
	}
	// A failed removal can leave the instance's only record as an in-flight copy
	// under the old name (restoreFailedRemoval's rename-back failed). providers.toml
	// now names only the new instance, so startup recovery would never look under
	// the old name again and that credential would be stranded unrecoverably.
	// Promote the newest copy to the record path when it is free - the read below
	// then carries it and the renamed instance has a usable credential at once -
	// and carry every other copy to the new name's aside name, marker and stamp
	// preserved, so no in-flight copy is left filed under the old name while
	// startup recovery and the sweep still parse what remains.
	promoted, carryProblems := c.carryOAuthAsidesForRename(oldName, newName)
	problems = append(problems, carryProblems...)
	record, err := c.auth.loadAuth(c.auth.stateDir, oldName)
	switch {
	case errors.Is(err, authopenai.ErrAuthNotFound):
	case err != nil:
		// The read refused the record. When this call promoted a copy to the
		// record path, its bytes are now at the canonical OLD-name path, which no
		// recovery recognizes once providers.toml names only the new instance - a
		// shape nothing restores, so the renamed instance would lose its OAuth
		// credential. Move those bytes back under a recovery-recognized aside for
		// the NEW name (kind and stamp preserved), the way every other carry
		// problem is reported: the rename stands and the caller is told what was
		// left reachable.
		if promoted != "" {
			// File the promoted bytes under a recovery-recognized aside for
			// the NEW name, kind and stamp preserved. The destination is chosen
			// the same collision-safe way the carry above chooses one
			// (freeAsideName steps past a taken stamp) and moved without
			// replacing anything (renameNoReplace): a file already at the
			// deterministic name is another credential's bytes and must survive.
			// On a refusal the bytes stay where they are and the problem is
			// reported the way the other carry problems are.
			promotedAside, _ := parseOAuthAside(promoted)
			var want int64
			if s, perr := strconv.ParseInt(promotedAside.stampText, 10, 64); perr == nil {
				want = s
			}
			recordPath := authopenai.AuthFilePath(c.auth.stateDir, oldName)
			dst, ok := freeAsideName(filepath.Dir(recordPath), newName, promotedAside.configBacked, want)
			switch {
			case !ok:
				problems = append(problems, fmt.Sprintf("OAuth record not read (%v), and no fresh recovery-recognized aside name under %q was free to file the copy %q it carried, so those bytes are still at %q", err, newName, promoted, recordPath))
			default:
				if rerr := renameNoReplace(recordPath, dst); rerr != nil {
					problems = append(problems, fmt.Sprintf("OAuth record not read (%v), and the copy %q it carried could not be filed as the recovery-recognized aside %q (%v)", err, promoted, filepath.Base(dst), rerr))
				} else {
					problems = append(problems, fmt.Sprintf("OAuth record not read (%v), so the copy %q it carried was filed as the recovery-recognized aside %q", err, promoted, filepath.Base(dst)))
				}
			}
		} else {
			problems = append(problems, fmt.Sprintf("OAuth record not read: %v", err))
		}
	default:
		// The record's provider field names the instance it belongs to (the
		// OAuth completion paths set it), so it follows the rename.
		record.Provider = newName
		if err := c.auth.saveAuth(c.auth.stateDir, newName, record); err != nil {
			problems = append(problems, fmt.Sprintf("OAuth record not copied: %v", err))
		} else if _, err := c.auth.deleteAuth(c.auth.stateDir, oldName); err != nil {
			problems = append(problems, fmt.Sprintf("OAuth record for %q left behind: %v", oldName, err))
		}
	}
	if len(problems) > 0 {
		return renamePersistedError{fmt.Errorf("renamed %q to %q, but: %s", oldName, newName, strings.Join(problems, "; "))}
	}
	return nil
}

// carryOAuthAsidesForRename carries the in-flight OAuth copies filed under
// oldName to newName across a rename and names what it could not carry. It
// returns the base name of the copy it promoted to the old record path, if any,
// so moveCredentials can tell whether a read that refuses the record is looking
// at a promoted copy it must move back under a name recovery recognizes. The
// newest copy is promoted to the old record path when that path is free, so
// moveCredentials' read carries it as the record and the renamed instance has a
// usable credential at once; when the path is occupied the instance already has
// a live record, so the copy is carried like every other. Every remaining copy
// is renamed to the new name's aside name with its MARKER (hence its kind) and
// stamp preserved, so restoreUncommittedOAuthAsides and reclaimOAuthAsides still
// parse it, kind intact, and no in-flight copy is left filed under a name
// providers.toml no longer carries.
//
// A copy whose stamp cannot be ordered (past an int64) is carried rather than
// promoted: the stamp text is preserved, so it stays parseable even though it
// cannot be ranked. Committed copies are not touched - they belong to a removal
// that stood and startup sweeps those whose name the config no longer carries.
func (c *hubInstancesController) carryOAuthAsidesForRename(oldName, newName string) (string, []string) {
	dir := filepath.Dir(authopenai.AuthFilePath(c.auth.stateDir, "instance"))
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", nil
		}
		return "", []string{fmt.Sprintf("OAuth copies under %q not read, so they could not follow the rename: %v", oldName, err)}
	}
	type carriedAside struct {
		name         string
		stamp        int64
		ranked       bool
		configBacked bool
	}
	var copies []carriedAside
	// The highest stamp already filed under the NEW name, in-flight or committed,
	// across every entry. The carry is the only writer under the caller's credMu, so
	// this seed - bumped locally as copies land - is equivalent to re-listing the
	// directory for every copy, and cheaper.
	var highestDest int64
	var haveDest bool
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		a, aside := parseOAuthAside(e.Name())
		if !aside {
			continue
		}
		if a.inst == newName {
			if s, perr := strconv.ParseInt(a.stampText, 10, 64); perr == nil {
				if !haveDest || s > highestDest {
					highestDest, haveDest = s, true
				}
			}
		}
		if a.committed || a.inst != oldName {
			continue
		}
		c := carriedAside{name: e.Name(), configBacked: a.configBacked}
		if s, perr := strconv.ParseInt(a.stampText, 10, 64); perr == nil {
			c.stamp, c.ranked = s, true
		}
		copies = append(copies, c)
	}
	newest := ""
	var newestStamp int64
	for _, c := range copies {
		if !c.ranked {
			continue
		}
		if newest == "" || c.stamp > newestStamp {
			newest, newestStamp = c.name, c.stamp
		}
	}
	var problems []string
	promoted := ""
	record := authopenai.AuthFilePath(c.auth.stateDir, oldName)
	if newest != "" {
		switch _, statErr := os.Lstat(record); {
		case errors.Is(statErr, os.ErrNotExist):
			if rerr := os.Rename(filepath.Join(dir, newest), record); rerr != nil {
				problems = append(problems, fmt.Sprintf("OAuth copy %q not restored before the rename (%v)", newest, rerr))
			} else {
				promoted = newest
			}
		case statErr != nil:
			problems = append(problems, fmt.Sprintf("check %q before restoring the OAuth copy %q (%v)", record, newest, statErr))
		}
	}
	// The carry must re-stamp the copies in the order the removals made them, so
	// the new name's stamps keep saying which record is newest. os.ReadDir hands
	// entries back in lexical order, and lexical is not numeric when stamps
	// differ in digit length: "...removing-10" sorts before "...removing-9".
	// The search bumps a destination to highest+1 when the source stamp is
	// not greater, so re-stamping in lexical order would push the numerically
	// older 9 above the newer 10 - and startup restores the newest copy, putting
	// the older credential back. Ordering the carry by the parsed stamp
	// ascending preserves the source ordering. A copy whose stamp cannot be
	// parsed (past an int64) is still carried, but not ranked, exactly as the
	// newest-copy search above already skips it.
	ranked := make([]carriedAside, 0, len(copies))
	var unranked []carriedAside
	for _, c := range copies {
		if c.ranked {
			ranked = append(ranked, c)
		} else {
			unranked = append(unranked, c)
		}
	}
	slices.SortStableFunc(ranked, func(a, b carriedAside) int {
		return cmp.Compare(a.stamp, b.stamp)
	})
	carryOrder := make([]carriedAside, 0, len(copies))
	carryOrder = append(carryOrder, ranked...)
	carryOrder = append(carryOrder, unranked...)
	for _, c := range carryOrder {
		if c.name == promoted {
			continue
		}
		// The destination is named from this copy's own stamp, so a copy already
		// filed under the NEW name at that stamp is a real collision. A fresh
		// stamp one past the highest the new name holds avoids replacing it; a
		// carry with no fresh name to take re-files the copy so recovery restores
		// it rather than resolving it forward and deleting it
		// (remarkUncarriedOAuthAside).
		var want int64
		if c.ranked {
			want = c.stamp
		}
		dst, stamp, reason, _ := stepFreeAsideName(filepath.Join(dir, newName+".json"), c.configBacked, want, highestDest, haveDest)
		if reason != asideSearchFound {
			problems = append(problems, remarkUncarriedOAuthAside(dir, c.name, newName,
				fmt.Errorf("no fresh aside name under %q was free to carry it to", newName)))
			continue
		}
		if rerr := renameNoReplace(filepath.Join(dir, c.name), dst); rerr != nil {
			problems = append(problems, remarkUncarriedOAuthAside(dir, c.name, newName, rerr))
			continue
		}
		// The copy landed under the new name, so a later copy must step past its
		// stamp too. The carry is the only writer under credMu, which makes this
		// in-memory bump equivalent to a fresh directory rescan.
		if !haveDest || stamp > highestDest {
			highestDest, haveDest = stamp, true
		}
	}
	return promoted, problems
}

// removalRemedy names the action that really takes an environment-backed
// instance away, keyed on what makes it exist. Every surface that can reach
// Remove (the CLI and direct RPC callers, as much as the panes that hide the
// affordance) reads it, so a remedy has to name something that exists for this
// instance: the variable it reads, the ADC credentials the host supplies, the
// stored credential a keyless instance holds, or - when it holds none and
// needs none - that the row belongs to its provider.
func removalRemedy(inst registry.Instance) string {
	// The variable comes first: it is what supplies the credential the removal
	// cannot take away, whether or not the scheme also resolves without one - a
	// keyless gateway reading OLLAMA_API_KEY is refused for the variable it
	// reads, not for the credential it does not need.
	if varName, ok := strings.CutPrefix(inst.CredentialSource, "env:"); ok {
		return fmt.Sprintf("unset %s instead", varName)
	}
	// No source at all - a keyless instance, or one the registry derives from
	// its provider alone - holds no credential this removal could take away, so
	// naming one to remove would send the caller after something that does not
	// exist. The same words the keyless-with-no-stored-credential case uses say
	// what is actually true of the row.
	if inst.CredentialSource == "none" || inst.CredentialSource == "" {
		return "it comes back with its provider and holds no credential of its own to clear"
	}
	if keylessScheme(inst.Auth) {
		if inst.CredentialSource == "store" {
			return "clear the stored credential instead"
		}
		return "it comes back with its provider and holds no credential of its own to clear"
	}
	if inst.CredentialSource == "adc" {
		return "remove the application-default credentials this host supplies instead"
	}
	return "remove the credential that supplies it instead"
}

// keylessScheme reports whether an auth scheme resolves without a credential, so
// the registry derives the instance whether or not one is stored
// (computeInstances): a keyless local endpoint (auth: none) and a gateway on the
// optional-bearer scheme. Those instances come back with their provider, which
// is what makes them the environment's rather than the user's.
func keylessScheme(auth string) bool {
	return auth == registry.AuthNone || auth == registry.AuthOptionalBearer
}

// environmentBacked reports whether an instance owes its existence to the
// host's environment rather than to a credential the user added through the
// UI. The two differ in what a removal can achieve: a stored key and a
// signed-in Codex record are files under the instance name, so deleting them
// takes the instance with them, while an API-key variable, the ADC file and a
// keyless local endpoint come back with the host - the row would be re-derived
// by the reload right after. The credential source is the discriminator, not
// `implicit` alone: a curated provider is implicit whenever no providers.toml
// entry shadows it, which includes the account a user signed in to
// (client-side parity: fromEnvironment in the AppWire package's
// credentialLabels).
//
// The Codex transport is exempt before the source is consulted, mirroring the
// client: it reads only its OAuth record, and the instance exists because that
// record file does - a record the hub cannot parse is still the user's, and the
// status reports no usable source for it (openAIInstanceStatus) while the
// registry reports the oauth source. Without the exemption a Codex row whose
// status resolved no source (empty/none) would be offered Remove in the UI but
// refused here. The client's other early return - true when the row needs no
// credential - is already the keylessScheme branch: the wire's
// CredentialRequired is exactly `Auth != AuthNone && Auth != AuthOptionalBearer`
// (List, app_instances.go), so `!credentialRequired` is keylessScheme, and
// keylessScheme returns true below. Verified against
// appwire-client/typescript/credentialLabels.ts and this file's List entry.
func environmentBacked(inst registry.Instance) bool {
	if !inst.Implicit {
		return false
	}
	if inst.Auth == registry.AuthOAuthOpenAICodex {
		return false
	}
	if keylessScheme(inst.Auth) {
		return true
	}
	return inst.CredentialSource != "store" && inst.CredentialSource != "oauth"
}

// Remove deletes an instance, its stored key and its OAuth record. An instance
// that exists from the environment has no entry to delete and would come
// straight back, so it is refused with a message saying what to unset instead
// (spec §5.1); one the user credentialed through the UI has no entry either,
// and there the credential cleanup is the removal (environmentBacked). Refusals
// that blame the name the caller sent - a name that resolves to no instance or
// an environment-only one that cannot be deleted - follow Create and Edit's
// convention (#717/#748): appwire.InvalidParams, not a generic wire error.
func (c *hubInstancesController) Remove(params appwire.InstanceRemoveParams) error {
	if err := c.refuseWhenBroken(); err != nil {
		return err
	}
	if err := c.requireAuth(); err != nil {
		return err
	}
	// The name is forwarded to authopenai.DeleteAuth, which joins it into
	// stateDir/auth/<name>.json; validating it here is what keeps a name
	// containing path separators from deleting an arbitrary file.
	name := strings.TrimSpace(params.Name)
	if !registry.ValidInstanceName(name) {
		return appwire.InvalidParams(fmt.Sprintf("invalid instance name %q (lowercase, no slash)", params.Name))
	}

	// The fingerprint key is resolved once, before either lock is taken:
	// resolving it can repair the key file (an inter-process lock and a write),
	// and the removal below holds mu and credMu exclusively, so a repair there
	// would hold every listing and credential op behind it. One key for the
	// whole removal, so the assertion under the locks is checked against the
	// key the caller's row was served with.
	key, keyErr := resolveEndpointFingerprintKey(c.authStateDir())

	c.mu.Lock()
	defer c.mu.Unlock()
	// Read the authored layer before anything is deleted: this is a pure read,
	// so a failure here leaves nothing to undo, and it happens inside c.mu, so
	// the layer it returns is still the one this removal edits. before is an
	// independent parse of the same file - a fresh read sharing no maps with
	// l - so the reload rollback below writes back exactly what was on disk
	// before this call (Edit's own rollback input).
	before, _, err := c.read()
	if err != nil {
		return err
	}
	l, _, err := c.read()
	if err != nil {
		return err
	}

	// Held exclusively across the credential cleanup, the providers.toml write
	// and the reload that follows it, the way a rename holds it across its
	// check and re-key (see hubAuthController.credMu). A credential writer
	// already in flight finishes first, and the cleanup below removes whatever
	// it wrote; one that starts afterwards reads the reloaded registry, where
	// this instance no longer exists. Holding only the read side left a writer
	// that had already passed its checks free to store a key after the
	// cleanup, leaving a credential behind under a name the removal had just
	// deleted.
	c.auth.credMu.Lock()
	defer c.auth.credMu.Unlock()

	// The lookup names what this call deletes - the authored entry, the stored
	// key and the OAuth record under this name - and what decides whether the
	// instance is the user's to remove is the credential source it resolves, so
	// it is asked here, under both locks. c.mu is what holds the deletion, as
	// it is for Edit's: a rename landing between the two would hand the
	// deletion to whatever holds the name afterwards. credMu is what a
	// credential write holds - storing a key or signing the instance in
	// changes the source it resolves without taking c.mu - so a source read
	// before this lock is one a writer may already have flipped: an instance
	// the user has just credentialed would be refused as the environment's,
	// and one a writer has just taken over would be refused with it.
	inst, ok := c.reg.Get().Instance(name)
	if !ok {
		return appwire.InvalidParams(fmt.Sprintf("instance %q not found", name))
	}
	if environmentBacked(inst) {
		return appwire.InvalidParams(fmt.Sprintf("%s exists without an authored entry (%s), so deleting the instance is not what takes it away: %s", name, describeImplicit(inst), removalRemedy(inst)))
	}

	// The confirmation this removal carries names the row the client listed, so
	// a name another client has re-pointed since (a removal and a recreation
	// under it, or an edit to its base_url) is refused rather than having its
	// replacement instance removed. Asked here, under the exclusive lock, so it
	// describes the instance the cleanup below acts on.
	if err := c.auth.verifyEndpointFingerprintWithKey(name, params.ExpectedEndpointFingerprint, key, keyErr, removalFingerprintWording); err != nil {
		return err
	}

	// Credentials first, then the authored entry: a cleanup that cannot
	// complete fails the removal while the instance and its name still exist,
	// so the caller can retry it. The reverse order would report a deletion
	// that only half happened and leave the credential under a name nothing
	// curates - invisible until a later instance of that name inherits it.
	// What the cleanup is about to delete is captured first, because either
	// half of it can still fail - the cleanup itself, or the write below -
	// and both leave [providers.<name>] in place and tell the caller the
	// removal failed, so the instance the caller still has must still
	// authenticate. Capture and restore both sit inside this held lock, so no
	// writer can slip between them.
	//
	// A removal whose cleanup failed, or a hub that died between setting a copy
	// aside and deleting it, leaves the user's credential under a name no reader
	// reads. Nothing else collects those, so the sweep after the reload below is
	// what reclaims them. It runs only once this removal has stood: a refused
	// removal is not the place to fail over unrelated debris, and the refusal is
	// what the caller must act on (see reclaimOAuthAsides).

	// Whether this removal changes providers.toml decides the kind recorded in
	// the aside's name (setAsideOAuthFile): config-backed or credential-only.
	// That is an authored [providers.<name>] entry OR a `default` pointer naming
	// this instance - either one makes configChanged below true, so the file is
	// written. A crash after that write but before the commit mark must not leave
	// a credential-only in-flight copy, which startup would restore and thereby
	// resurrect the removed instance. configCarriesName is that rule, read from
	// `before`, the layer that still holds it; `authored` is reused below for
	// `configChanged`.
	_, authored := before.Providers[name]
	configBacked := configCarriesName(before, name)
	storedKey, hasStoredKey := c.auth.creds.Get(name)
	oauthAside, err := c.setAsideOAuthFile(name, configBacked)
	if err != nil {
		return err
	}

	removed, err := c.removeCredentials(name)
	if err != nil {
		return c.restoreFailedRemoval(name, storedKey, hasStoredKey && removed.storedKey, oauthAside, err, "the instance is still configured")
	}

	// An instance the user credentialed through the UI has no authored entry:
	// the credential cleanup above IS the removal, and writing the absent file
	// back as an empty one would leave a providers.toml the user never had. The
	// reload below is what re-derives the instance set either way. The file is
	// still written when its `default` named this instance, because dropping
	// that pointer is a real change to a file that already exists - leaving it
	// behind would name an instance the next load cannot find.
	delete(l.Providers, name)
	// A `default` naming the instance just removed would fail the next load,
	// so it goes with it; the ranking of §5.1 picks the replacement.
	if l.Default == name {
		l.Default = ""
	}
	configChanged := authored || l.Default != before.Default
	if configChanged {
		if err := c.writeLoadable(l); err != nil {
			return c.restoreFailedRemoval(name, storedKey, hasStoredKey, oauthAside, err, "the instance is still configured")
		}
	}
	// The removal's durable work has landed: the record is set aside, the
	// credential layers are deleted and, when it changed, providers.toml is
	// written. Mark every in-flight copy under this name committed now, before the
	// reload: a hub that dies anywhere from here through the reload and the
	// reclaim below leaves copies startup will not put back
	// (restoreUncommittedOAuthAsides), where one left in flight would resurrect
	// the removal the user just carried out. The mark is not left to the reclaim
	// because the reclaim runs only after the reload, so the whole reload would
	// sit inside the window a crash turns back into an in-flight copy.
	//
	// The commit mark deliberately sits AFTER the durable work, not before it. A
	// finding asked for a marker written first, so a crash mid-removal would
	// resolve forward (the removal treated as done); that policy was considered
	// and rejected. The removal's durable work is the credential deletions and the
	// config write, so a marker written before them cannot say whether they
	// happened - it only moves the ambiguity - and resolving it forward would
	// delete the credential of an instance the caller was told had failed. Every
	// failed removal here rolls back, and startup recovery does the same for a
	// crash mid-removal: the record comes back and the user can remove the
	// instance again. The residual window is therefore exactly one rename, the
	// boundary between the last durable step (the config write, or the credential
	// deletions when the config did not change) and this commit mark.
	//
	// The copy this call set aside now carries its committed name, and the
	// rollback paths below are handed that name: restoreFailedRemoval renames
	// whatever path it is given back to the record path, so a reload that fails
	// still puts the record back. The config-write failure above ran before this
	// point, so its copy is still in flight and its rollback still gets the
	// in-flight name. Nothing is deleted here - the reload that fails must still
	// be able to roll the record back.
	//
	// A mark that cannot land is itself a failed removal, not something to leave
	// to the reclaim: had it been tolerated and had the reclaim's delete failed
	// too, a removal reported as standing would leave an in-flight copy that
	// startup would put back, resurrecting exactly the credential the user
	// removed. So a mark failure rolls the removal back through the same path a
	// failed reload takes, and the caller is told the removal failed - never a
	// persisted removal - which is the state startup recovery exists for. The
	// reclaim still performs the same rename as a retry (reclaimOAuthAsides), so
	// a mark that fails transiently costs nothing once a later removal flushes
	// the copy and marks it; a failure that persists here rejects this removal
	// instead.
	mark, markErr := c.markOAuthAsidesCommitted(name, oauthAside)
	var reloadErr error
	if markErr == nil {
		reloadErr = c.reg.Reload()
	}
	if markErr != nil || reloadErr != nil {
		cause := markErr
		if cause == nil {
			cause = reloadErr
		}
		return c.rollBackFailedRemoval(before, name, storedKey, hasStoredKey, mark, configChanged, cause)
	}
	// The removal stands, so every copy of this name's record is unwanted now:
	// the one this call set aside and any an earlier removal of the name left
	// behind. The copies are the user's credentials under a name no reader looks
	// at, so a failure to delete one is reported even though the removal stands -
	// a caller told the removal succeeded has no reason to look for what it left
	// behind. removePersistedError is what tells the RPC handler to announce the
	// removal and leaves the caller the files the failure names. Nothing is
	// rolled back for it: the config, the registry and the credential the
	// instance resolved are all in their post-removal state.
	//
	// The commit point above already marked these copies committed, and the
	// reclaim performs the same rename again as a retry before deleting them, so a
	// crash in this window still leaves a copy startup can tell apart from one a
	// failed removal set aside - and never puts back.
	if err := c.reclaimOAuthAsides(name); err != nil {
		return removePersistedError{fmt.Errorf("removed %s, but %w", name, err)}
	}
	return nil
}

// setAsideOAuthFile moves the OAuth state file a removal's cleanup is about to
// take away out of the way, so a later failure can put it back, and returns the
// path it now sits at ("" when there was none). It moves the file rather than
// reading it: the record is taken away by path, so one the hub cannot read - or
// cannot parse - is still one the removal has to support, and a rename preserves
// the bytes in exactly the case a read would refuse. A file that cannot be moved
// is refused here, before anything is deleted, because the removal cannot
// promise to restore what it could not set aside.
//
// The name keeps the record's own suffix and adds one no reader looks for, so an
// aside left behind by a crash is never mistaken for a record (only .json files
// are read). It also records the removal's KIND, which is what the removal's own
// rename writes atomically: configBacked when this removal changed providers.toml
// - an authored [providers.<name>] entry or a `default` pointer naming the
// instance at removal start - and credential-only otherwise. Recovery reads that
// kind instead of inferring it from the registry.
func (c *hubInstancesController) setAsideOAuthFile(name string, configBacked bool) (string, error) {
	path := authopenai.AuthFilePath(c.auth.stateDir, name)
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		// Nothing here can tell what the path holds, and the rename below would
		// report this same failure as an aside it could not make. Refused with
		// the check named, before anything is deleted.
		return "", fmt.Errorf("remove %s: read its OAuth state at %s before setting it aside: %w", name, path, err)
	}
	// Only a record is set aside. This path is renamed by the call below and
	// deleted by the reclaim after a standing removal, and a removal may do
	// both to the hub's own record and not to whatever else the user keeps
	// under that name: the reclaim takes copies, so a directory renamed here
	// would be skipped there - and the removal would report success with the
	// user's own contents parked under a name no reader reads and no report
	// mentions. Refused before anything is deleted, with the path named.
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("remove %s: %s is not a regular file, so the removal cannot set its OAuth state aside: move it out of the way to remove the instance", name, path)
	}
	// The stamp steps until it names a path nothing holds. os.Rename replaces an
	// existing destination, so a stamp that repeats for the same record path
	// would have the second aside destroy the first, losing exactly the bytes
	// the aside exists to preserve.
	//
	// The seed is one past the highest stamp already filed for THIS record path,
	// or the clock when there is no copy to step past. The clock alone is not
	// enough: a backward step (an NTP correction, a VM snapshot, a container
	// clock) would file a later removal's copy under a SMALLER stamp than an
	// earlier one's, and startup restores the newest copy of a name - so the
	// older, possibly revoked record would be the one put back while the newer
	// credential sat as inert debris. Stepping past the highest makes the order
	// of the stamps the order of the removals. The search still steps a
	// clock-driven seed past a collision, and stepping is safe here because
	// removals of one name are serialized: every removal holds the caller's
	// credMu exclusively, so no other aside for this path can be created between
	// the existence check and the rename. The candidate keeps the all-digits
	// tail oauthAsideInstance requires, so a stepped name is still reclaimable
	// rather than debris.
	//
	// The marker the copy is written under records the removal's kind, so
	// recovery can decide whether to put it back without asking the registry.
	stamp := c.auth.now().UnixNano()
	if stamp < 0 {
		// A negative stamp is not a name any copy can carry: its decimal text
		// holds a '-' the all-digits tail oauthAsideInstance requires, so the
		// copy would be debris the reclaim silently skips - a removal reported
		// as successful could leave the credential on disk. Refused before
		// anything is deleted, mirroring the stamp bounds the search enforces.
		return "", fmt.Errorf("remove %s: the clock returned the negative stamp %d, which no OAuth aside name can carry", name, stamp)
	}
	aside, _, reason, searchErr := findFreeAsideName(filepath.Dir(path), name, configBacked, stamp)
	switch reason {
	case asideSearchFound:
	case asideSearchDirUnreadable:
		// The stamps already filed for this record path are what orders the
		// copies of a name, so a seed taken from the clock alone cannot be
		// trusted when the directory cannot be listed: an unreadable directory
		// that already holds a higher-stamped copy, together with a clock that
		// stepped backward, would file this copy under a smaller stamp - and
		// startup restores the newest copy, so the older credential would be
		// the one put back while the current one sat as inert debris. Refused
		// rather than guessed, before anything is deleted. A directory that does
		// not exist is not this case: no copy can be filed somewhere that is
		// not there.
		return "", fmt.Errorf("remove %s: list %s to order its OAuth copies before setting one aside: %w", name, filepath.Dir(path), searchErr)
	case asideSearchCandidateUnreadable:
		// The candidate could not be checked, so nothing here can promise
		// the rename will not land on a copy that is already there - and a
		// failure other than "not there" (a directory this process cannot
		// search, a candidate past the name limit) fails the rename too.
		// Stepping past it would spin instead, holding the caller's credMu, so
		// the whole removal is refused with the cause named.
		return "", fmt.Errorf("remove %s: check whether %s is free to set its OAuth state aside: %w", name, aside, searchErr)
	case asideSearchExhausted:
		// Stepping past the maximum would wrap to a negative tail no copy can
		// carry, exactly the bound the search enforces. A removal that cannot
		// name its aside is refused rather than leaving debris the reclaim
		// skips, which would let a reported removal leave the credential on disk.
		return "", fmt.Errorf("remove %s: no aside stamp at or below %d was free to set its OAuth state aside", name, maxAsideStamp)
	}
	if err := os.Rename(path, aside); err != nil {
		return "", fmt.Errorf("remove %s: set its OAuth state aside to preserve it: %w", name, err)
	}
	return aside, nil
}

// oauthAsideMarker separates a record's path from the stamp of the removal that
// set it aside (setAsideOAuthFile). It is what tells a copy apart from a record
// when the leftovers are reclaimed, and it names an IN-FLIGHT copy: the removal
// that made it had not yet done its durable work - the aside, the credential
// deletions and, when it changed, the providers.toml write - when the copy was
// written, so startup puts it back (restoreUncommittedOAuthAsides). This shape
// is the CREDENTIAL-ONLY kind: the removed name had no [providers.<name>] entry
// at removal start (an implicit Codex account, a stored key for a curated
// provider), so the record file is the whole of what made the instance exist.
// Asides written by the release before the kind was recorded carry this shape,
// so they parse as credential-only; that is the conservative reading, because a
// credential-only in-flight copy is always put back when its record path is
// free, which never strands a previous release's bytes.
const oauthAsideMarker = ".removing-"

// oauthCommittedMarker is the same separation for a copy whose removal HAD done
// its durable work: markOAuthAsidesCommitted renames every in-flight copy to this
// shape at the removal's commit point - before the reload that publishes it - and
// the reclaim after a successful reload performs the same rename as a retry. A
// crash from there onward therefore leaves a copy startup can tell apart from one
// a failure set aside - and never puts back. The stamp is preserved, so the two
// shapes order against each other the same way.
const oauthCommittedMarker = ".removed-"

// oauthConfigAsideMarker and oauthConfigCommittedMarker are the same two shapes
// for a removal that CHANGED providers.toml (Remove's `before` naming the
// instance through a [providers.<name>] entry or a `default` pointer), which
// setAsideOAuthFile records in the name it writes atomically. The kind is what
// recovery uses instead of inferring from the registry: an in-flight copy whose
// name the config still carries - an authored entry OR a `default` pointer - is
// an in-doubt removal to put back, while a config-backed copy whose name the
// config no longer carries reached its providers.toml write - the config itself
// is the durable evidence that the removal proceeded - so it is resolved forward
// (returned to the committed shape) rather than restored. A credential-only copy
// is always restored while its record path is free.
const oauthConfigAsideMarker = ".removing-cfg-"
const oauthConfigCommittedMarker = ".removed-cfg-"

// oauthAsideMarkerFor names the in-flight marker for a removal of the given kind.
// The committed shape is its oauthCommittedAsideName.
func oauthAsideMarkerFor(configBacked bool) string {
	if configBacked {
		return oauthConfigAsideMarker
	}
	return oauthAsideMarker
}

// oauthCommittedMarkerFor names the committed marker for a removal of the given
// kind, the counterpart of oauthAsideMarkerFor.
func oauthCommittedMarkerFor(configBacked bool) string {
	if configBacked {
		return oauthConfigCommittedMarker
	}
	return oauthCommittedMarker
}

// oauthAsideShapes is the aside-name grammar in the order it must be tried: the
// config-backed pair before the plain pair because the config-backed markers
// share the plain ones' prefix, and within each pair the committed shape before
// the in-flight one. parseOAuthAside, oauthAsideInstance and oauthAsideStampText
// all read this one table.
var oauthAsideShapes = []struct {
	marker       string
	committed    bool
	configBacked bool
}{
	{oauthConfigCommittedMarker, true, true},
	{oauthConfigAsideMarker, false, true},
	{oauthCommittedMarker, true, false},
	{oauthAsideMarker, false, false},
}

// oauthAside is what the aside-name grammar yields for one name: the instance a
// copy was made from, whether the removal that made it had stood, whether the
// removal was config-backed (oauthConfigAsideMarker), and the all-digits stamp
// between the marker and the name's end. The zero value is "not a copy".
type oauthAside struct {
	inst         string
	committed    bool
	configBacked bool
	stampText    string
	// marker is the aside marker the grammar matched and markerIndex is where
	// that occurrence starts in name. Together they identify the TRAILING
	// occurrence a swap must rewrite: an instance whose own name holds a marker
	// would otherwise have the name's substring swapped instead of the copy's
	// own marker (swapOAuthAsideMarker).
	marker      string
	markerIndex int
}

// parseOAuthAside reads the aside-name grammar once, so a caller that needs any
// of the instance, the shape flags or the stamp parses them together. The aside
// name is the record's whole path - its .json suffix included - plus a stamp, so
// an instance whose own name holds a marker is not one: `x.removing-1`'s record
// is `x.removing-1.json`, whose tail after the marker is not a number, and no
// record a load would read is ever taken for debris.
func parseOAuthAside(name string) (oauthAside, bool) {
	for _, m := range oauthAsideShapes {
		i := strings.LastIndex(name, m.marker)
		if i < 0 {
			continue
		}
		record, stamp := name[:i], name[i+len(m.marker):]
		if !strings.HasSuffix(record, ".json") || stamp == "" {
			continue
		}
		if strings.IndexFunc(stamp, func(r rune) bool { return r < '0' || r > '9' }) >= 0 {
			continue
		}
		return oauthAside{
			inst:         strings.TrimSuffix(record, ".json"),
			committed:    m.committed,
			configBacked: m.configBacked,
			stampText:    stamp,
			marker:       m.marker,
			markerIndex:  i,
		}, true
	}
	return oauthAside{}, false
}

// oauthAsideInstance returns the instance a copy was made from, whether the
// removal that made it had stood, whether the removal was config-backed
// (oauthConfigAsideMarker), and whether name is a copy at all.
func oauthAsideInstance(name string) (inst string, committed, configBacked, aside bool) {
	a, ok := parseOAuthAside(name)
	return a.inst, a.committed, a.configBacked, ok
}

// oauthAsideStampText returns the stamp an aside name carries. The name has
// already been accepted by oauthAsideInstance, so parseOAuthAside reports it and
// the tail after its marker is all digits.
func oauthAsideStampText(name string) string {
	a, _ := parseOAuthAside(name)
	return a.stampText
}

// oauthCommittedAsideName returns the name an in-flight copy takes once the
// removal it belongs to has stood. The stamp is preserved - the committed copy
// orders against the others the same way - and the KIND is preserved, so the
// committed shape still records whether the removal was config-backed. name must
// be an in-flight copy (oauthAsideInstance returned it with committed false), so
// the grammar parses it and swapOAuthAsideMarker rewrites the trailing marker it
// matched.
func oauthCommittedAsideName(name string) string {
	a, _ := parseOAuthAside(name)
	return swapOAuthAsideMarker(name, a, oauthCommittedMarkerFor(a.configBacked))
}

// swapOAuthAsideMarker returns name with the marker occurrence the aside grammar
// matched replaced by replacement. It rewrites the position the PARSER matched,
// not the first marker that appears anywhere: for a copy of an instance whose own
// name holds a marker (`x.removing-cfg-1`, a legal provider name), a search would
// rewrite the instance-name substring and mangle the copy into the aside of a
// different instance - `x.removed-cfg-1.json.removing-5`, which the parser reads
// as an in-flight copy of `x.removed-cfg-1`, defeating the commit mark for the
// copy the removal actually made. The matched occurrence is by construction the
// last marker that leaves an all-digits tail, so it is also the greatest-index
// marker among the four. A name the grammar does not parse is returned unchanged.
func swapOAuthAsideMarker(name string, a oauthAside, replacement string) string {
	if a.marker == "" {
		return name
	}
	return name[:a.markerIndex] + replacement + name[a.markerIndex+len(a.marker):]
}

// oauthInFlightAsideName returns the name a COMMITTED copy takes when a failing
// rollback cannot put it back at the record path: the in-flight shape startup
// recovery reads (restoreUncommittedOAuthAsides). The stamp and the KIND are
// preserved, the exact inverse of oauthCommittedAsideName. name must be a
// committed copy (oauthAsideInstance returned it with committed true), so the
// grammar parses it and swapOAuthAsideMarker rewrites the trailing marker it
// matched.
func oauthInFlightAsideName(name string) string {
	a, _ := parseOAuthAside(name)
	return swapOAuthAsideMarker(name, a, oauthAsideMarkerFor(a.configBacked))
}

// maxAsideStamp is the largest stamp an aside name can carry. freeAsideName
// refuses to step past it: a successor would wrap to a negative tail that
// oauthAsideInstance does not parse, turning a copy into debris no recovery
// reads. (setAsideOAuthFile's own seed guard refuses the same successor.)
const maxAsideStamp int64 = 1<<63 - 1

// renameNoReplace moves src to dst without replacing an existing dst. POSIX
// rename(2) silently replaces its destination, which for an OAuth copy means
// losing the bytes that destination held - a stale copy of the same instance's
// record, or another copy's only surviving credential. It therefore checks the
// destination first and refuses a taken one with an error satisfying
// errors.Is(err, os.ErrExist), so the caller can pick a fresh name or report the
// copy as uncarried, and then performs the move with a SINGLE rename(2). The
// old link-then-unlink left a window between two syscalls in which a crash
// stranded the bytes under both names - a partial move whose old- and new-name
// copies recovery could restore independently. A filesystem that cannot
// hard-link a file no longer matters: no link is attempted.
//
// The check and the move are safe against a concurrent writer because every
// caller serializes under the same lock - Edit's and Remove's credMu (see
// hubAuthController.credMu; the rename carry and the removal both hold it
// across their moves), or startup before the hub serves
// (restoreUncommittedOAuthAsides, run while the process holds hub.lock and
// before the web server is built) - so no other writer can create the
// destination between the check and the move.
//
// src equal to dst is refused explicitly with os.ErrExist. rename(2) on a path
// onto itself is a no-op success, but the callers that rely on the refusal read
// it as "the committed name was taken" - the recovery pass hands a config-backed
// in-flight copy its committed name, and a copy already filed there is exactly
// what must not be overwritten or silently collapsed. link(2) refused this same
// path with EEXIST, so the refusal is kept here rather than lost to rename(2).
func renameNoReplace(src, dst string) error {
	if src == dst {
		return &os.LinkError{Op: "rename", Old: src, New: dst, Err: os.ErrExist}
	}
	if _, statErr := os.Lstat(dst); statErr == nil {
		return &os.LinkError{Op: "rename", Old: src, New: dst, Err: os.ErrExist}
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return statErr
	}
	return os.Rename(src, dst)
}

// freeAsideName picks a path for an in-flight copy of name's record under the
// given kind, carrying a stamp no name in dir already holds. want is the stamp
// preferred (a copy's own, across a rename); the search starts one past the
// highest stamp already filed for name when that is greater, so a copy set down
// later orders newest, and it steps upward past any candidate still taken. found
// is false when no candidate is free - the stamp would pass maxAsideStamp - so
// the caller leaves its source where it is rather than overwriting anything. The
// candidate keeps the all-digits tail oauthAsideInstance requires, so every name
// this can return parses as a copy.
func freeAsideName(dir, name string, configBacked bool, want int64) (string, bool) {
	path, _, reason, _ := findFreeAsideName(dir, name, configBacked, want)
	if reason != asideSearchFound {
		return "", false
	}
	return path, true
}

// asideSearch is why findFreeAsideName and stepFreeAsideName returned no path:
// the directory could not be listed, the candidate could not be checked, or no
// stamp at or below maxAsideStamp was free. asideSearchFound is the zero value -
// a path was found.
type asideSearch int

const (
	asideSearchFound asideSearch = iota
	asideSearchDirUnreadable
	asideSearchCandidateUnreadable
	asideSearchExhausted
)

// highestAsideStamp returns the highest stamp already filed for name in entries,
// in-flight or committed, and whether any was found. A stamp that cannot be
// ordered (past an int64) is skipped, exactly as the search below skips it.
func highestAsideStamp(entries []os.DirEntry, name string) (int64, bool) {
	var highest int64
	var have bool
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		a, aside := parseOAuthAside(e.Name())
		if !aside || a.inst != name {
			continue
		}
		s, perr := strconv.ParseInt(a.stampText, 10, 64)
		if perr != nil {
			continue
		}
		if !have || s > highest {
			highest, have = s, true
		}
	}
	return highest, have
}

// stepFreeAsideName picks the first path for recordPath under the given kind
// whose stamp is want - bumped one past highest when that is greater - and that
// nothing holds, stepping upward past any candidate still taken. It is the one
// collision-safe naming rule freeAsideName and setAsideOAuthFile share. The
// candidate keeps the all-digits tail oauthAsideInstance requires, so every path
// this returns parses as a copy. reason is asideSearchFound on success, and
// otherwise names why the search refused; err carries the filesystem cause where
// there was one.
func stepFreeAsideName(recordPath string, configBacked bool, want, highest int64, have bool) (string, int64, asideSearch, error) {
	stamp := want
	// One past the highest is all digits for any non-negative stamp; the comparison
	// also refuses a maxAsideStamp successor, which would wrap to a negative tail no
	// copy can carry.
	if have && highest < maxAsideStamp && highest+1 > stamp {
		stamp = highest + 1
	}
	marker := oauthAsideMarkerFor(configBacked)
	for {
		candidate := recordPath + marker + strconv.FormatInt(stamp, 10)
		switch _, lerr := os.Lstat(candidate); {
		case errors.Is(lerr, os.ErrNotExist):
			return candidate, stamp, asideSearchFound, nil
		case lerr != nil:
			// The candidate could not be checked, so nothing here can promise the move
			// that follows will not land on something already there.
			return candidate, stamp, asideSearchCandidateUnreadable, lerr
		}
		if stamp >= maxAsideStamp {
			return "", stamp, asideSearchExhausted, nil
		}
		stamp++
	}
}

// findFreeAsideName lists dir, seeds the search from the highest stamp already
// filed for name there, and delegates the collision-safe naming to
// stepFreeAsideName. A directory that cannot be listed is refused rather than
// guessed at - the stamps already filed for name are what a candidate must step
// past, and recovery orders copies by their stamps. A directory that does not
// exist is the empty case: the move that follows reports whatever is really
// wrong.
func findFreeAsideName(dir, name string, configBacked bool, want int64) (string, int64, asideSearch, error) {
	entries, rerr := os.ReadDir(dir)
	if rerr != nil && !errors.Is(rerr, os.ErrNotExist) {
		return "", 0, asideSearchDirUnreadable, rerr
	}
	highest, have := highestAsideStamp(entries, name)
	return stepFreeAsideName(filepath.Join(dir, name+".json"), configBacked, want, highest, have)
}

// remarkUncarriedOAuthAside re-files a copy a rename could not carry so startup
// recovery restores it to the RENAMED instance instead of deleting it, and
// returns the carry problem to report. providers.toml already names only the new
// instance, so re-filing the copy under the OLD name - in any shape recovery
// restores - would put its bytes at the old record path, which no instance uses:
// the renamed instance would be left without the credential the carry was trying
// to preserve. The copy is therefore filed under the NEW name as that name's
// in-flight copy in the CREDENTIAL-ONLY shape (the shape recovery restores even
// without a readable config), chosen the same collision-safe way the carry
// chooses a destination (freeAsideName, which keeps the all-digits stamp
// oauthAsideInstance requires) and landed without replacing anything
// (renameNoReplace). Startup recovery then puts it back at the new record path
// when that path is free. When even that cannot land, the report says plainly
// that the bytes are still filed under the old name in a shape startup will not
// restore.
func remarkUncarriedOAuthAside(dir, name, newName string, cause error) string {
	// The copy's own stamp is preferred, so the fallback orders the same way the
	// carry would have. A stamp that cannot be ordered (past an int64) starts the
	// search at zero and freeAsideName steps past whatever is taken.
	var want int64
	if s, perr := strconv.ParseInt(oauthAsideStampText(name), 10, 64); perr == nil {
		want = s
	}
	dst, ok := freeAsideName(dir, newName, false, want)
	if !ok {
		return fmt.Sprintf("OAuth copy %q not carried to %q, and no fresh credential-only aside name under %q was free to re-file it, so its bytes are still filed under the old name in a shape startup will not restore (%v)", name, newName, newName, cause)
	}
	if rerr := renameNoReplace(filepath.Join(dir, name), dst); rerr != nil {
		return fmt.Sprintf("OAuth copy %q not carried to %q, and it could not be re-filed under %q, so its bytes are still filed under the old name in a shape startup will not restore (%v; %v)", name, newName, filepath.Base(dst), cause, rerr)
	}
	return fmt.Sprintf("OAuth copy %q not carried to %q and re-filed under the new name as %q, which startup recovery restores to the renamed instance rather than deleting the credential (%v)", name, newName, filepath.Base(dst), cause)
}

// commitOAuthAside renames one in-flight copy to its committed shape and returns
// the path the copy carries afterwards: the committed path on success, and the
// in-flight path unchanged when the copy was already committed or the rename
// failed. It is the single rename both the removal's commit point
// (markOAuthAsidesCommitted) and its reclaim (reclaimOAuthAsides) perform, so the
// crash mark and its retry cannot drift apart.
//
// The move never replaces an existing committed path (renameNoReplace): that
// path can hold another copy's bytes, and POSIX rename would silently destroy
// them. A taken destination is a failed commit - the caller treats it as one -
// rather than a completed mark that overwrote whatever was there.
//
// A committed copy is returned untouched rather than renamed again: it already
// carries the shape startup never puts back, and renaming it would only risk
// losing it. The caller decides what a failure means - the commit point fails and
// rolls the removal back on one, while the reclaim reports it.
func commitOAuthAside(path string) (string, error) {
	name := filepath.Base(path)
	_, committed, _, aside := oauthAsideInstance(name)
	if !aside || committed {
		return path, nil
	}
	committedPath := filepath.Join(filepath.Dir(path), oauthCommittedAsideName(name))
	if err := renameNoReplace(path, committedPath); err != nil {
		return path, err
	}
	return committedPath, nil
}

// configCarriesName reports whether providers.toml still carries an instance: an
// authored [providers.<name>] entry or a `default` pointer naming it. A removal
// that only cleared `default` still wrote the file (Remove's configChanged), so
// `default` is one of the facts that decides the aside kind, and recovery must
// read the config the same way - a config-backed in-flight copy a `default`
// pointer names is a removal that never reached its write, and is put back rather
// than resolved forward.
func configCarriesName(l *registry.Layer, name string) bool {
	if _, ok := l.Providers[name]; ok {
		return true
	}
	return l.Default == name
}

// readAsideConfig reads the providers config restoreUncommittedOAuthAsides
// classifies config-dependent copies against, and returns the error that stops
// the pass when there is none it can trust. An EMPTY path is one spelling of an
// absent config: main.go passes "" when EVENER_PROVIDERS_CONFIG is present and
// empty, which cmdutil reads as "no user layer at all" (ProvidersConfigPath). A
// path whose file does not exist is the other: ReadConfigFile returns an EMPTY
// layer with a NIL error for ANY missing file, which is not evidence that the
// config carries no name - a fresh install, a broken symlink, or a config
// removed while the auth directory kept its asides all read that way, and
// treating it as authoritative would resolve every config-backed in-flight copy
// forward and delete the sweep's committed copies.
func readAsideConfig(providersConfigPath string) (*registry.Layer, error) {
	if providersConfigPath == "" {
		return nil, errors.New("there is no providers config path (EVENER_PROVIDERS_CONFIG is present and empty), so there is no config evidence to classify the config-backed copies against")
	}
	layer, exists, err := registry.ReadConfigFile(providersConfigPath)
	if err != nil {
		return nil, err
	}
	if !exists {
		return nil, fmt.Errorf("there is no providers config file at %s", providersConfigPath)
	}
	return layer, nil
}

// asideConfigProblem words the config failure that left the config-dependent
// copies of a pass unclassified, in the two spellings the pass distinguishes: no
// path at all, and a path it could not read.
func asideConfigProblem(providersConfigPath string, cfgErr error) string {
	if providersConfigPath == "" {
		return fmt.Sprintf("%v, so only credential-only in-flight copies were put back and every config-dependent copy was left untouched", cfgErr)
	}
	return fmt.Sprintf("read %s to classify the config-backed copies and sweep the committed ones, so only credential-only in-flight copies were put back and every config-dependent copy was left untouched (%v)", providersConfigPath, cfgErr)
}

// restoreUncommittedOAuthAsides puts back the in-flight OAuth records a removal
// set aside for an instance that still exists. It reports what it could not put
// back - and, for the one deliberate delete whose bytes could be a stranded
// instance's only credential, that it did delete them - and whether it restored
// anything at all.
//
// A removal moves the record aside before it deletes anything, so a hub that
// dies inside that window - or a failed removal whose rename-back did not land
// (restoreFailedRemoval) - leaves a record under its in-flight aside name. The
// KIND recorded in that name, not the provider registry, decides what happens to
// it:
//
//   - A CREDENTIAL-ONLY copy (oauthAsideMarker) has no [providers.<name>] entry
//     to name the instance: the record file was the whole of what made it exist
//     (an implicit Codex account, a stored key for a curated provider). It is
//     put back when its record path is free. That is the rollback policy for an
//     in-doubt removal - deleting it would strand the only credential the
//     instance ever had without the user asking. Asides written by the release
//     before the kind was recorded carry the plain shape and parse as
//     credential-only, so they are restored too, never stranded.
//   - A CONFIG-BACKED copy (oauthConfigAsideMarker) whose name providers.toml
//     STILL carries - an authored [providers.<name>] entry OR a `default`
//     pointer naming it - is an in-doubt removal that never reached its config
//     write: the config is the durable evidence that it did not, and a removal
//     that cleared the `default` pointer is exactly what wrote the file. It is
//     put back when its record path is free.
//   - A CONFIG-BACKED copy whose name the config no longer carries is a removal
//     that DID reach its providers.toml write: it is resolved forward by
//     returning it to its committed shape below, which the sweep then collects.
//     It is NOT put back; doing so would resurrect a removal whose configuration
//     and credentials were already deleted. (This is the high finding: recovery
//     must not infer the kind from the current registry.)
//
// The bytes are moved, never read, so a record the hub cannot read is put back
// as faithfully as any other.
//
// A COMMITTED copy is never put back: it is the leftover of a removal that had
// done its durable work - the commit point marked it
// (markOAuthAsidesCommitted) before the reload that published the removal, and
// the reclaim (reclaimOAuthAsides) then deletes what a crash left. Putting it
// back would undo a removal the caller was told had happened. One whose name
// providers.toml does not carry is also DELETED here: the removal that made it
// stood, so nothing wants it once the name is gone, and no later removal of that
// name will ever collect it. A credential-only copy of that shape whose record
// path is free is reported before it goes, because it could be a failed
// rollback's only surviving credential and a delete must not be silent; what
// actually forces the re-sign-in in that state is the failed rollback that could
// not restore the record, not this delete, which only removes the bytes a human
// could have copied back by hand. One whose name the config DOES carry is left
// where it is - after a removal that failed between its commit mark and its
// rollback, that copy can be the instance's only record, and the conservative
// rule is what protects it. The sweep's protection leans on a committed copy
// meaning the removal STOOD, which is why a failing rollback returns a committed
// copy it cannot put back to the in-flight shape (restoreFailedRemoval). The
// residual this design accepts: if that rollback can neither rename the record
// back to its own path nor rename the copy to the in-flight shape, a committed
// copy of a removal that did not stand survives here, and this sweep deletes it
// on the next start (and reports a credential-only one).
//
// A record filed under its own name again is the record the instance has now, so
// a copy beside it stays too.
//
// A providers.toml that cannot be read does not abort the pass, and a pass with
// nothing set aside does not even consult it: the auth directory is read first,
// and no aside copies means no recovery work a config failure could have
// prevented, so a fresh install without a providers.toml reports nothing. When
// there IS work, the credential-only rule above does not consult the config, so
// those copies are still put back while their record path is free - an unrelated
// parse failure must not strand the one credential a credential-only instance
// ever had. Every config-dependent copy is deferred untouched: a config-backed
// copy cannot be classified without the config (guessing would either resurrect
// a removal that stood or delete a credential that did not), and the sweep that
// deletes committed copies no config names cannot know what the config names. A
// credential-only in-flight copy is deferred too when its instance ALSO has a
// config-dependent copy deferred here: restoring it first would take the record
// path, and when the config later reads the newer config-backed copy is either
// resolved forward (destroyed) or skipped as the path is occupied - a stale
// credential standing in for the current one. The config failure and any such
// whole-instance deferral are reported through the problems path, so a partial
// pass never reads as clean. When the config reads, the pass is exactly as
// before.
//
// An ABSENT config is one of those unreadable configs, and it has two
// spellings. An EMPTY path is one: main.go passes "" when
// EVENER_PROVIDERS_CONFIG is present and empty, which cmdutil reads as "no user
// layer at all" (ProvidersConfigPath). A path whose file does not exist is the
// other: ReadConfigFile returns an EMPTY layer with a NIL error for ANY missing
// file, so a fresh install, a broken symlink, or a config removed while the auth
// directory kept its asides would otherwise read as authoritative evidence that
// the config carries no name. Both would resolve every config-backed in-flight
// copy forward and delete the sweep's committed copies - exactly the state a
// removal interrupted BEFORE its providers.toml write leaves behind - so both
// are treated as no evidence at all: the credential-only half still runs, every
// config-dependent copy is deferred untouched, and the situation is reported.
//
// The forward resolution never overwrites a file already filed under the copy's
// committed name (renameNoReplace): a partial prior rename can leave both paths
// present, and rename(2) would silently destroy the bytes already there - a copy
// of the same instance's record, or another copy's only surviving credential. A
// taken destination leaves the in-flight copy where it is, holds the taken
// committed name out of the sweep so its bytes survive too, and reports both.
//
// One instance can hold several copies - a removal strands one, a later sign-in
// writes the record again, a second removal sets that one aside as well - so the
// newest IN-FLIGHT copy goes back and the rest stay: the newest is the record
// the instance had last. A copy whose stamp cannot be ordered is left alone with
// them.
//
// The caller must reload after a restore that put anything back. A registry
// resolves each instance's credential from the state root as it builds its list
// (registry.Instances), so the credential a config-carried instance resolves is
// visible without one; but that list is computed at load, so a credential-only
// instance whose record came back after the load stays out of the list - and
// out of every listing over it - until the next Reload. The returned bool is
// true when at least one record was put back, so a partial restore (some copies
// reported as problems) still asks for the reload.
func restoreUncommittedOAuthAsides(stateDir, providersConfigPath string) (bool, error) {
	// The config decides two things this pass needs: the KIND of every copy that
	// could be config-backed (one whose name the config no longer carries is
	// resolved forward, one the config still carries is put back) and which
	// committed copies the sweep deletes (the ones no config names). A
	// credential-only in-flight copy needs neither: its recovery is "put it back
	// while its record path is free", which the config cannot change. So a config
	// that cannot be read does not abort the pass - it completes the
	// credential-only half, defers every config-dependent copy untouched (both
	// the config-backed copies and the committed-copy sweep stay exactly where
	// they are for a later pass with a readable config), and reports the failure
	// so a partial pass is never read as clean. Deferring, never guessing, is what
	// keeps an unrelated parse failure from deleting or resurrecting anything.
	//
	// The auth directory is read FIRST, and a pass with nothing set aside returns
	// without consulting or reporting the config: there is then no recovery work
	// a config failure could have prevented, so every fresh install - whose
	// default configuration has no providers.toml - starts without a diagnostic
	// claiming recovery was deferred when there was nothing to recover. A config
	// failure is reported only when there IS work it deferred.
	//
	// Where the records live, asked of the function that places them, so this
	// cannot look somewhere a record never lands.
	dir := filepath.Dir(authopenai.AuthFilePath(stateDir, "instance"))
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			// No directory is nothing set aside: a state root that never held a
			// record has no copy of one.
			return false, nil
		}
		// The directory exists but cannot be read. That is recovery work this
		// pass could not do, and the config failure (which the pass would have
		// needed) is part of why it could not be completed; both are reported.
		var problems []string
		if _, cfgErr := readAsideConfig(providersConfigPath); cfgErr != nil {
			problems = append(problems, asideConfigProblem(providersConfigPath, cfgErr))
		}
		if len(problems) > 0 {
			problems = append(problems, fmt.Sprintf("read the OAuth state directory %s (%v)", dir, err))
			return false, fmt.Errorf("startup OAuth recovery could not finish: %s", strings.Join(problems, ", "))
		}
		return false, fmt.Errorf("put back the OAuth records a failed removal set aside: %w", err)
	}
	hasAside := false
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if _, _, _, aside := oauthAsideInstance(e.Name()); aside {
			hasAside = true
			break
		}
	}
	if !hasAside {
		// Nothing set aside is nothing to recover, so a config failure here
		// prevented no recovery work and is not reported.
		return false, nil
	}
	layer, cfgErr := readAsideConfig(providersConfigPath)
	var problems []string
	if cfgErr != nil {
		problems = append(problems, asideConfigProblem(providersConfigPath, cfgErr))
	}
	// A committed copy carries its name and kind into the delete step, so the
	// delete can report the credential-only ones that could be a stranded
	// instance's only credential rather than removing them silently.
	type committedCopy struct {
		name         string
		inst         string
		configBacked bool
	}
	// The newest in-flight copy of each name that still exists, chosen before
	// anything moves so the choice does not depend on the order the directory
	// hands its entries back.
	type newestCopy struct {
		name  string
		stamp int64
	}
	newest := make(map[string]newestCopy, len(entries))
	var committed []committedCopy
	// A committed copy is durable evidence that a removal's commit mark
	// (markOAuthAsidesCommitted) landed. renameNoReplace is a single rename(2)
	// now, so this pass can no longer create a same-stamp pair - but a release
	// whose move was link-then-unlink could crash between the two syscalls and
	// leave the SAME bytes under BOTH names: an in-flight copy and a committed
	// copy of the same instance at the same stamp. The reader stays in place for
	// that legacy state. A legitimate state cannot produce the pair:
	// setAsideOAuthFile seeds every new copy's stamp one past the highest
	// already filed for the name, in-flight and committed alike, so a later
	// in-flight copy always outranks an earlier committed one. The pair
	// therefore means the move was interrupted, and the committed name is the
	// proof the removal's durable work had landed. The in-flight twin must be
	// treated as committed - never put back, and swept by exactly the rules that
	// govern a committed copy - or recovery would restore a credential whose
	// removal had already done its durable work.
	paired := make(map[string]bool, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		a, aside := parseOAuthAside(e.Name())
		if !aside || !a.committed {
			continue
		}
		paired[a.inst+"\x00"+a.stampText] = true
	}
	// The highest stamp a config-backed in-flight copy PROVES per instance: the
	// config is the durable evidence a removal reached its providers.toml write,
	// and that proof is dated - it says the removal's durable work had landed only
	// as of this copy's stamp. A later credential-only removal of the same name
	// has no such proof, and if its own removal was interrupted its copy must stay
	// restorable, so the classification loop resolves a copy forward only at or
	// before the recorded proof. Collected BEFORE the classification loop so the
	// answer cannot depend on the order os.ReadDir hands the copies back: a
	// credential-only copy read first would otherwise reach the newest map before
	// the config-backed copy proved the removal stood. A copy whose stamp is paired
	// with a committed copy is already treated as committed by the pair rule and is
	// not what resolves forward here; a stamp that does not parse cannot order
	// copies against the proof and is skipped.
	resolvedForward := make(map[string]int64, len(entries))
	if cfgErr == nil {
		for _, e := range entries {
			a, aside := parseOAuthAside(e.Name())
			if e.IsDir() || !aside || a.committed || !a.configBacked {
				continue
			}
			if paired[a.inst+"\x00"+a.stampText] {
				continue
			}
			if configCarriesName(layer, a.inst) {
				continue
			}
			stamp, perr := strconv.ParseInt(a.stampText, 10, 64)
			if perr != nil {
				continue
			}
			if prev, ok := resolvedForward[a.inst]; !ok || stamp > prev {
				resolvedForward[a.inst] = stamp
			}
		}
	}
	// Committed names a forward resolution could not promote onto because the
	// destination was already taken. They are held out of the sweep: the bytes
	// already at the committed name are exactly what the no-replace move refused
	// to overwrite, and deleting them here would lose what it saved.
	protected := make(map[string]bool)
	// Names whose copies this pass cannot judge without the config. A config the
	// pass could not read leaves a config-backed in-flight copy unclassified
	// (restoring it could resurrect a removal that stood; resolving it forward
	// could delete a credential that did not), and it leaves every committed copy
	// the sweep would judge exactly where it is. Neither can be decided, so the
	// whole INSTANCE is deferred: a credential-only in-flight copy of a name that
	// has such a copy is deferred with it (deferredRestores below). Otherwise the
	// credential-only copy would take the record path first, and when the config
	// later reads, the newer config-backed copy is either resolved forward
	// (destroyed) or skipped because the path is occupied - a stale credential
	// standing in for the current one. A name with no config-dependent copy keeps
	// today's behaviour: its credential-only copies are still put back, because
	// an unrelated config failure must not strand the only credential an instance
	// ever had.
	deferredNames := make(map[string]bool)
	deferredRestores := make(map[string]bool, len(entries))
	if cfgErr != nil {
		for _, e := range entries {
			a, aside := parseOAuthAside(e.Name())
			if e.IsDir() || !aside {
				continue
			}
			switch {
			case a.committed:
				// The sweep would judge this committed copy against the config.
				deferredNames[a.inst] = true
			case paired[a.inst+"\x00"+a.stampText]:
				// The in-flight twin is swept by exactly the committed copy's
				// rules, so the config judges it too.
				deferredNames[a.inst] = true
			case a.configBacked:
				// A config-backed in-flight copy cannot be classified without the
				// config.
				deferredNames[a.inst] = true
			}
		}
	}
	for _, e := range entries {
		a, aside := parseOAuthAside(e.Name())
		if e.IsDir() || !aside {
			continue
		}
		inst, isCommitted, configBacked := a.inst, a.committed, a.configBacked
		if isCommitted {
			// Only a committed copy whose name providers.toml does NOT carry is
			// swept below: it belongs to a removal that STOOD, so nothing wants it
			// once the name is gone, and no later removal will ever collect it. A
			// committed copy whose name the config DOES carry is left exactly where
			// it is - after a removal that failed between its commit mark and its
			// rollback, that copy can be the instance's only record, and leaving it
			// is the conservative rule. The residual: a failing rollback that can
			// neither restore the record nor return the copy to the in-flight shape
			// leaves a committed copy of a removal that did not stand, and this
			// sweep deletes it on the next start.
			if cfgErr == nil && !configCarriesName(layer, inst) {
				committed = append(committed, committedCopy{e.Name(), inst, configBacked})
			}
			continue
		}
		if paired[inst+"\x00"+a.stampText] {
			// The in-flight twin of a committed copy: the committed name proves
			// the commit mark landed, so this is never restored. It is treated
			// exactly as the committed copy is - swept with it when the config no
			// longer carries the name, left beside it when it does - so nothing
			// is left for a later pass to restore.
			if cfgErr == nil && !configCarriesName(layer, inst) {
				committed = append(committed, committedCopy{e.Name(), inst, configBacked})
			}
			continue
		}
		if configBacked {
			if cfgErr != nil {
				// The config is what classifies this copy and it cannot be read:
				// leave it in flight for a later pass rather than guessing. A
				// guess would either resurrect a removal that stood or delete a
				// credential that did not.
				continue
			}
			if !configCarriesName(layer, inst) {
				// The removal reached its config write (the config is the durable
				// evidence): resolve it forward rather than restoring it. Returning it
				// to the committed shape lets the sweep below collect it, so it does
				// not stay in flight for a later pass to resurrect. The move is
				// no-replace: a destination already filed under the committed name is
				// left untouched, the copy stays in flight for a later pass, and the
				// take is reported - the alternative, rename(2), silently destroys
				// the bytes already there.
				committedName := oauthCommittedAsideName(e.Name())
				source := filepath.Join(dir, e.Name())
				target := filepath.Join(dir, committedName)
				if rerr := renameNoReplace(source, target); rerr != nil {
					// Hold the taken destination out of the sweep below: deleting it
					// would lose exactly the bytes this refusal protected.
					protected[committedName] = true
					problems = append(problems, fmt.Sprintf("return the config-backed copy %s of %q, whose name the config no longer carries, to the committed shape %s for the sweep: the committed name was taken, so the copy stayed in flight and %s was left untouched (%v)", source, inst, target, target, rerr))
				} else {
					committed = append(committed, committedCopy{committedName, inst, configBacked})
				}
				continue
			}
		}
		// Credential-only copies, and config-backed copies the config still
		// carries, are put back if their record path is free.
		if deferredNames[inst] {
			// The config this copy's disposition depends on could not be read,
			// and a config-dependent copy of the SAME instance is deferred on it
			// (deferredNames): putting this copy back first would take the record
			// path, so the config-backed copy would later be resolved forward or
			// skipped rather than restored. Defer the whole instance to the pass
			// that can read the config.
			deferredRestores[inst] = true
			continue
		}
		stamp, err := strconv.ParseInt(a.stampText, 10, 64)
		if err != nil {
			// A stamp past an int64 is a name no removal wrote, and one that
			// cannot be ordered against the copies beside it, so it can take
			// neither the forward nor the restore path.
			continue
		}
		// A copy resolves forward only when a config-backed copy of the same
		// instance proved the removal reached its providers.toml write AND this
		// copy's stamp is at or before that proof's stamp. The config-backed copy
		// proves the removal stood only up to its own stamp; a later
		// credential-only copy has no such proof, and its interrupted removal must
		// stay restorable, so it falls through to the newest-restorable path
		// below. At or before (not strictly before) keeps a same-stamp copy
		// resolving forward. Such a copy - whatever its kind - is treated exactly
		// as a committed copy is and never put back: swept when the config does
		// not carry the name (which is what made the instance resolve forward) and
		// left beside a carried name otherwise, so nothing is left for a later
		// pass to restore.
		if proof, ok := resolvedForward[inst]; ok && stamp <= proof {
			if !configCarriesName(layer, inst) {
				committed = append(committed, committedCopy{e.Name(), inst, configBacked})
			}
			continue
		}
		if seen, ok := newest[inst]; !ok || stamp > seen.stamp {
			newest[inst] = newestCopy{e.Name(), stamp}
		}
	}
	if len(deferredRestores) > 0 {
		names := make([]string, 0, len(deferredRestores))
		for inst := range deferredRestores {
			names = append(names, inst)
		}
		slices.Sort(names)
		problems = append(problems, fmt.Sprintf("held back the credential-only in-flight copies of %s: the config that would classify a config-dependent copy of the same instance could not be read, so restoring one could stand a stale credential in for the current record", strings.Join(names, ", ")))
	}
	restored := false
	for inst, entry := range newest {
		path := authopenai.AuthFilePath(stateDir, inst)
		// A record filed under its own name again is the one the instance has
		// now - it was written after the removal that set this copy aside - so
		// the copy stays where it is.
		if _, err := os.Lstat(path); err == nil {
			continue
		} else if !errors.Is(err, os.ErrNotExist) {
			problems = append(problems, fmt.Sprintf("check %s before putting %s back (%v)", path, entry.name, err))
			continue
		}
		if err := os.Rename(filepath.Join(dir, entry.name), path); err != nil {
			problems = append(problems, fmt.Sprintf("put %s back as %s (%v)", entry.name, path, err))
			continue
		}
		restored = true
	}
	// Delete the committed copies no config names: the removal they belong to
	// stood, and no later removal of that name will ever collect them, so leaving
	// them would keep the user's credential on disk forever under a name nothing
	// reads. A credential-only copy whose record path is free could be a failed
	// rollback's only surviving credential - the state restoreFailedRemoval
	// leaves when it can neither restore the record nor return the copy to the
	// in-flight shape - so the delete is reported rather than silent. The report
	// names what the delete removes: the bytes a human could have copied back by
	// hand. What forces a re-sign-in in that state is the failed rollback, not
	// the delete. A delete that fails is reported with the rest of this pass's
	// problems either way.
	for _, c := range committed {
		if protected[c.name] {
			continue
		}
		loneCredential := false
		if !c.configBacked {
			if _, statErr := os.Lstat(authopenai.AuthFilePath(stateDir, c.inst)); errors.Is(statErr, os.ErrNotExist) {
				loneCredential = true
			}
		}
		if err := os.Remove(filepath.Join(dir, c.name)); err != nil {
			if !errors.Is(err, os.ErrNotExist) {
				problems = append(problems, fmt.Sprintf("delete the committed copy %s whose name the config no longer carries (%v)", c.name, err))
			}
			continue
		}
		if loneCredential {
			problems = append(problems, fmt.Sprintf("deleted the committed credential-only copy %s of %q, whose name the config no longer carries and whose record path is free, so those bytes were the last copy of that credential a human could have put back by hand", c.name, c.inst))
		}
	}
	if len(problems) > 0 {
		return restored, fmt.Errorf("startup OAuth recovery could not finish: %s", strings.Join(problems, ", "))
	}
	return restored, nil
}

// oauthCommitMark is what one removal's commit point (markOAuthAsidesCommitted)
// changed: the path this removal's own copy now carries ("" when it set nothing
// aside) and every path the mark touched, the own one included. The rollback
// needs the whole set, not just the own path: a copy the mark committed for a
// removal that then rolled back would otherwise be left in the committed shape
// startup never restores, stranding an instance's only OAuth credential
// (rollBackFailedRemoval). A copy whose commit rename failed stays in the
// in-flight shape and is returned unchanged.
type oauthCommitMark struct {
	own    string
	marked []string
}

// markOAuthAsidesCommitted moves every IN-FLIGHT copy filed under name to its
// committed shape, and returns the paths it touched - this removal's own copy
// and every other copy of the name an earlier failed removal left behind - with
// an error naming anything it could not mark. It is the removal's commit point,
// run once the aside, the credential deletions and the providers.toml write have
// all landed and immediately before the reload that publishes the removal
// (Remove), so a hub that dies anywhere from there onward leaves copies startup
// will never put back (restoreUncommittedOAuthAsides) instead of restoring a
// removal the user carried out. The mark is not left to the reclaim below
// because the reclaim runs only after the reload: the whole reload would then
// sit inside a window a crash turns back into an in-flight copy.
//
// A failure to mark is reported, not swallowed: the caller rolls the removal back
// through the same path a failed reload takes (rollBackFailedRemoval), so an
// in-flight copy can only survive beside a removal the caller was told had
// failed. Had the mark been tolerated and the reclaim's delete then failed too, a
// removal reported as standing would leave a copy startup restores - undoing the
// removal. The reclaim still performs the same rename as a retry
// (reclaimOAuthAsides), so a mark that fails transiently costs nothing beyond
// this rejection: the rollback renames the copy back, and the next removal sets
// it aside and marks it again. The returned mark is what lets that rollback undo
// the mark for the copies it did not set aside itself (reinstateMarkedAsides).
func (c *hubInstancesController) markOAuthAsidesCommitted(name, own string) (oauthCommitMark, error) {
	mark := oauthCommitMark{own: own}
	dir := filepath.Dir(authopenai.AuthFilePath(c.auth.stateDir, "instance"))
	entries, err := os.ReadDir(dir)
	if err != nil {
		// No directory is nothing to mark: a state root that never held a record
		// has no copy of one.
		if errors.Is(err, os.ErrNotExist) {
			return mark, nil
		}
		return mark, fmt.Errorf("mark the OAuth copies the removal of %s set aside as committed: %w", name, err)
	}
	var problems []string
	for _, e := range entries {
		inst, committed, _, aside := oauthAsideInstance(e.Name())
		// A directory is skipped for the same reason the reclaim skips one: the
		// hub sets copies aside as regular files, so a directory filed under a
		// copy's name was not written by a removal.
		if e.IsDir() || !aside || committed || inst != name {
			continue
		}
		path := filepath.Join(dir, e.Name())
		renamed, markErr := commitOAuthAside(path)
		mark.marked = append(mark.marked, renamed)
		if own != "" && e.Name() == filepath.Base(own) {
			mark.own = renamed
		}
		if markErr != nil {
			problems = append(problems, fmt.Sprintf("%s (%v)", path, markErr))
		}
	}
	if len(problems) > 0 {
		return mark, fmt.Errorf("mark the OAuth copies the removal of %s set aside as committed: %s", name, strings.Join(problems, ", "))
	}
	return mark, nil
}

// reinstateMarkedAsides returns the copies one removal's commit mark moved to the
// committed shape but the rollback did not put back, renaming each to the
// in-flight shape startup recovery reads (restoreUncommittedOAuthAsides). The own
// copy is left to the caller's rename-back (restoreFailedRemoval); every other
// copy belongs to a removal that did not stand, and startup never restores a
// committed copy while the restored config carries the name - so leaving one
// committed would strand an instance's only credential. A copy whose commit
// rename already failed is still in flight and is skipped. A copy whose return
// cannot land is reported, because its bytes then sit in the shape the sweep
// deletes rather than the one recovery reads.
func (c *hubInstancesController) reinstateMarkedAsides(mark oauthCommitMark) []string {
	var problems []string
	ownBase := ""
	if mark.own != "" {
		ownBase = filepath.Base(mark.own)
	}
	for _, path := range mark.marked {
		base := filepath.Base(path)
		if base == ownBase {
			continue
		}
		_, committed, _, aside := oauthAsideInstance(base)
		if !aside || !committed {
			continue
		}
		inFlight := filepath.Join(filepath.Dir(path), oauthInFlightAsideName(base))
		if err := renameNoReplace(path, inFlight); err != nil {
			problems = append(problems, fmt.Sprintf("the committed copy %s could not be returned to the in-flight shape startup recovery reads (%v)", base, err))
		}
	}
	return problems
}

// reclaimOAuthAsides deletes the copies of one name's OAuth record that a
// removal of that name has just made unwanted: the copy the call set aside and
// any an earlier removal of the name left behind. It is run once the removal has
// stood, after the reload that drops the instance from the registry, and it
// takes copies FILED UNDER THAT NAME ONLY.
//
// It renames each in-flight copy to its committed name before deleting it. The
// removal's commit point (markOAuthAsidesCommitted) already did that rename
// before the reload, so this is a retry that normally finds the work done; the
// rename (and the unmarked handling below) is a backstop for a copy the commit
// point left in flight, which a removal reaching here should not have. The rename
// is what keeps a crash before the delete from leaving a copy startup would put
// back (restoreUncommittedOAuthAsides); the delete is the whole point.
//
// The two failure classes are described separately. A copy whose DELETE failed
// is a credential still on disk, and deleting it is what takes it away - the
// caller is the only one left who can. A copy whose commit rename failed but
// which WAS deleted leaves nothing on disk, so it is reported as the crash mark
// that could not be set, never as a leftover: naming a file that is gone would
// send the caller after nothing. A rename that failed and whose delete failed
// too names both, because the copy is on disk in its in-flight shape and startup
// would put it back.
//
// The name is the whole of what makes a copy safe to take. A removal is the only
// call that can know the bytes are no longer wanted, so a copy under any other
// name - including one whose own removal failed after setting the record aside
// and could not put it back (restoreFailedRemoval) - may be the last surviving
// credential of an instance the user still has. Taking it from here would turn a
// repairable file-level failure into a forced sign-in, so it is left where it
// is, and the failure that stranded it named it at the time. Removing that name
// again is what reclaims it, because a removal of the name is the caller saying
// the credential is not wanted any more.
//
// A failure is reported rather than ignored: the removal stands, and the caller
// is the only one left who can delete the copy the report names. Both classes
// reach the caller as the removal's own failure (removePersistedError), because
// the removal stood and every client has to drop the row.
func (c *hubInstancesController) reclaimOAuthAsides(name string) error {
	// Where the records live, asked of the function that places them, so the
	// sweep cannot look somewhere a record never lands.
	dir := filepath.Dir(authopenai.AuthFilePath(c.auth.stateDir, "instance"))
	entries, err := os.ReadDir(dir)
	if err != nil {
		// No directory is nothing to reclaim: a state root that never held a
		// record has no copy of one to collect.
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("collect the OAuth copies the removal of %s set aside: %w", name, err)
	}
	var unmarked, remaining []string
	for _, e := range entries {
		inst, _, _, aside := oauthAsideInstance(e.Name())
		// A directory is skipped: this takes the copies the hub itself set
		// aside, and deleting a directory's contents is not something a removal
		// may do. setAsideOAuthFile refuses anything but a regular file at the
		// record path, so one filed under a copy's name was not written by a
		// removal.
		if e.IsDir() || !aside || inst != name {
			continue
		}
		path := filepath.Join(dir, e.Name())
		// The commit mark, first, is what keeps a crash before the delete below
		// from leaving a copy startup would put back; a copy already committed by
		// the removal's commit point comes back unchanged.
		committedPath, markErr := commitOAuthAside(path)
		if delErr := c.auth.deleteAside(committedPath); delErr != nil {
			remaining = append(remaining, fmt.Sprintf("%s (%v)", committedPath, delErr))
			if markErr != nil {
				// The copy is on disk in its in-flight shape, so startup would put
				// it back; name the unset crash mark beside the failed delete.
				remaining = append(remaining, fmt.Sprintf("%s could not be marked committed first (%v)", path, markErr))
			}
			continue
		}
		if markErr != nil {
			// Deleted, but the crash mark was never set. What is wrong is the
			// mark, not a leftover; a copy that could not be marked and WAS
			// deleted must not be described as remaining.
			unmarked = append(unmarked, fmt.Sprintf("%s (%v)", path, markErr))
		}
	}
	var problems []string
	if len(unmarked) > 0 {
		problems = append(problems, fmt.Sprintf("a credential copy the removal of %s set aside could not be marked committed, though it was deleted: %s", name, strings.Join(unmarked, ", ")))
	}
	if len(remaining) > 0 {
		problems = append(problems, fmt.Sprintf("a credential the removal of %s set aside is still on disk, and deleting it is what takes it away: %s", name, strings.Join(remaining, ", ")))
	}
	if len(problems) > 0 {
		return errors.New(strings.Join(problems, "; "))
	}
	return nil
}

// rollBackFailedRemoval undoes a removal that failed at or after its commit
// point - a reload that could not resolve the config it wrote, or a commit mark
// that could not land - and reports what it could not put back. Both failures
// share this one path so they cannot drift apart. When the config was changed it
// writes before back first; a rollback write that cannot land leaves the entry
// gone - the file on disk is the one the REMOVAL wrote - and is reported as a
// persisted removal once the registry is reloaded over that file (a failed mark
// never parked the registry, so without the reload the hub would keep serving an
// instance providers.toml no longer carries). It restores the credentials this
// call deleted and the record from whichever aside path it now carries
// (restoreFailedRemoval renames whatever path it is handed), returns every OTHER
// copy this removal's mark committed to the in-flight shape startup recovery
// reads (reinstateMarkedAsides), then reloads. The un-marking is what keeps a
// removal reported as rolled back from stranding an instance whose only OAuth
// copy an earlier failed removal had already set aside: startup never restores a
// committed copy while the restored config carries the name. A rollback that
// fails at the write leaves the removal standing, so the copies stay committed
// and the reclaim or the sweep collects them. On the rollback-write failure that
// reload is over the removal's file; on every other path it is over the restored
// pre-removal file, where the retry is the recovery for a failed reload that
// parked the registry on the implicit-only view a failed load leaves.
// configChanged is what selects both the rollback write and the wording of a
// retry that fails, so a caller can tell whether the config the rollback
// restored loads.
func (c *hubInstancesController) rollBackFailedRemoval(before *registry.Layer, name, storedKey string, hasStoredKey bool, mark oauthCommitMark, configChanged bool, cause error) error {
	if configChanged {
		// writeLoadable's dry parse only checks the layer against the registry
		// schema; Reload resolves it, so a config that parses can still fail to
		// load (#711). A failed reload drops the registry to implicit-only and
		// refuses every instance write until the file loads again, so leaving
		// the removal in place would have the file, the hub's view and every
		// client's listing disagreeing about an instance only some of them
		// still have - with nothing but hand-editing the file to get back. Put
		// the file and the credentials this call deleted back, the way Edit
		// restores its file.
		if restoreErr := c.write(before); restoreErr != nil {
			// The rollback could not land, so the entry stays gone: the file on
			// disk is still the one this removal wrote, and providers.toml carries
			// the removal out.
			//
			// The registry must be reloaded over that file, or the hub keeps
			// serving the instance the config no longer has. The failure that
			// brought us here does not do it: a failed mark never parked the
			// registry, so it still holds the pre-removal view, and a failed
			// reload parked it on the implicit-only view a load of the REMOVAL's
			// file produced. Either way the file that stands is the removal's,
			// which writeLoadable already validated, so loading it is what makes
			// the registry agree. A reload that fails too is reported beside the
			// failed write, so nobody reads a half-repaired hub as healthy.
			//
			// What is left is a persisted removal however the credentials end:
			// the RPC handler announces it and every other client drops the row,
			// which is what keeps their lists from disagreeing with the file -
			// and the caller from retrying a removal whose entry is already gone.
			//
			// What decides whether the credentials go back is what would
			// re-derive the instance's row, not whether an authored entry
			// existed. The removal's config write took any authored
			// [providers.<name>] entry away, so an instance whose provider the
			// registry does NOT derive from a credential is left with no row: its
			// credentials go back, under the name the caller re-authors once this
			// removal is reported as standing.
			//
			// A curated implicit provider is different: computeInstances
			// (llm/registry/instances.go) adds a row for every curated id whose
			// Implicit flag is set and whose credential resolves without the
			// network, with no authored entry needed. Restoring this removal's
			// credential would therefore re-create the row the caller was just
			// told is gone - even when the instance also had an authored entry,
			// which the config write removed. The registry's provider view
			// answers the question for any provider id, curated or not, so
			// nothing here hardcodes a provider name. Such an instance's
			// credential stays deleted and its aside is reclaimed; the removal
			// stands and the listing agrees with the file.
			remnant := fmt.Errorf("%w; the rollback could not be written, so the removal stands in the config (%w)", cause, restoreErr)
			derivedFromCredential := false
			if p, ok := c.reg.Get().Provider(name); ok && registry.BoolValue(p.Implicit) {
				derivedFromCredential = true
			}
			if derivedFromCredential {
				if rerr := c.reclaimOAuthAsides(name); rerr != nil {
					remnant = fmt.Errorf("%w; and the credentials this removal deleted could not be kept deleted (%w)", remnant, rerr)
				}
			} else {
				remnant = c.restoreFailedRemoval(name, storedKey, hasStoredKey, mark.own, remnant, "the entry is gone from the config")
				if more := c.reinstateMarkedAsides(mark); len(more) > 0 {
					remnant = fmt.Errorf("%w; %s", remnant, strings.Join(more, " and "))
				}
			}
			if reloadErr := c.reg.Reload(); reloadErr != nil {
				return removePersistedError{fmt.Errorf("%w; the registry could not be reloaded over the removal the config still carries either (%w)", remnant, reloadErr)}
			}
			return removePersistedError{remnant}
		}
	}
	// The credentials go back before the reload below, because a load resolves
	// each instance's credential from the stores: one that runs while this
	// call's deletions are still missing caches "none" as the source of the
	// instance the caller still has, and putting the key back afterwards does
	// not rebuild that view. The pane would then show a stored key beside no
	// active source, and the next launch would be refused for missing
	// credentials, until some later write happened to reload again.
	restored := c.restoreFailedRemoval(name, storedKey, hasStoredKey, mark.own,
		fmt.Errorf("removing %q was rolled back: %w", name, cause), "the instance is still configured")
	if more := c.reinstateMarkedAsides(mark); len(more) > 0 {
		restored = fmt.Errorf("%w; %s", restored, strings.Join(more, " and "))
	}
	if reloadErr := c.reg.Reload(); reloadErr != nil {
		if configChanged {
			// The file this rollback put back is the pre-removal one, and the
			// reload that failed read the file this call wrote - so if the config
			// was already unresolvable before the removal (Remove's own guard
			// reads the registry, which had not been reloaded since and so never
			// saw the change that broke it), this reload fails on the very file
			// the rollback restored. The registry stays on the implicit-only view
			// a failed load leaves and refuses instance writes until the file
			// loads (registry.go's WritesRefused, spec §10). Reinstating the
			// previous registry view would instead have the hub serve and rewrite
			// a config it cannot load, which is what that refusal exists to
			// prevent - so the failure reports what is left: the rollback landed,
			// and the config still does not load. A caller told only that the
			// removal "was rolled back" would read the hub as healthy.
			return fmt.Errorf("%w; the config it restored does not load either, so instance writes stay refused until it does (%w)", restored, reloadErr)
		}
		// Nothing was written, so there is no file to put back: restoring the
		// credentials this call deleted is the whole rollback, and a write here
		// would create the providers.toml Remove's guard exists to avoid. The
		// reload is still retried once the credentials are back: a failed reload
		// parked the registry on the implicit-only view a failed load leaves
		// (writes refused, this row missing), and the state the file describes is
		// unchanged, so a second attempt is the recovery rather than a repetition.
		return fmt.Errorf("%w; the registry could not be reloaded either, so instance writes stay refused until it can be (%w)", restored, reloadErr)
	}
	return restored
}

// restoreFailedRemoval puts back what the cleanup deleted after a failed
// removal, and folds whatever it could not restore into the error the caller
// sees: a caller told only that the removal failed would have no way to know
// what state the name is in. frame names that state in the failure to restore
// - whether the entry is still authored or the removal stood - so the message
// reads as correct English for the failure that produced it. Its callers pass
// only the layers the failure actually deleted, so this never rewrites - and
// never reports a failure to rewrite - a credential that is still where it was.
//
// When the record cannot be renamed back to its own path and the aside it was
// handed is committed-shaped, it renames the copy to its IN-FLIGHT shape
// instead. A committed copy must mean the removal STOOD, or the startup sweep
// (restoreUncommittedOAuthAsides) cannot tell debris from a failed removal's
// only credential: the sweep deletes committed copies whose name the config
// does not carry, which for a credential-only instance is every committed copy.
// Returning the copy to the shape recovery reads is what keeps that true when a
// failing rollback cannot put the record back under its own name - the state on
// disk then says the removal did not stand, and startup restores it instead of
// the sweep deleting it.
func (c *hubInstancesController) restoreFailedRemoval(name, storedKey string, hasStoredKey bool, oauthAside string, cause error, frame string) error {
	var problems []string
	if hasStoredKey {
		if err := c.auth.setCredential(name, storedKey); err != nil {
			problems = append(problems, fmt.Sprintf("its stored key could not be restored (%v)", err))
		}
	}
	if oauthAside != "" {
		// Moved back rather than rewritten. The bytes were never read, so this
		// restores a record the hub cannot read as faithfully as one it can, and
		// a rename cannot leave the half-written file a copy could.
		if err := os.Rename(oauthAside, authopenai.AuthFilePath(c.auth.stateDir, name)); err != nil {
			restoreErr := err
			if _, committed, _, _ := oauthAsideInstance(filepath.Base(oauthAside)); committed {
				inFlight := filepath.Join(filepath.Dir(oauthAside), oauthInFlightAsideName(filepath.Base(oauthAside)))
				if fallbackErr := os.Rename(oauthAside, inFlight); fallbackErr != nil {
					problems = append(problems, fmt.Sprintf("its OAuth record could not be restored (%v), and its committed copy could not be returned to the in-flight shape startup recovery reads (%v)", restoreErr, fallbackErr))
				} else {
					problems = append(problems, fmt.Sprintf("its OAuth record could not be restored (%v), so its committed copy was returned to the in-flight shape startup recovery reads (%s)", restoreErr, filepath.Base(inFlight)))
				}
			} else {
				problems = append(problems, fmt.Sprintf("its OAuth record could not be restored (%v)", restoreErr))
			}
		}
	}
	if len(problems) == 0 {
		return cause
	}
	return fmt.Errorf("%w; %s, but %s", cause, frame, strings.Join(problems, " and "))
}

// removeCredentials deletes the credential layers filed under a name whose
// instance is being removed: the stored key and the OAuth record. Both go
// through the controller's seams, like every other path that removes a
// credential (Logout, ApiKeyClear), and neither failure is tolerated - a
// credential left behind sits under a name nothing curates, and a later
// instance holding that name would inherit it. A missing entry is not a
// failure: Store.Clear deletes and persists, and DeleteAuth reports not-found
// as (false, nil).
//
// Only a layer that is actually there is touched. Store.Clear persists the
// file it holds whatever it was asked to delete, so clearing an entry that was
// never there rewrites state the instance does not have - and on a credentials
// path that is gone or unwritable that rewrite fails the removal over a
// credential it never had, while the deletion of a missing OAuth record is a
// no-op by construction.
//
// It reports the stored key it actually deleted even when it fails, because its
// caller restores exactly that: Store.Clear puts its own entry back when the
// persist fails (nothing deleted), while a failed DeleteAuth leaves its file in
// place - rewriting either would be a false alarm on a disk that is already
// refusing writes. The OAuth record is not reported, and cannot be: the removal
// moves it aside before this runs (setAsideOAuthFile), so DeleteAuth finds
// nothing at the record's path and its "removed" answer is always false here. A
// caller keying a restore on that answer would never put the record back; the
// aside is what restoreFailedRemoval puts back instead.
func (c *hubInstancesController) removeCredentials(name string) (deletedCredentials, error) {
	var deleted deletedCredentials
	if _, stored := c.auth.creds.Get(name); stored {
		if err := c.auth.clearCredential(name); err != nil {
			return deleted, fmt.Errorf("remove %s: clear stored credential: %w", name, err)
		}
		deleted.storedKey = true
	}
	if _, err := c.auth.deleteAuth(c.auth.stateDir, name); err != nil {
		return deleted, fmt.Errorf("remove %s: delete OAuth state: %w", name, err)
	}
	return deleted, nil
}

// deletedCredentials names which credential layers a removal's cleanup
// actually removed, so a restore rewrites only those.
type deletedCredentials struct {
	storedKey bool
}

// describeImplicit names what makes an implicit instance exist, so the remove
// refusal can say what to take away.
func describeImplicit(inst registry.Instance) string {
	switch src := inst.CredentialSource; {
	case strings.HasPrefix(src, "env:"):
		return src
	case src == "oauth":
		return "OAuth record for " + inst.Name
	case src == "store":
		return "credentials.toml entry for " + inst.Name
	case src == "none" || src == "":
		// A keyless row has no credential that makes it exist; "credential
		// source none" would name an object the caller cannot go clear.
		return "no credential of its own"
	default:
		return "credential source " + src
	}
}

// instanceModels renders an instance's model inventory for the sheet's
// per-model toggles. A registry that cannot list the instance yields no
// rows rather than an error: the entry still describes the instance.
func instanceModels(r *registry.Registry, name string) []appwire.InstanceModelEntry {
	if r == nil {
		return nil
	}
	models, err := r.InstanceModels(name)
	if err != nil {
		return nil
	}
	out := make([]appwire.InstanceModelEntry, 0, len(models))
	for _, m := range models {
		out = append(out, appwire.InstanceModelEntry{ID: m.ID, Disabled: m.Disabled})
	}
	return out
}

// RefreshModels fetches one instance's live listing into the held registry,
// then answers with the updated list. It is a read: no file is written, so
// it stays available while writes are refused. A failed fetch is an error,
// not a catalog-only list — the sheet keeps its catalog rows and toasts
// the failure.
func (c *hubInstancesController) RefreshModels(ctx context.Context, params appwire.InstanceRefreshModelsParams) (appwire.InstanceListResponse, error) {
	reg := c.reg.Get()
	if reg == nil {
		return appwire.InstanceListResponse{}, errors.New("providers.toml cannot be read: the provider registry has not loaded")
	}
	name := strings.TrimSpace(params.Name)
	if _, ok := reg.Instance(name); !ok {
		return appwire.InstanceListResponse{}, appwire.InvalidParams(fmt.Sprintf("instance %q not found", name))
	}
	if err := fetchInstanceLive(ctx, c.reg, name); err != nil {
		return appwire.InstanceListResponse{}, err
	}
	return c.List(), nil
}

// writeAndReload persists a mutated layer and reloads the registry: the
// tail Edit and SetModelDisabled share. writeLoadable's dry parse only
// checks TOML syntax against the registry schema; it does not resolve the
// config the way Reload does. A standalone instance (no base, and its own
// name is not a registry id either) that just lost its only base_url is a
// config that parses fine but cannot resolve an endpoint (llm/registry:
// "no base URL: set base_url = … or base = <registry id>"), and one bad
// instance record fails the whole reload, not just this one (#711).
// Restore the file this call just overwrote instead of leaving every
// instance operation refused by a config only this write produced. verb
// names the write in the refusal ("edit", "toggle").
func (c *hubInstancesController) writeAndReload(before, l *registry.Layer, name, verb string) error {
	if err := c.writeLoadable(l); err != nil {
		return err
	}
	if err := c.reg.Reload(); err != nil {
		if restoreErr := c.write(before); restoreErr != nil {
			return fmt.Errorf("%w (and restoring the previous config failed: %w)", err, restoreErr)
		}
		_ = c.reg.Reload() // best-effort: put the last-good registry view back
		return appwire.InvalidParams(fmt.Sprintf("this %s would leave %q unable to load: %v", verb, name, err))
	}
	return nil
}

// SetModelDisabled flips one model row's disabled flag, writing through
// aliases where the flag is shared: a same-provider alias id resolves to its
// target and the flag lands there, so all names of one model on one
// connection share one flag. A cross-provider alias carries its own row on
// this instance — the config layer cannot author the target's record — so
// the flag lands on the alias row and each connection keeps its own choice.
// It writes an explicit bool — authoring the row when the model exists only
// as a curated entry — so the choice survives catalog refreshes. Refusals
// follow Create's convention: the caller sent the bad name, so unknown
// instances, glob ids, dangling aliases, and unknown rows come back as
// appwire.InvalidParams.
func (c *hubInstancesController) SetModelDisabled(params appwire.InstanceSetModelDisabledParams) error {
	if err := c.refuseWhenBroken(); err != nil {
		return err
	}
	if err := c.requireAuth(); err != nil {
		return err
	}
	name := strings.TrimSpace(params.Name)
	model := strings.TrimSpace(params.Model)

	c.mu.Lock()
	defer c.mu.Unlock()
	// Held across the write and the reload like every other mutation here
	// (hubAuthController.credMu): a reload commits the credential view it
	// read, so one running across a credential write's clear could publish a
	// state that clear had already invalidated.
	c.auth.credMu.Lock()
	defer c.auth.credMu.Unlock()
	if _, ok := c.reg.Get().Instance(name); !ok {
		return appwire.InvalidParams(fmt.Sprintf("instance %q not found", name))
	}
	// AliasTarget names the row the flag lands on: a same-provider alias's
	// target, or a cross-provider alias's own row. Membership, glob, and
	// dangling refusals all come from the same answer.
	target, err := c.reg.Get().AliasTarget(name, model)
	if err != nil {
		return appwire.InvalidParams(err.Error())
	}
	// before is an independent parse from l below — a fresh read sharing no
	// maps with it — so a toggle that parses fine but fails to load restores
	// exactly what was on disk, the way Edit's own before does.
	before, _, err := c.read()
	if err != nil {
		return err
	}
	l, _, err := c.read()
	if err != nil {
		return err
	}
	p, ok := l.Providers[name]
	if !ok {
		// An implicit instance has no authored entry; shadowing it carries
		// the toggle alone, the way Edit shadows its own fields.
		p = registry.Provider{ID: name}
	}
	if p.Models == nil {
		p.Models = map[string]registry.Model{}
	}
	row := p.Models[target.Model]
	row.ID = target.Model
	disabled := params.Disabled
	row.Disabled = &disabled
	p.Models[target.Model] = row
	l.Providers[name] = p
	return c.writeAndReload(before, l, name, "toggle")
}

// SetDefault records which instance a bare model reference resolves on. A
// name that resolves to no instance follows Create and Edit's convention
// (#717/#748): the caller sent it, so it comes back as appwire.InvalidParams.
func (c *hubInstancesController) SetDefault(params appwire.InstanceSetDefaultParams) error {
	if err := c.refuseWhenBroken(); err != nil {
		return err
	}
	if err := c.requireAuth(); err != nil {
		return err
	}
	name := strings.TrimSpace(params.Name)

	c.mu.Lock()
	defer c.mu.Unlock()
	// Held across the write and the reload like every other mutation here
	// (hubAuthController.credMu): a reload commits the credential view it
	// read, so one running across a credential write's clear could publish a
	// state that clear had already invalidated.
	c.auth.credMu.Lock()
	defer c.auth.credMu.Unlock()
	// Checked under the lock that holds the write: a rename landing between
	// the two would leave a default naming an instance that has moved, which
	// the next load refuses while it sits on disk.
	if _, ok := c.reg.Get().Instance(name); !ok {
		return appwire.InvalidParams(fmt.Sprintf("instance %q not found", name))
	}
	l, _, err := c.read()
	if err != nil {
		return err
	}
	l.Default = name
	if err := c.writeLoadable(l); err != nil {
		return err
	}
	return c.reg.Reload()
}
