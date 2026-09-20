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
	"primeradiant.com/evener/internal/credentials"
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
	// beforeCredentialLock, when set, runs after a removal's pre-lock work and
	// just before it takes auth.credMu. A race test uses it as a barrier: the
	// test holds the credential lock, waits for this signal, and only then
	// releases it, so the removal is provably parked at the lock with its
	// pre-lock classification already made instead of the test sleeping and
	// guessing it got there. Nil in production.
	beforeCredentialLock func()
	// applied records the providers.toml writes that landed during the current
	// mutation, so instanceWrite learns a change happened from the write
	// primitive itself (write) rather than from a marker each error path has to
	// remember to attach. Cleared when a mutation takes mu, and again when a
	// rollback puts the previous file back.
	applied appliedWrites
}

func (c *hubInstancesController) read() (*registry.Layer, bool, error) {
	return registry.ReadConfigFile(c.providersConfigPath)
}

func (c *hubInstancesController) write(l *registry.Layer) error {
	err := registry.WriteConfigFile(c.providersConfigPath, l)
	if err == nil {
		// The primitive itself records the write the instant it lands; the
		// mutation's rollback clears it again when it puts the prior file back.
		c.applied.markApplied()
	}
	return err
}

// lockForWrite takes mu exclusively and clears the applied-write record, so
// every mutation's answer to "did this call write?" is its own writes and not
// a previous call's.
func (c *hubInstancesController) lockForWrite() {
	c.mu.Lock()
	c.applied.resetApplied()
}

// captureApplied folds the mutation's write record into the error it is about
// to return, and clears it. It is deferred to run before mu is released, so
// the record it reads is this mutation's own: no other mutation can take mu
// until this one has captured, so the mark can neither be reset nor stolen out
// from under the call that made it. Folding it onto the error (rather than
// leaving the record for the RPC layer to read after the lock is gone) is what
// keeps the applied answer per-call and race-free.
func (c *hubInstancesController) captureApplied(errp *error) {
	wrote := c.applied.takeApplied()
	if *errp != nil && wrote {
		*errp = writeApplied(*errp)
	}
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
	return c.listLocked(key, keyErr)
}

// listLocked builds the listing from state the caller has ALREADY locked, using
// a fingerprint key resolved before that lock. A mutation that captured its
// answer under its own write lock uses this so the answer is the state that
// mutation produced, not a second read a concurrent edit can slip into (see
// Edit).
func (c *hubInstancesController) listLocked(key []byte, keyErr error) appwire.InstanceListResponse {
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
		RenameLeavesRow:     c.renameLeavesRow(r, inst),
		StoredEmail:         status.StoredEmail,
		CredentialRequired:  !keylessScheme(inst.Auth),
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
func (c *hubInstancesController) Create(params appwire.InstanceCreateParams) (err error) {
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
	c.lockForWrite()
	defer c.mu.Unlock()
	defer c.captureApplied(&err)
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
			// The entry this call wrote is still in the file, so the change
			// stands and every other client's list is stale: the write primitive
			// already marked the applied state, so the marker rides the plain
			// error and instanceWrite broadcasts it.
			return fmt.Errorf("%w (and restoring the previous config failed: %w)", err, restoreErr)
		}
		// The rollback put the previous file back, so the write this call made is
		// undone: nothing for other clients to hear about.
		c.applied.resetApplied()
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
	return c.edit(params, nil)
}

// edit applies an edit. When out is non-nil it receives the listing captured
// while this edit's write lock is still held, so the answer describes the state
// this edit produced rather than a later List() a concurrent write can slip
// into; the RPC handler uses it that way for its response.
func (c *hubInstancesController) edit(params appwire.InstanceEditParams, out *appwire.InstanceListResponse) (err error) {
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

	// Resolved before the locks, as Remove resolves its own (see the file's note
	// on resolving the fingerprint key before the locks): resolving can repair
	// the key file - an inter-process lock and a write - so doing it while c.mu
	// and credMu are held would hold every listing and credential op behind it.
	// This edit's own endpoint assertion needs it, and so does the listing the
	// edit captures under its write locks (listLocked), so it is resolved for
	// every caller rather than only the captured-listing one.
	key, keyErr := resolveEndpointFingerprintKey(c.authStateDir())
	c.lockForWrite()
	defer c.mu.Unlock()
	defer c.captureApplied(&err)
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
	// The edit is applied to the row the client listed, so the endpoint that row
	// was served with is asserted before anything is written: a name another
	// client has re-pointed since - or replaced with a different instance - must
	// not have its replacement edited or renamed. Asked here, under the lock
	// that holds the write, so it describes the instance this edit lands on.
	// Empty asserts nothing (the contract InstanceRemoveParams documents), and
	// an edit lands no secret, so it has nothing to fail closed for: a hub whose
	// state root cannot yield the key (the listing then omits every fingerprint,
	// which is why the client sent no assertion) still edits and renames. A
	// caller that DID assert an endpoint is still refused, because the hub
	// cannot say the name resolves where the caller was told it does.
	if params.ExpectedEndpointFingerprint != "" {
		if err := c.auth.verifyEndpointFingerprintWithKey(name, params.ExpectedEndpointFingerprint, key, keyErr); err != nil {
			return err
		}
	}
	newName := strings.TrimSpace(params.NewName)
	renaming := newName != "" && newName != name
	// The intent this rename records, if any: written before the layer is
	// mutated and spent once both halves of the credential move are done with it.
	var renameIntentPath string
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
		// A rename that never finished leaves its record naming THIS name as the one
		// the credential belongs to.
		// Resolve it BEFORE the layer below is mutated: the completion asks whether
		// the config still carries the name, and a layer that already names the new
		// one would read the earlier rename as one that never landed - spending its
		// record without moving the credential. Chaining a second rename onto an
		// unfinished one would strand the older credential under a name nothing
		// reads; what cannot be finished refuses the rename instead.
		{
			if pending := c.finishPendingRename(name, l, nil); len(pending) > 0 {
				return appwire.Conflict(fmt.Sprintf("renaming %q to %q cannot start while an earlier rename into %q is unfinished: %s", name, newName, name, strings.Join(pending, "; ")))
			}
			record, rerr := c.beginRename(name, newName)
			if rerr != nil {
				return rerr
			}
			renameIntentPath = record
		}
		// The map key is the instance name providers.toml is written under,
		// and the default pointer follows so the file still loads. It is set
		// whenever this rename moves the instance an unqualified launch
		// resolves: either the pointer named it, or §5.1 ranking picked it
		// with no pointer at all. Renaming a ranked default without pinning it
		// would hand the next bare launch to whatever instance ranks behind it
		// - a signed-in Codex account loses to a configured Groq key - and the
		// user moved their account, not their default.
		delete(l.Providers, name)
		p.ID = newName
		if l.Default == name || c.launchDefaultIs(name) {
			l.Default = newName
		}
		l.Providers[newName] = p
	} else {
		l.Providers[name] = p
	}
	// writeAndReload restores before when the reload fails (see #711
	// on its comment); a rename continues below on success.
	if err := c.writeAndReload(before, l, name, "edit"); err != nil {
		// A rename whose reload failed and whose rollback write also failed
		// is an applied write: providers.toml carries the new name, so the
		// rename is as persisted as one that ended cleanly and gets the same
		// discriminator (instanceRenameError) the handler maps to
		// ErrorInstanceRenamePersisted. The write primitive marked that applied
		// state on the controller, so the marker is not wrapped onto the error.
		// The record stays: the credential move still has to happen, and startup
		// finishes it from the record. A non-rename edit has no rename to
		// report, so it keeps the error as it came.
		if renaming && c.applied.peekApplied() {
			return renamePersistedError{err}
		}
		// The write did not land (or the config it wrote was put back), so the
		// rename never happened: the record is spent here rather than making every
		// later start judge a mutation that never started. What it cannot spend is
		// reported beside the write failure.
		if renaming {
			return c.abandonIntent(renameIntentPath, err)
		}
		return err
	}
	if renaming {
		moveErr := c.moveCredentials(name, newName, renameIntentPath)
		// The reload above ran while the stored key and OAuth record still
		// sat under the old name, so a curated provider this instance had
		// shadowed could resolve a credential and reappear as a phantom
		// implicit instance — one that also makes renaming back a sticky
		// Conflict. Remove clears credentials before its reload; a rename
		// cannot, because a failed reload restores the file and the
		// credentials would already have moved.
		reloadErr := c.reg.Reload()
		switch {
		case reloadErr == nil:
			if moveErr != nil {
				return moveErr
			}
			// Both halves landed, so this is a clean rename: fall through to the
			// listing captured below rather than returning nil here, so the RPC
			// handler's answer still describes the state this edit produced.
		case moveErr == nil:
			// Everything this rename writes is already written, so it is as
			// persisted as one that ended cleanly and is announced the same
			// way: renamePersistedError is what the RPC handler reads to hand
			// the client the discriminator, and the write primitive's applied
			// mark is what the handler broadcasts on. The record stays: only
			// the credential move below spends it.
			return renamePersistedError{reloadErr}
		default:
			// Both halves failed, and the caller has to hear both: the move
			// report says what credential was left behind, and the reload
			// failure says the hub's own view may still list the old name
			// (and refuse instance writes) until it loads again. The
			// renamePersistedError discriminator comes from the move report
			// (moveCredentials marks its own failure), and the applied marker
			// comes from the providers.toml write this rename landed in
			// writeAndReload — captureApplied folds it onto this error — so
			// folding the reload error around the move report preserves both
			// without losing the move's message or wire class.
			return fmt.Errorf("%w; the registry could not be reloaded either, so it may still list the old name and refuse instance writes until it can be (%w)", moveErr, reloadErr)
		}
	}
	if out != nil {
		// Still under this edit's write locks, so no concurrent edit can land
		// between the write and this read. Reached for a plain edit AND a clean
		// rename (the rename branch above only returns on an applied error).
		*out = c.listLocked(key, keyErr)
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

// removeAppliedError is a removal whose credential deletion reached the store
// (or whose config entry left it) before a later step failed: the instance's
// stored key or OAuth record is gone, so the removal stands even though the
// call returns an error. Every other client's list is stale by exactly as much
// as after a clean removal, so the RPC handler broadcasts on it and still
// returns it, leaving the client that asked with what was left behind to
// reconcile rather than retry against a missing instance.
type removeAppliedError struct{ err error }

func (e removeAppliedError) Error() string { return e.err.Error() }

func (e removeAppliedError) Unwrap() error { return e.err }

// removeApplied wraps err as a standing removal unless it already carries the
// discriminator - restoreFailedRemoval marks its own result, and the config
// rollback path wraps that result again.
func removeApplied(err error) error {
	if _, ok := errors.AsType[removeAppliedError](err); ok {
		return err
	}
	return removeAppliedError{err}
}

// moveCredentials carries an instance's stored key and OAuth record to its
// new name after a rename. It runs once providers.toml is written and
// reloaded, with credMu held by the caller: the config is already renamed, so
// a failure here is reported as what was left behind rather than undone — the
// list stays consistent with the file, and a leftover stays reachable under
// the old name through evener/auth/apiKey/clear or the state directory. That
// report is a renamePersistedError, which is what tells the RPC handler the
// rename is on disk however this call ends (it broadcasts and hands the client
// the discriminator).
//
// intentPath is the rename's intent record, written before providers.toml was
// touched. A rename never lands its record - there is no commit marker for it to
// write, because whether the rename happened is read from the config, which is
// durable before any credential moves - so the record is spent once BOTH halves
// of the move have landed: nothing is left at the old canonical record path, no
// copy is still filed under the old name, and the stored key is filed under the
// new one. While any half is unfinished the
// record stays, and startup finishes the move from it
// (restoreUncommittedOAuthAsides, completeRenameIntent) - which is what keeps a
// copy the carry promoted onto the OLD canonical record path from being stranded
// there once providers.toml names only the new instance.
//
// Nothing it calls takes credMu, which the caller still holds.
func (c *hubInstancesController) moveCredentials(oldName, newName, intentPath string) error {
	var problems []string
	// The rename carries no commit marker: its landing is read from the config,
	// which is durable evidence written before any credential moved
	// (completeRenameIntent), so the record stays under its in-flight name until
	// the whole move is done with it.
	// One persist, so the key is never briefly filed under both names or
	// neither: a copy-then-clear pair whose second half failed would leave
	// the old name resolving a credential the config no longer names.
	if err := c.auth.creds.Move(oldName, newName); err != nil {
		problems = append(problems, fmt.Sprintf("stored key not copied: %v", err))
	}
	// A failed removal can leave the instance's only record as a parked copy
	// under the old name (restoreFailedRemoval's rename-back failed).
	// providers.toml now names only the new instance, so startup recovery would
	// never look under the old name again and that credential would be stranded
	// unrecoverably. Promote the newest copy to the record path when it is free
	// - the read below then carries it and the renamed instance has a usable
	// credential at once - and carry every other copy to the new name's parked
	// name, stamp preserved, so no copy is left filed under the old name while
	// startup recovery and the reclaim still parse what remains.
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
		// credential. Move those bytes back under a parked copy for the NEW name
		// (stamp preserved), the way every other carry problem is reported: the
		// rename stands and the caller is told what was left reachable.
		if promoted != "" {
			// File the promoted bytes under a parked copy for the NEW name, stamp
			// preserved. The destination is chosen the same collision-safe way the
			// carry above chooses one (freeAsideName steps past a taken stamp) and
			// moved without replacing anything (renameNoReplace): a file already at
			// the deterministic name is another credential's bytes and must survive.
			_, promotedStampText, _ := parseOAuthAside(promoted)
			var want int64
			if stamp, perr := strconv.ParseInt(promotedStampText, 10, 64); perr == nil {
				want = stamp
			}
			recordPath := authopenai.AuthFilePath(c.auth.stateDir, oldName)
			dst, ok := freeAsideName(filepath.Dir(recordPath), newName, want)
			switch {
			case !ok:
				problems = append(problems, fmt.Sprintf("OAuth record not read (%v), and no fresh parked name under %q was free to file the copy %q it carried, so those bytes are still at %q", err, newName, promoted, recordPath))
			default:
				if rerr := renameNoReplace(recordPath, dst); rerr != nil {
					problems = append(problems, fmt.Sprintf("OAuth record not read (%v), and the copy %q it carried could not be filed as the parked copy %q (%v)", err, promoted, filepath.Base(dst), rerr))
				} else {
					problems = append(problems, fmt.Sprintf("OAuth record not read (%v), so the copy %q it carried was filed as the parked copy %q", err, promoted, filepath.Base(dst)))
				}
			}
		} else {
			// Nothing was promoted to the record path, so the file sitting at the
			// OLD record path is what is left of the credential - and it is
			// unreadable, which is why this branch is the one running. Leaving it
			// there strands it: providers.toml now names only the new instance, no
			// recovery pass recognizes a plain record as a copy, and the sweep
			// reads no other name. File it under the NEW name as a parked copy -
			// the shape startup recovery restores - chosen collision-safely and
			// landed without replacing anything (freeAsideName, which ranks it
			// after whatever the new name already holds, then renameNoReplace).
			// Only a regular file is a record this may move: a directory (or a
			// socket, or a device) at that path was not written as one, and moving
			// it into a copy's name would take it where the copy rules skip
			// directories.
			recordPath := authopenai.AuthFilePath(c.auth.stateDir, oldName)
			if info, statErr := os.Lstat(recordPath); statErr == nil && !info.Mode().IsRegular() {
				problems = append(problems, fmt.Sprintf("OAuth record not read (%v), and %s is not a regular file (%v), so it was left where it is", err, recordPath, info.Mode()))
				break
			}
			dst, ok := freeAsideName(filepath.Dir(recordPath), newName, 0)
			switch {
			case !ok:
				problems = append(problems, fmt.Sprintf("OAuth record not read (%v), and no fresh parked name under %q was free to file the record %q under, so those bytes are still at %q", err, newName, recordPath, recordPath))
			default:
				if rerr := renameNoReplace(recordPath, dst); rerr != nil {
					problems = append(problems, fmt.Sprintf("OAuth record not read (%v), and the record %q could not be filed as the parked copy %q (%v)", err, recordPath, filepath.Base(dst), rerr))
				} else {
					problems = append(problems, fmt.Sprintf("OAuth record not read (%v), so the record %q was filed as the parked copy %q", err, recordPath, filepath.Base(dst)))
				}
			}
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
	// The record is spent once BOTH halves landed: nothing is at the old
	// canonical record path, no copy is still filed under the old name, and the
	// stored key is filed under the new one. While any part is unfinished the
	// record stays, and the next start finishes the move from it; a record that
	// will not come away is spent by that same start (the completion is
	// idempotent and finds nothing left to do), not a reason to fail a rename
	// whose durable work landed.
	if intentPath != "" {
		_, stillFiled := c.auth.creds.Get(oldName)
		_, statErr := os.Lstat(authopenai.AuthFilePath(c.auth.stateDir, oldName))
		leftBehind, lsErr := c.parkedCopies(oldName)
		switch {
		case lsErr != nil:
			problems = append(problems, fmt.Sprintf("list %q's parked copies to decide whether the rename is finished (%v)", oldName, lsErr))
		case errors.Is(statErr, os.ErrNotExist) && !stillFiled && len(leftBehind) == 0:
			_ = removeOAuthIntent(intentPath)
		}
	}
	if len(problems) > 0 {
		// The rename reached the file, so it is as persisted as one that ended
		// cleanly: renamePersistedError is what the RPC handler reads to hand
		// the client the discriminator, and the config write the rename landed
		// marked the applied state the handler broadcasts on.
		return renamePersistedError{fmt.Errorf("renamed %q to %q, but: %s", oldName, newName, strings.Join(problems, "; "))}
	}
	return nil
}

// beginRemoval writes a removal's intent record before the first byte of any
// copy moves. It is written whenever a stored credential may be present, which
// is not only the auth directory: a stored key lives in credentials.toml, so a
// key-only removal creates the directory rather than skipping its record - the
// record is where the key it clears is staged, and a removal that cleared a key
// with nothing durable left of it would lose the user's only credential to a
// crash (beginRename creates it for the same reason).
func (c *hubInstancesController) beginRemoval(name string, configBacked bool) (string, error) {
	path, err := c.writeIntent(removalIntent(name, configBacked), true)
	if err != nil {
		return "", fmt.Errorf("remove %s: record the removal for startup recovery: %w", name, err)
	}
	return path, nil
}

// beginRename writes a rename's intent record before providers.toml is touched.
// It is written whenever a STORED CREDENTIAL may be present, and a stored key
// lives in credentials.toml - not in the auth directory - so a rename of a
// key-only instance creates the directory rather than skipping its record. The
// record names the OLD instance, which is the name every credential the rename
// moves is filed under.
func (c *hubInstancesController) beginRename(oldName, newName string) (string, error) {
	path, err := c.writeIntent(renameIntent(oldName, newName), true)
	if err != nil {
		return "", appwire.Conflict(fmt.Sprintf("renaming %q to %q cannot be recorded for startup recovery: %v", oldName, newName, err))
	}
	return path, nil
}

// abandonIntent removes a mutation's intent record when the mutation was refused
// before it changed anything: the record exists to describe work that started,
// and one whose work never started is not something a later pass should act on.
// A record that will not come away is reported beside the refusal rather than
// swallowed.
func (c *hubInstancesController) abandonIntent(path string, cause error) error {
	// The record and everything staged with it go together: a staging left beside
	// no record is debris nothing reads (the store's key is still there, which is
	// why the record is being abandoned at all).
	if serr := spendStagedRemovalKey(path); serr != nil {
		cause = fmt.Errorf("%w; the stored key staged beside its record could not be spent (%w)", cause, serr)
	}
	if path == "" {
		return cause
	}
	if err := removeOAuthIntent(path); err != nil {
		return fmt.Errorf("%w; the intent record %s the mutation had written could not be removed either (%w)", cause, path, err)
	}
	return cause
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
// The Codex transport needs no case of its own here: the source check already
// places it with the user. The registry resolves a Codex instance from its
// OAuth record alone, to the "oauth" source when that record is readable and
// "none" when it is absent or corrupt (credential, llm/registry/instances.go),
// and neither is on the allow-list below; the hub's own status reports the same
// two values for it (openAIInstanceStatus), so a Codex row whose status
// resolved no usable source is still the user's to remove. The client's other
// early return - true when the row needs no credential - is already the
// keylessScheme branch: the wire's CredentialRequired is exactly `Auth !=
// AuthNone && Auth != AuthOptionalBearer` (List, app_instances.go), so
// `!credentialRequired` is keylessScheme, and keylessScheme returns true below.
//
// The source check is an ALLOW-list, not a deny-list: only env:<VAR> and adc
// name a credential the host supplies. Any other source - none, empty, or a
// future one this vocabulary does not know - is not the environment's, so an
// implicit credential-required instance resolving none (a bearer row with no
// key) stays the user's to remove rather than being badged "from environment"
// and refused with nothing to say. Mirrors the client's
// `activeSource.startsWith("env:") || activeSource === "adc"` in
// appwire-client/typescript/credentialLabels.ts, verified against this file's
// List entry.
func environmentBacked(inst registry.Instance) bool {
	if !inst.Implicit {
		return false
	}
	if keylessScheme(inst.Auth) {
		return true
	}
	return strings.HasPrefix(inst.CredentialSource, "env:") || inst.CredentialSource == "adc"
}

// renameLeavesRow reports whether renaming inst leaves an instance resolving
// under its old name, because the environment re-supplies what the rename
// moves. Two things can hold the old name up: the instance is already
// environment-backed (environmentBacked - the environment is what supplies it
// now, so moving the user's layers changes nothing), or the old name is a
// curated provider id that re-derives an instance from the layers a rename
// cannot move (registry.ProviderRenameLeavesInstance). The rename note reads
// the wire bit this computes; a client cannot answer it because it cannot see
// ADC availability or the curated set.
func (c *hubInstancesController) renameLeavesRow(r *registry.Registry, inst registry.Instance) bool {
	if environmentBacked(inst) {
		return true
	}
	if r == nil {
		return false
	}
	return r.ProviderRenameLeavesInstance(inst.Name)
}

// launchDefaultIs reports whether an unqualified launch currently resolves the
// named instance: providers.toml's `default` names it, or §5.1 ranking picks it
// when the file names none. It asks registry.DefaultInstance - the same answer
// llm/client's launch path reads - rather than re-deriving the ranking, so the
// rename's default-preserving pointer and the launch cannot disagree.
func (c *hubInstancesController) launchDefaultIs(name string) bool {
	def, _, err := c.reg.Get().DefaultInstance()
	return err == nil && def == name
}

// Remove deletes an instance, its stored key and its OAuth record. An instance
// that exists from the environment has no entry to delete and would come
// straight back, so it is refused with a message saying what to unset instead
// (spec §5.1); one the user credentialed through the UI has no entry either,
// and there the credential cleanup is the removal (environmentBacked).
// Refusals that blame the name the caller sent - a name that resolves to no
// instance or an environment-only one that cannot be deleted - follow Create
// and Edit's convention (#717/#748): appwire.InvalidParams, not a generic wire
// error.
func (c *hubInstancesController) Remove(params appwire.InstanceRemoveParams) (err error) {
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

	c.lockForWrite()
	defer c.mu.Unlock()
	defer c.captureApplied(&err)
	// The lookup names what this call deletes - the authored entry, the
	// stored key and the OAuth record under this name - so it is made under
	// the lock that holds the deletion, as Edit's are: a rename landing
	// between the two would hand the deletion to whatever holds the name
	// afterwards.
	// Only existence is asked here, under c.mu alone. Whether the row is the
	// user's to remove is decided by the credential source it resolves, and a
	// credential write changes that source while holding credMu alone - so the
	// classification is asked under BOTH locks, below (hubAuthController.credMu).
	// Refusing it here on the source this call can read without credMu would
	// refuse a row a writer has just made the user's, which is the finding the
	// locked re-check below exists for.
	if _, ok := c.reg.Get().Instance(name); !ok {
		return appwire.InvalidParams(fmt.Sprintf("instance %q not found", name))
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
	if c.beforeCredentialLock != nil {
		c.beforeCredentialLock()
	}
	c.auth.credMu.Lock()
	defer c.auth.credMu.Unlock()

	// The classification above predates the credential lock, and a credential
	// writer that held that lock first can change what the instance resolves:
	// clearing a stored key on an instance whose provider has an environment
	// fallback turns a row the user owns into one the environment supplies. So
	// the question is re-asked here, under the lock no writer can hold beside
	// this call - existence first, because a rename landing between the two
	// would have taken the name away entirely.
	locked, ok := c.reg.Get().Instance(name)
	if !ok {
		return appwire.InvalidParams(fmt.Sprintf("instance %q not found", name))
	}
	if environmentBacked(locked) {
		// The refusal Remove's doc comment promises for an environment-only row:
		// appwire.InvalidParams, with the remedy describeImplicit and
		// removalRemedy build. It is asked HERE, before either parse of
		// providers.toml, so a config the hub cannot read (an old-schema file,
		// say) cannot preempt the documented refusal with a read error and take
		// the remedy away from the caller - and under the credential lock, so a
		// writer that has just made the row the user's is not refused on the
		// stale source an unlocked read would have seen.
		return appwire.InvalidParams(fmt.Sprintf("%s exists from the environment (%s); %s", name, describeImplicit(locked), removalRemedy(locked)))
	}

	// Read the authored layer before anything is deleted, and only now: this is
	// a pure read, so a failure here leaves nothing to undo, and it runs under
	// both locks, so the layer it returns is still the one this removal edits.
	// before is an independent parse of the same file - a fresh read sharing no
	// maps with l - so the reload rollback below writes back exactly what was on
	// disk before this call (Edit's own rollback input).
	before, _, err := c.read()
	if err != nil {
		return err
	}
	l, _, err := c.read()
	if err != nil {
		return err
	}

	// A rename ONTO this name that never finished still has its credential filed
	// under the name it came from, because the config is written before any
	// credential moves. The removal below takes away the entry that credential is
	// being carried to, and the next start would then read the rename as one that
	// never landed - the config it asks no longer carries the new name - and spend
	// its record without moving anything, leaving the credential stranded under a
	// name neither the config nor any recovery pass reads. The rename path resolves
	// an unfinished rename into the name it is about to take (finishPendingRename)
	// for the same reason; a removal takes that name away, so it resolves it too,
	// and refuses rather than deleting the name a credential is still being
	// carried to. Resolution runs before anything here is mutated, and it changes
	// no config: the rename landed in providers.toml already, or its completion
	// would have been refused.
	if pending := c.finishPendingRename(name, l, nil); len(pending) > 0 {
		return appwire.Conflict(fmt.Sprintf("removing %q cannot start while a rename into it is unfinished: %s", name, strings.Join(pending, "; ")))
	}

	// The confirmation this removal carries names the row the client listed, so
	// a name another client has re-pointed since (a removal and a recreation
	// under it, or an edit to its base_url) is refused rather than having its
	// replacement instance removed. Asked here, under the exclusive lock, so it
	// describes the instance the cleanup below acts on.
	if err := c.auth.verifyEndpointFingerprintWithKey(name, params.ExpectedEndpointFingerprint, key, keyErr); err != nil {
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
	// The removal's KIND and its decision to write providers.toml are ONE
	// answer, read from ONE parse: l, the layer this removal mutates and writes.
	// `before` is the pristine parse the reload rollback writes back, and
	// providers.toml is user-editable, so an out-of-process save between the two
	// reads can make them disagree. A kind read from `before` while the write
	// was decided from `l` would be exactly the resurrection the kind exists to
	// prevent: a kind of credential-only, written after a config write this call
	// really made, would leave a removal whose record says the config cannot
	// judge it - and recovery puts a started credential-only removal's copy back
	// (restoreUncommittedOAuthAsides).
	//
	// An authored [providers.<name>] entry or a `default` pointer naming this
	// instance means the removal drops something from providers.toml, so it
	// writes the file and the record's kind is config-backed; anything else is
	// credential-only and writes nothing.
	//
	// Whether the config authored this instance is asked of the same parse, for
	// the same reason: it decides which frame a failed restore reports, and it
	// has to describe the layer whose view this call acted on.
	_, authored := l.Providers[name]
	configBacked := configCarriesName(l, name)
	// The write decision is that same answer, taken once: this removal rewrites
	// providers.toml exactly when the parse it writes carried the name.
	configChanged := configBacked
	storedKey, hasStoredKey := c.auth.creds.Get(name)
	// The removal records its intent BEFORE the first byte of any copy moves, so a
	// hub that dies anywhere from here on leaves a record that says what this
	// mutation was doing and what evidence decides its outcome. The kind it
	// carries is that evidence: providers.toml for a removal that changed it, the
	// record file itself for one that did not.
	intentPath, err := c.beginRemoval(name, configBacked)
	if err != nil {
		return err
	}
	// The stored key is cleared before the config write commits, and the record
	// alone cannot bring it back: stage the bytes beside the record, so a crash in
	// that window leaves the next start both the evidence and the bytes.
	if err := stageRemovalKey(intentPath, storedKey, hasStoredKey); err != nil {
		return c.abandonIntent(intentPath, fmt.Errorf("remove %s: stage its stored key for startup recovery: %w", name, err))
	}
	oauthAside, err := c.setAsideOAuthFile(name)
	if err != nil {
		// Nothing has been deleted yet, so the removal has nothing to recover:
		// the record it just wrote must not outlive the refusal.
		//
		// A park that failed AFTER its rename landed hands back the name the copy
		// was parked under, and those bytes go back before anything is abandoned:
		// a credential-only instance has no config entry to carry it, so a parked
		// copy whose record is gone is debris the next start deletes - a refused
		// removal destroying the credential it refused to remove. A copy that
		// cannot be put back keeps the record instead, so the pass that can
		// resolve it finds both it and the bytes staged beside it.
		if oauthAside != "" {
			if backErr := c.putParkedOAuthRecordBack(name, oauthAside); backErr != nil {
				return fmt.Errorf("%w; the instance's OAuth record is parked under %s and could not be put back (%w), so the record %s was kept for startup recovery", err, oauthAside, backErr, intentPath)
			}
		}
		return c.abandonIntent(intentPath, err)
	}

	removed, err := c.removeCredentials(name)
	if err != nil {
		// The cleanup never reached the config, so a clean restore leaves
		// nothing changed; a credential that stays deleted is still a change
		// every other client's status for this name is stale against, and the
		// hub owes them the broadcast even though the entry itself never
		// moved - restoreFailedRemoval wraps its own result in that case.
		//
		// An authored entry never moved, so the instance resolves from the config
		// whatever happens to its credentials, and the configured frame with
		// supplyAny's strict question is the honest report. An implicit instance
		// has no entry to fall back on - the layer supplyOf names is what carries
		// it - so when the cleanup deleted that layer and the restore cannot put
		// it back, the instance no longer resolves: the removal stands, the frame
		// must say so, and the discriminator goes with it, because
		// instanceRemoveError reads it to tell the client the removal applied
		// rather than leaving it to retry a removal that stands. supplyConfig is
		// the provider carrying the row, with no credential of the user's at
		// stake, so it keeps the configured frame.
		frame, supplies := "the instance is still configured", supplyAny
		if !authored {
			supplies = supplyOf(locked)
			if supplies != supplyConfig {
				frame = "the removal stands"
			}
		}
		_, restoreErr := c.restoreFailedRemoval(name, storedKey, hasStoredKey && removed.storedKey, oauthAside, err, frame, supplies)
		// The record this call wrote is settled with the restore: a copy it
		// could not put back keeps it at the started phase, which is what tells
		// the next start the credential is still the instance's.
		if more := c.resolveRemovalIntent(intentPath, name); len(more) > 0 {
			return fmt.Errorf("%w; %s", restoreErr, strings.Join(more, " and "))
		}
		return restoreErr
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
	// configChanged was taken above, from the same parse as the kind.
	if configChanged {
		if err := c.writeLoadable(l); err != nil {
			// The config write never landed, so a clean restore leaves
			// nothing changed; a credential that stays deleted is still a
			// change every other client's status for this name is stale
			// against - restoreFailedRemoval wraps its own result in that
			// case.
			//
			// Which layer the restore has to put back - and so what a failed
			// restore means - depends on what was in the file before this
			// write. An authored entry never moved: the write that would have
			// deleted it failed, so the instance resolves from the config
			// whatever happens to its credentials, and the configured frame
			// with supplyAny's strict question is the honest report. An
			// implicit instance has no entry to fall back on, and the change
			// this write failed to make - dropping the `default` pointer - is
			// not a layer it resolves from: the layer supplyOf names is. When
			// that layer is a credential this call deleted, a restore that
			// cannot put it back leaves the instance unresolvable, so the
			// removal stands and the frame has to say so; the discriminator
			// goes with it, because instanceRemoveError reads it to tell the
			// client the removal applied instead of leaving it to retry a
			// removal that stands. supplyConfig is the provider carrying the
			// row, with no credential of the user's at stake, so it keeps the
			// configured frame.
			frame, supplies := "the instance is still configured", supplyAny
			if !authored {
				supplies = supplyOf(locked)
				if supplies != supplyConfig {
					frame = "the removal stands"
				}
			}
			restored, restoreErr := c.restoreFailedRemoval(name, storedKey, hasStoredKey, oauthAside, err, frame, supplies)
			if more := c.resolveRemovalIntent(intentPath, name); len(more) > 0 {
				restoreErr = fmt.Errorf("%w; %s", restoreErr, strings.Join(more, " and "))
			}
			if restored {
				// The carrying layer is back, so the removal did not stand, and
				// restoreFailedRemoval answers such a rollback with the
				// leftovers when a stray other layer stayed deleted. The write
				// failure and the applied mark - that deletion is what every
				// other client's status for this name is stale against - are
				// folded in here, the way the reload rollback below folds the
				// same leftovers into its own report.
				if leftovers, ok := errors.AsType[removalLeftoversError](restoreErr); ok {
					return fmt.Errorf("%w; some credentials were not put back: %w", err, leftovers)
				}
			}
			return restoreErr
		}
	}
	// The removal's durable work has landed: the record is set aside, the
	// credential layers are deleted and, when it changed, providers.toml is
	// written. The commit point is the record's phase, rewritten now, before the
	// reload: a hub that dies anywhere from here through the reclaim below leaves
	// a landed record, and startup sweeps the copies such a removal left instead
	// of putting them back (restoreUncommittedOAuthAsides) - a copy restored here
	// would resurrect the removal the user just carried out. The phase is written
	// here rather than left to the reclaim because the reclaim runs only after the
	// reload, so the whole reload would sit inside the window a crash turns back
	// into an in-doubt removal.
	//
	// A phase that cannot be written is itself a failed removal, not something to
	// leave to the reclaim: a removal reported as standing must not leave a record
	// startup would read as one that did not stand. It rolls the removal back
	// through the same path a failed reload takes, and the caller is told the
	// removal failed rather than that it stood.
	// Which layer a failed restore has to put back - and so what a failed
	// restore means - depends on what carried the instance when this removal
	// classified it, under both locks. An authored entry resolves from the
	// config, so the configured frame with supplyAny's strict question is the
	// honest report. An implicit instance has no entry to fall back on: the layer
	// supplyOf names is what carries it, and a restore that cannot put that layer
	// back leaves the instance unresolvable, so the removal stands and
	// restoreFailedRemoval binds the discriminator to that answer. When nothing
	// was written there is no config to fall back on at all, so the question is
	// always the carrying layer's.
	frame, supplies := "the instance is still configured", supplyAny
	if !authored {
		supplies = supplyOf(locked)
		if supplies != supplyConfig {
			frame = "the removal stands"
		}
	}
	if !configChanged {
		frame, supplies = "the removal stands", supplyOf(locked)
	}
	landedPath, err := landOAuthIntent(intentPath)
	if err != nil {
		// The commit point is the rename that files the record as landed: a record
		// that cannot be carried there leaves the removal in doubt - the copy it
		// parked would be put back by the next start - so it rolls back here,
		// through the same path a failed reload takes, and never reports the
		// removal as standing. A rename that LANDED and then failed its directory
		// sync is the one case where the record is not where this call left it:
		// the helper reports the name it now holds, and the rollback has to
		// un-land THAT name - un-landing the in-flight one no-ops on a file that
		// is no longer there, and a rolled-back removal would leave a landed
		// record startup reads as the removal having stood.
		return c.rollBackFailedRemoval(before, name, storedKey, hasStoredKey, landedPath, oauthAside, configChanged, frame, supplies, err)
	}
	intentPath = landedPath
	if err := c.reg.Reload(); err != nil {
		// The raw error, not a framed one: rollBackFailedRemoval adds the single
		// "removing %q failed:" frame itself, so wrapping it here would report the
		// same sentence twice.
		return c.rollBackFailedRemoval(before, name, storedKey, hasStoredKey, intentPath, oauthAside, configChanged, frame, supplies, err)
	}
	// The removal stands, so every copy of this name's record is unwanted now:
	// the one this call set aside and any an earlier removal of the name left
	// behind. The copies are the user's credentials under a name no reader looks
	// at, so a failure to delete one is reported even though the removal stands
	// - a caller told the removal succeeded has no reason to look for what it
	// left behind. The discriminator is what tells the RPC handler to announce
	// the removal and leaves the caller the files the failure names; nothing is
	// rolled back for it, because the config, the registry and the credential
	// the instance resolved are all in their post-removal state.
	if err := c.reclaimOAuthAsides(name); err != nil {
		// The intent stays: the copies this call parked are still on disk, and the
		// record is what says the removal STOOD - a later start sweeps them and
		// reports the credential-only ones rather than putting them back. The
		// removal stood, so the applied mark the write and the deletions set stays
		// on the controller: instanceWrite announces the change.
		return removeApplied(fmt.Errorf("removed %s, but %w", name, err))
	}
	// The removal stood and its copies are gone, so the record has nothing left
	// to describe - and the stored key it staged is gone with the removal it
	// described, so the staging goes now rather than waiting for a start that
	// would read a landed record and spend it there. A record that will not come
	// away is debris the next start removes (an intent whose name holds no copies
	// is spent by the sweep), not a reason to fail a removal whose durable work
	// landed.
	_ = spendStagedRemovalKey(intentPath)
	_ = removeOAuthIntent(intentPath)
	return nil
}

// removalSupply names the layer an instance resolves from, so a failed
// removal's rollback can decide from what actually carries the instance rather
// than from every deleted file coming back. supplyAny is the older, stricter
// question the config-backed call sites ask - did every layer this call deleted
// come back? - because there the authored config entry, not a credential file,
// is what the instance resolves from.
type removalSupply uint8

const (
	supplyAny removalSupply = iota
	// supplyConfig: the authored entry or the provider itself carries it.
	supplyConfig
	supplyStoredKey
	supplyOAuth
)

// supplyOf reports which removed credential layer carries inst: the OAuth
// record for the Codex scheme, the stored key for the "store" source, and
// neither when the authored entry or the provider itself is what holds the
// instance up. It mirrors credential()'s precedence - the source it reports is
// the one that actually won.
func supplyOf(inst registry.Instance) removalSupply {
	switch inst.CredentialSource {
	case "oauth":
		return supplyOAuth
	case "store":
		return supplyStoredKey
	}
	return supplyConfig
}

// restoreFailedRemoval puts back what the cleanup deleted after a failed
// removal, and reports whether the layer that carries the instance is back:
// supplies names that layer, so a restore that leaves the instance configured
// answers ok even when a stray other layer could not be put back (a stored key
// left over beside a Codex OAuth record, or the reverse). supplyAny keeps the
// stricter rule and answers ok only on a complete restore.
//
// cause is the failure that triggered the rollback. On a restore that carries
// the instance (ok true) it comes back unchanged, unless a stray layer could
// not be put back - then the returned error names only those leftover layers,
// which the caller folds into its own rollback report: a deleted credential
// that stays deleted is a change other clients are stale against even though
// the instance itself is carried again, and the deletion's applied mark (set
// by removeCredentials) keeps announcing it. On a restore that does not carry
// the instance, the returned error folds cause, frame, and the layers that
// could not be put back. When the failed restore took the layer that carries
// the instance - a stored key or OAuth record
// (supplyStoredKey/supplyOAuth) - it also carries the standing-removal
// discriminator, because the instance no longer resolves and clients must
// reconcile the removal. supplyAny/supplyConfig are the config-backed
// question: the authored entry or its provider still carries the instance, so
// the removal did not stand (the caller that knows the config IS gone marks
// removeApplied itself). frame names the state - whether the entry is still
// authored or the removal stood - so the message reads as correct English for
// the failure that produced it and never contradicts the wire class. Its
// callers pass only the layers the failure actually deleted, so this never
// rewrites - and never reports a failure to rewrite - a credential that is
// still where it was. A restore that puts every deleted layer back clears the
// applied mark, because the removal is then undone.
func (c *hubInstancesController) restoreFailedRemoval(name, storedKey string, hasStoredKey bool, oauthAside string, cause error, frame string, supplies removalSupply) (bool, error) {
	var problems []string
	storedKeyRestored := true
	if hasStoredKey {
		if err := c.auth.setCredential(name, storedKey); err != nil {
			storedKeyRestored = false
			problems = append(problems, fmt.Sprintf("its stored key could not be restored (%v)", err))
		}
	}
	oauthRestored := true
	if oauthAside != "" {
		// Moved back rather than rewritten. setAsideOAuthFile renamed the record
		// away without reading it, so this restores a record the hub cannot read
		// as faithfully as one it can, and a rename cannot leave the half-written
		// file a rewrite could. renameNoReplace, like every other payload move: a
		// file that appeared at the record path is another credential's bytes, and
		// rename(2) would silently replace them. The parked copy is the shape
		// startup recovery reads, so a refused move leaves the bytes recoverable.
		if err := renameNoReplace(oauthAside, authopenai.AuthFilePath(c.auth.stateDir, name)); err != nil {
			oauthRestored = false
			problems = append(problems, fmt.Sprintf("its OAuth record could not be restored (%v)", err))
		}
	}
	var carried bool
	switch supplies {
	case supplyStoredKey:
		carried = storedKeyRestored
	case supplyOAuth:
		carried = oauthRestored
	default:
		carried = len(problems) == 0
	}
	if !carried {
		standing := fmt.Errorf("%w; %s, but %s", cause, frame, strings.Join(problems, " and "))
		if supplies == supplyStoredKey || supplies == supplyOAuth {
			return false, removeApplied(standing)
		}
		return false, standing
	}
	if len(problems) > 0 {
		// The instance is carried again, but a stray layer stayed deleted: a
		// plain rollback, so the caller learns what is missing while the
		// credential-deletion mark removeCredentials set keeps the change
		// announced.
		return true, removalLeftoversError{strings.Join(problems, " and ")}
	}
	// Every layer this call deleted is back, and so is the one that carries the
	// instance: the removal is undone, so there is nothing to announce.
	c.applied.resetApplied()
	return true, cause
}

// removalLeftoversError is the error restoreFailedRemoval returns when the
// layer that carries the instance is back but a stray other layer could not be
// put back. The caller folds it into its own rollback report: the removal
// rolled back, but a credential is still gone, and the credential deletion's
// applied mark is what makes every other client hear about it.
type removalLeftoversError struct{ problems string }

func (e removalLeftoversError) Error() string { return e.problems }

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
// It reports which layers it actually deleted even when it fails, because its
// caller restores exactly those: Store.Clear puts its own entry back when the
// persist fails (nothing deleted), while a failed DeleteAuth leaves its file
// in place - rewriting either would be a false alarm on a disk that is already
// refusing writes.
func (c *hubInstancesController) removeCredentials(name string) (deletedCredentials, error) {
	var deleted deletedCredentials
	if _, stored := c.auth.creds.Get(name); stored {
		if err := c.auth.clearCredential(name); err != nil {
			return deleted, fmt.Errorf("remove %s: clear stored credential: %w", name, err)
		}
		deleted.storedKey = true
		// The primitive that removed a layer records the change the instance
		// mutation stands for; a full restore below clears it again.
		c.applied.markApplied()
	}
	removedRecord, err := c.auth.deleteAuth(c.auth.stateDir, name)
	if err != nil {
		return deleted, fmt.Errorf("remove %s: delete OAuth state: %w", name, err)
	}
	deleted.oauthRecordDeleted = removedRecord
	if removedRecord {
		c.applied.markApplied()
	}
	return deleted, nil
}

// deletedCredentials reports which credential layers a removal's cleanup
// actually deleted. It is a REPORT of what the cleanup found, not the restore
// decision: a removal moves its record aside first (setAsideOAuthFile), so by
// the time this cleanup runs there is nothing at the record path for deleteAuth
// to delete and oauthRecordDeleted is false for every removal that went through
// Remove. What a failed removal puts back is that copy -
// restoreFailedRemoval's oauthAside - never this flag.
// Do not gate a restore on it: the old gate's shape (hasOAuth &&
// removed.oauthRecord) would skip the rename-back and lose the record.
type deletedCredentials struct {
	storedKey bool
	// oauthRecordDeleted reports whether the cleanup found and deleted a record
	// at the record path. False for a removal, whose record was set aside first;
	// true only for a caller that invokes the cleanup with the record still in
	// place.
	oauthRecordDeleted bool
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
	default:
		return "credential source " + src
	}
}

// removalRemedy names the action that really takes an environment-backed
// instance away, keyed on what makes it exist. The refusal beside describeImplicit
// reads it, so a remedy has to name something that exists for this instance and
// would take the row with it: the variable a credential-required scheme reads,
// the ADC credentials the host supplies, or - for a keyless scheme - the
// provider endpoint the row is derived from, since a stored credential such a
// row holds is the user's to clear but does not take the row away.
func removalRemedy(inst registry.Instance) string {
	// A keyless scheme comes first: it resolves without any credential, so the
	// registry derives its instance whether or not a variable is set
	// (computeInstances). Unsetting the variable an active env source names
	// would only drop the optional key and leave the row - a keyless gateway
	// reading OLLAMA_API_KEY keeps coming back from its provider endpoint - so
	// the endpoint is what the remedy has to name instead.
	if keylessScheme(inst.Auth) {
		if strings.HasPrefix(inst.CredentialSource, "env:") {
			return "the provider endpoint is what keeps it, so remove or disable that endpoint instead"
		}
		if inst.CredentialSource == "store" {
			// The stored credential is the user's to clear, but it is not what
			// removes the row: keylessScheme makes the registry re-derive the
			// instance from its provider endpoint, so clearing the credential
			// leaves the row and reads as the advised action having failed. The
			// endpoint is the remedy, exactly as in the env: branch above.
			return "the provider endpoint is what keeps it, so remove or disable that endpoint instead (clearing the stored credential does not remove the row)"
		}
		return "it comes back with its provider and holds no credential of its own to clear"
	}
	// For a credential-required scheme the variable comes first: it is what
	// supplies the credential the removal cannot take away, and unsetting it
	// takes the instance with it.
	if varName, ok := strings.CutPrefix(inst.CredentialSource, "env:"); ok {
		return fmt.Sprintf("unset %s instead", varName)
	}
	// No source at all - a credential-required instance the registry derives
	// from its provider alone - holds no credential this removal could take
	// away, so naming one to remove would send the caller after something that
	// does not exist.
	if inst.CredentialSource == "none" || inst.CredentialSource == "" {
		return "it comes back with its provider and holds no credential of its own to clear"
	}
	if inst.CredentialSource == "adc" {
		return "remove the application-default credentials this host supplies instead"
	}
	return "remove the credential that supplies it instead"
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

// RefreshModels fetches one instance's live listing into the held registry
// and reports only the fetch error; the caller's instanceWrite wrapper
// produces the updated list. It is a read: no file is written, so it stays
// available while writes are refused. A failed fetch is an error, not a
// catalog-only list — the sheet keeps its catalog rows and toasts
// the failure.
func (c *hubInstancesController) RefreshModels(ctx context.Context, params appwire.InstanceRefreshModelsParams) error {
	reg := c.reg.Get()
	if reg == nil {
		return errors.New("providers.toml cannot be read: the provider registry has not loaded")
	}
	name := strings.TrimSpace(params.Name)
	if _, ok := reg.Instance(name); !ok {
		return appwire.InvalidParams(fmt.Sprintf("instance %q not found", name))
	}
	return fetchInstanceLive(ctx, c.reg, name)
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
			// The layer this call wrote is still in the file, like Create's
			// failed rollback: the write primitive set the applied mark, so
			// instanceWrite broadcasts the plain error's change.
			return fmt.Errorf("%w (and restoring the previous config failed: %w)", err, restoreErr)
		}
		// The rollback put the previous file back, so the write is undone.
		c.applied.resetApplied()
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
func (c *hubInstancesController) SetModelDisabled(params appwire.InstanceSetModelDisabledParams) (err error) {
	if err := c.refuseWhenBroken(); err != nil {
		return err
	}
	if err := c.requireAuth(); err != nil {
		return err
	}
	name := strings.TrimSpace(params.Name)
	model := strings.TrimSpace(params.Model)

	c.lockForWrite()
	defer c.mu.Unlock()
	defer c.captureApplied(&err)
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
func (c *hubInstancesController) SetDefault(params appwire.InstanceSetDefaultParams) (err error) {
	if err := c.refuseWhenBroken(); err != nil {
		return err
	}
	if err := c.requireAuth(); err != nil {
		return err
	}
	name := strings.TrimSpace(params.Name)

	c.lockForWrite()
	defer c.mu.Unlock()
	defer c.captureApplied(&err)
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
	// The new default is on disk, so a failed reload is an applied write: only
	// the hub's own view is behind, and every other client's list is stale. The
	// write primitive set the applied mark instanceWrite broadcasts on.
	return c.reg.Reload()
}

// ---- asides machinery (ported from the aside branch) ----

// carryOAuthAsidesForRename carries the parked OAuth copies filed under
// oldName to newName across a rename and names what it could not carry. It
// returns the base name of the copy it promoted to the old record path, if any,
// so moveCredentials can tell whether a read that refuses the record is looking
// at a promoted copy it must move back under a name recovery recognizes. The
// newest copy is promoted to the old record path when that path is free, so
// moveCredentials' read carries it as the record and the renamed instance has a
// usable credential at once; when the path is occupied the instance already has
// a live record, so the copy is carried like every other. Every remaining copy
// is renamed to the new name's parked name with its stamp preserved, so
// restoreUncommittedOAuthAsides and reclaimOAuthAsides still parse it, and no
// copy is left filed under a name providers.toml no longer carries.
//
// A copy whose stamp cannot be ordered (past an int64) is not carried: giving it
// a fresh stamp would rank an intentionally unorderable copy and make it the
// newest recovery candidate. It is left filed under the old name with its stamp
// text intact - still parseable, still unorderable - and reported the way every
// other carry problem is. Committed copies are not touched - they belong to a
// removal that stood and startup sweeps those whose name the config no longer
// carries.
func (c *hubInstancesController) carryOAuthAsidesForRename(oldName, newName string) (string, []string) {
	return carryOAuthAsidesForRenameAt(c.auth.stateDir, oldName, newName)
}

// carryOAuthAsidesForRenameAt is carryOAuthAsidesForRename over a state root, so
// the startup pass can finish a rename the hub died inside.
func carryOAuthAsidesForRenameAt(stateDir, oldName, newName string) (string, []string) {
	dir := filepath.Dir(authopenai.AuthFilePath(stateDir, "instance"))
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", nil
		}
		return "", []string{fmt.Sprintf("OAuth copies under %q not read, so they could not follow the rename: %v", oldName, err)}
	}
	type carriedAside struct {
		name   string
		stamp  int64
		ranked bool
	}
	var copies []carriedAside
	// The highest stamp already filed under the NEW name. The carry is the only
	// writer under the caller's credMu, so this seed - bumped locally as copies
	// land - is equivalent to re-listing the directory for every copy, and cheaper.
	highestDest, haveDest := highestAsideStamp(entries, newName)
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		inst, stampText, aside := parseOAuthAside(e.Name())
		if !aside || inst != oldName {
			continue
		}
		c := carriedAside{name: e.Name()}
		if s, perr := strconv.ParseInt(stampText, 10, 64); perr == nil {
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
	record := authopenai.AuthFilePath(stateDir, oldName)
	if newest != "" {
		switch _, statErr := os.Lstat(record); {
		case errors.Is(statErr, os.ErrNotExist):
			if rerr := renameNoReplace(filepath.Join(dir, newest), record); rerr != nil {
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
	// ascending preserves the source ordering.
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
		if !c.ranked {
			// A copy whose stamp cannot be ordered (past an int64) is not given a
			// fresh rank as part of the carry's own ordering - that would turn an
			// intentionally unorderable copy into the newest recovery candidate.
			// Leaving it under the OLD name is not an option either: providers.toml
			// now names only the new instance, so the copy would be read as debris
			// of a name the config no longer carries and swept. It is re-filed under
			// the NEW name by remarkUncarriedOAuthAside, which reports it and keeps
			// its bytes recoverable, the way every other carry failure is handled.
			problems = append(problems, remarkUncarriedOAuthAside(dir, c.name, newName,
				fmt.Errorf("its stamp %q cannot be ordered (past an int64), so it was not given a fresh rank", oauthAsideStampText(c.name))))
			continue
		}
		// The destination is named from this copy's own stamp, so a copy already
		// filed under the NEW name at that stamp is a real collision. A fresh
		// stamp one past the highest the new name holds avoids replacing it; a
		// carry with no fresh name to take re-files the copy so recovery restores
		// it rather than resolving it forward and deleting it
		// (remarkUncarriedOAuthAside).
		dst, stamp, reason, _ := stepFreeAsideName(filepath.Join(dir, newName+".json"), c.stamp, highestDest, haveDest)
		if reason != asideSearchFound {
			problems = append(problems, remarkUncarriedOAuthAside(dir, c.name, newName,
				fmt.Errorf("no fresh parked name under %q was free to carry it to", newName)))
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

// parkedCopies returns the parked copies filed under name, sorted by name.
func (c *hubInstancesController) parkedCopies(name string) ([]string, error) {
	dir := filepath.Dir(authopenai.AuthFilePath(c.auth.stateDir, "instance"))
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if inst, _, ok := parseOAuthAside(e.Name()); ok && inst == name {
			out = append(out, e.Name())
		}
	}
	slices.Sort(out)
	return out, nil
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
// are read). The name carries no classification of its own: which mutation parked
// the copy, and - for a removal - whether that removal changed providers.toml
// (configBacked: an authored [providers.<name>] entry or a `default` pointer
// naming the instance at removal start) or not (credential-only), is read from
// the intent record the removal wrote BEFORE this rename (its kind field,
// oauthRemovalKind). Recovery classifies a copy from that record, never from the
// copy's name.
func (c *hubInstancesController) setAsideOAuthFile(name string) (string, error) {
	path := authopenai.AuthFilePath(c.auth.stateDir, name)
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		// Nothing here can tell what the path holds, and the rename below would
		// report this same failure as a copy it could not park. Refused with
		// the check named, before anything is deleted.
		return "", fmt.Errorf("remove %s: read its OAuth state at %s before setting it aside: %w", name, path, err)
	}
	// Only a record is parked. This path is renamed by the call below and
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
	// would have the second copy destroy the first, losing exactly the bytes
	// the copy exists to preserve. It comes from the same clock the removal's
	// intent record did, and the record is written FIRST, so this stamp is at or
	// above the record's - a clock too coarse to separate the two reads leaves
	// them EQUAL, which recovery reads as "this record could have parked this
	// copy" (restoreUncommittedOAuthAsides); the write order can never invert.
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
	// credMu exclusively, so no other copy for this path can be created between
	// the existence check and the rename. The candidate keeps the all-digits
	// tail parseOAuthAside requires, so a stepped name is still reclaimable
	// rather than debris.
	stamp := c.auth.now().UnixNano()
	if stamp < 0 {
		// A negative stamp is not a name any copy can carry: its decimal text
		// holds a '-' the all-digits tail parseOAuthAside requires, so the
		// copy would be debris the reclaim silently skips - a removal reported
		// as successful could leave the credential on disk. Refused before
		// anything is deleted, mirroring the stamp bounds the search enforces.
		return "", fmt.Errorf("remove %s: the clock returned the negative stamp %d, which no OAuth copy name can carry", name, stamp)
	}
	aside, _, reason, searchErr := findFreeAsideName(filepath.Dir(path), name, stamp)
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
		// name its copy is refused rather than leaving debris the reclaim
		// skips, which would let a reported removal leave the credential on disk.
		return "", fmt.Errorf("remove %s: no copy stamp at or below %d was free to set its OAuth state aside", name, maxAsideStamp)
	}
	if err := os.Rename(path, aside); err != nil {
		return "", fmt.Errorf("remove %s: set its OAuth state aside to preserve it: %w", name, err)
	}
	// The parked name is durable only once its directory is synced, and every
	// step after this one is durable on its own: a power loss that dropped this
	// rename while the credentials deletion or the config write survived would
	// leave a removal the config says happened with the record still at its
	// canonical path - a state no startup pass reads as a removal at all. The
	// sync is therefore part of the park, and a failure refuses the removal
	// before anything is deleted: the caller abandons the record it wrote, and
	// the copy it parked is an orphan the config decides.
	if err := syncDir(filepath.Dir(path)); err != nil {
		// The rename landed, so these bytes ARE parked; only its durability is
		// unproven. The removal is refused here and has deleted nothing, so the
		// name the copy now holds goes back with the failure: the caller puts the
		// bytes where it found them (or, when it cannot, keeps the record that
		// still names the copy), and either way startup never reads a credential
		// this refusal preserved as debris of a removal that stood.
		return aside, fmt.Errorf("remove %s: sync %s so the name the copy was just parked under is durable before the removal deletes anything: %w", name, filepath.Dir(path), err)
	}
	// The step that takes the record away records the change the instance
	// mutation stands for - this is the primitive's equivalent of the stored-key
	// and record deletions removeCredentials marks - and a restore that puts the
	// copy back clears the mark again (restoreFailedRemoval); a removal that
	// cannot put it back is an applied change whatever else fails.
	c.applied.markApplied()
	return aside, nil
}

// putParkedOAuthRecordBack returns a copy a refused removal parked to the name
// the instance's record belongs at, and syncs the directory so the name it is
// filed under survives a power failure. It is a no-replace move for the reason
// the park is: bytes at the canonical path are whoever wrote them - a sign-in
// that raced this removal - and undoing a park must not destroy them. The caller
// keeps the removal's record when this fails, so the pass that can resolve the
// copy finds it and the bytes staged beside it.
func (c *hubInstancesController) putParkedOAuthRecordBack(name, parked string) error {
	target := authopenai.AuthFilePath(c.auth.stateDir, name)
	if err := renameNoReplace(parked, target); err != nil {
		return err
	}
	return syncDir(filepath.Dir(target))
}

// maxAsideStamp is the largest stamp a copy name can carry. The searches refuse
// to step past it: a successor would wrap to a negative tail that parseOAuthAside
// does not parse, turning a copy into debris no recovery reads. (setAsideOAuthFile's
// own seed guard refuses the same successor.)
const maxAsideStamp int64 = 1<<63 - 1

// renameNoReplace moves src to dst without replacing an existing dst. POSIX
// rename(2) silently replaces its destination, which for an OAuth copy means
// losing the bytes that destination held - a stale copy of the same instance's
// record, or another copy's only surviving credential. A taken destination comes
// back as an error satisfying errors.Is(err, os.ErrExist), so the caller can pick
// a fresh name or report the copy as uncarried. The old link-then-unlink left a
// window between two syscalls in which a crash stranded the bytes under both
// names - a partial move whose old- and new-name copies recovery could restore
// independently. A filesystem that cannot hard-link a file no longer matters: no
// link is attempted.
//
// WHERE the no-replace decision is made depends on the platform and the
// filesystem, and what this function guarantees follows from that:
//
//   - With an atomic no-replace rename (renameNoReplaceAtomic: renameat2's
//     RENAME_NOREPLACE on Linux) the refusal is the KERNEL's, in the one syscall
//     the move is. No destination can be created between a check and the move,
//     because there is no check: a writer racing this move loses to it, and the
//     taken destination is left exactly as it was.
//   - A filesystem that cannot do one (EINVAL, ENOSYS, ENOTSUP - a network or
//     FUSE mount) takes the checked move at the end of this function: an Lstat,
//     then a plain rename(2). That path HAS a window, and what closes it is the
//     caller discipline rather than this function: every caller serializes under
//     credMu (Edit's and Remove's; the rename carry and the removal both hold it
//     across their moves) or is startup before the hub serves
//     (restoreUncommittedOAuthAsides, run while the process holds hub.lock and
//     before the web server is built). A writer that holds neither - a second hub
//     sharing this state root while holding a different hub.lock, which nothing
//     in this package excludes (see repairEndpointFingerprintKey's sibling lock)
//   - can create the destination inside that window and have it replaced.
//
// src equal to dst is refused explicitly with os.ErrExist. rename(2) on a path
// onto itself is a no-op success, but a caller that reaches for a name the
// source already holds has mistaken a taken destination for a free one, and a
// silent no-op would report a move that never happened as landed. link(2)
// refused this same path with EEXIST, so the refusal is kept here rather than
// lost to rename(2).
func renameNoReplace(src, dst string) error {
	if src == dst {
		return &os.LinkError{Op: "rename", Old: src, New: dst, Err: os.ErrExist}
	}
	err := renameNoReplaceAtomic(src, dst)
	if err == nil {
		return nil
	}
	if errors.Is(err, os.ErrExist) {
		return &os.LinkError{Op: "rename", Old: src, New: dst, Err: os.ErrExist}
	}
	if !renameNoReplaceAtomicUnsupported(err) {
		// The move itself failed - a source that is not there, a directory that
		// cannot be written, a permission - and the caller hears that cause rather
		// than a refusal it would read as "the destination is taken".
		return err
	}
	// The checked fallback: this filesystem has no no-replace rename to make, so
	// the destination is looked at first. A destination that appears after this
	// look is replaced - see the discipline the doc comment names - which is the
	// one thing this path cannot promise on its own.
	if _, statErr := os.Lstat(dst); statErr == nil {
		return &os.LinkError{Op: "rename", Old: src, New: dst, Err: os.ErrExist}
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return statErr
	}
	return os.Rename(src, dst)
}

// freeAsideName picks a path for a parked copy of name's record, carrying a
// stamp no name in dir already holds. want is the stamp preferred (a copy's own,
// across a rename); the search starts one past the highest stamp already filed
// for name when that is greater, so a copy set down later orders newest, and it
// steps upward past any candidate still taken. found is false when no candidate
// is free - the stamp would pass maxAsideStamp - so the caller leaves its source
// where it is rather than overwriting anything. The candidate keeps the
// all-digits tail parseOAuthAside requires, so every name this can return parses
// as a copy.
func freeAsideName(dir, name string, want int64) (string, bool) {
	path, _, reason, _ := findFreeAsideName(dir, name, want)
	if reason != asideSearchFound {
		return "", false
	}
	return path, true
}

// asideSearch is why findFreeAsideName and stepFreeStampedName returned no path:
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

// highestStampedStamp returns the highest stamp already carried by a name built
// from inst's record under the given marker, and whether any was found. A stamp
// that cannot be ordered (past an int64) is skipped, exactly as the search below
// skips it.
func highestStampedStamp(entries []os.DirEntry, inst, marker string) (int64, bool) {
	var highest int64
	var have bool
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		record, stampText, ok := parseStampedName(e.Name(), marker)
		if !ok || strings.TrimSuffix(record, ".json") != inst {
			continue
		}
		s, perr := strconv.ParseInt(stampText, 10, 64)
		if perr != nil {
			continue
		}
		if !have || s > highest {
			highest, have = s, true
		}
	}
	return highest, have
}

// highestAsideStamp returns the highest stamp already filed for a parked copy of
// name in entries, and whether any was found.
func highestAsideStamp(entries []os.DirEntry, name string) (int64, bool) {
	return highestStampedStamp(entries, name, oauthAsideMarker)
}

// stepFreeStampedName picks the first name <recordPath><marker><stamp> beneath
// recordPath's directory whose stamp is want - bumped one past highest when that
// is greater - and that nothing holds, stepping upward past any candidate still
// taken. It is the ONE collision-safe naming rule the parked copies and the
// intent records share: a name recovery ranks by its stamp must never be handed
// a stamp below one already filed, and a taken candidate must never be replaced.
// The candidate keeps the all-digits tail parseStampedName requires, so every
// path this returns parses as the shape the marker names. reason is
// asideSearchFound on success, and otherwise names why the search refused; err
// carries the filesystem cause where there was one.
func stepFreeStampedName(recordPath, marker string, want, highest int64, have bool) (string, int64, asideSearch, error) {
	stamp := want
	// The stamp this name must carry is at or above every name already filed for
	// this record, so recovery - which restores the NEWEST copy of a name, and
	// orders records by stamp - cannot put an older credential back in place of
	// this one, and cannot read a later mutation's record as an earlier one.
	if have && highest >= maxAsideStamp {
		// The highest name already filed sits at the maximum, so there is no stamp
		// above it to step to and the maximum itself is taken: refuse rather than
		// file this one below a name recovery would rank above it.
		return "", stamp, asideSearchExhausted, nil
	}
	if have && highest+1 > stamp {
		// One past the highest is all digits for any non-negative stamp; the case
		// above keeps this comparison away from a maxAsideStamp successor, which
		// would wrap to a negative tail no name can carry.
		stamp = highest + 1
	}
	for {
		candidate := recordPath + marker + strconv.FormatInt(stamp, 10)
		switch _, lerr := os.Lstat(candidate); {
		case errors.Is(lerr, os.ErrNotExist):
			return candidate, stamp, asideSearchFound, nil
		case lerr != nil:
			// The candidate could not be checked, so nothing here can promise the
			// move that follows will not land on something already there.
			return candidate, stamp, asideSearchCandidateUnreadable, lerr
		}
		if stamp >= maxAsideStamp {
			return "", stamp, asideSearchExhausted, nil
		}
		stamp++
	}
}

// stepFreeAsideName is stepFreeStampedName for a parked copy.
func stepFreeAsideName(recordPath string, want, highest int64, have bool) (string, int64, asideSearch, error) {
	return stepFreeStampedName(recordPath, oauthAsideMarker, want, highest, have)
}

// findFreeAsideName lists dir, seeds the search from the highest stamp already
// filed for name there, and delegates the collision-safe naming to
// stepFreeAsideName. A directory that cannot be listed is refused rather than
// guessed at - the stamps already filed for name are what a candidate must step
// past, and recovery orders copies by their stamps. A directory that does not
// exist is the empty case: the move that follows reports whatever is really
// wrong.
func findFreeAsideName(dir, name string, want int64) (string, int64, asideSearch, error) {
	entries, rerr := os.ReadDir(dir)
	if rerr != nil && !errors.Is(rerr, os.ErrNotExist) {
		return "", 0, asideSearchDirUnreadable, rerr
	}
	highest, have := highestAsideStamp(entries, name)
	// The record this copy belongs to is written FIRST, and its own stamp steps
	// past every record already filed for the name - so a copy seeded from the
	// copies alone can be parked BELOW its record's stamp: a lingering record
	// stamped above the clock (a step backward, a snapshot restored) is stepped
	// past by the record while this copy stays at the clock's reading. Recovery
	// attributes a copy to a record only while the record's stamp is at or below
	// the copy's (restoreUncommittedOAuthAsides), so an inverted pair reads as an
	// orphan - and an orphan of a name the config does not carry is swept, losing
	// the only credential of a removal that never committed. The copy therefore
	// steps past both record markers too, so the order the stamps carry is the
	// order of the mutations whatever the clock does.
	for _, marker := range []string{oauthIntentMarker, oauthLandedMarker} {
		if s, ok := highestStampedStamp(entries, name, marker); ok && (!have || s > highest) {
			highest, have = s, true
		}
	}
	return stepFreeAsideName(filepath.Join(dir, name+".json"), want, highest, have)
}

// writeIntent names and writes one mutation's intent record. The stamp is the
// clock's, stepped past any record already filed for the same instance by the
// one collision-safe search the copy names use too (stepFreeStampedName), so a
// repeated stamp - a clock that did not advance, a state root restored from a
// snapshot - never overwrites an earlier mutation's record: two records of one
// instance never share a name. A record and a COPY can share a stamp, because
// the copy's name carries a different marker; that is the write order showing
// through (the record is written before the copy is parked), and recovery reads
// it as the record's. The bytes are written atomically (writeOAuthIntentFile).
//
// createDir names whether the auth directory may be created. A rename's record
// is needed whenever a STORED CREDENTIAL may be present - a stored key lives in
// credentials.toml, not in this directory - so a key-only rename creates the
// directory rather than skipping its record. A removal skips the record entirely
// when the directory does not exist: a record path and a parked copy live inside
// it, so a state root that never held one has nothing a crash could strand.
func (c *hubInstancesController) writeIntent(i oauthIntent, createDir bool) (string, error) {
	dir := filepath.Dir(authopenai.AuthFilePath(c.auth.stateDir, "instance"))
	if createDir {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return "", fmt.Errorf("make %s (%w)", dir, err)
		}
	} else {
		if _, err := os.Stat(dir); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return "", nil
			}
			return "", fmt.Errorf("check %s (%w)", dir, err)
		}
	}
	stamp := c.auth.now().UnixNano()
	if stamp < 0 {
		// A negative stamp is not a name any record can carry: its decimal text
		// holds a '-' the all-digits tail parseOAuthIntent requires, so the record
		// would be debris recovery never reads.
		return "", fmt.Errorf("the clock returned the negative stamp %d, which no intent name can carry", stamp)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", fmt.Errorf("list %s (%w)", dir, err)
	}
	highest, have := highestStampedStamp(entries, i.inst, oauthIntentMarker)
	// A landed record is the same mutation's other name, so it occupies the same
	// stamp: a new record steps past both.
	if landed, ok := highestStampedStamp(entries, i.inst, oauthLandedMarker); ok && (!have || landed > highest) {
		highest, have = landed, true
	}
	path, _, reason, searchErr := stepFreeStampedName(filepath.Join(dir, i.inst+".json"), oauthIntentMarker, stamp, highest, have)
	switch reason {
	case asideSearchFound:
	case asideSearchCandidateUnreadable:
		return "", fmt.Errorf("check whether %s is free to record this mutation: %w", path, searchErr)
	default:
		return "", fmt.Errorf("no intent stamp at or below %d was free to record this mutation", maxAsideStamp)
	}
	if err := writeOAuthIntentFile(path, i); err != nil {
		return "", fmt.Errorf("write %s (%w)", path, err)
	}
	return path, nil
}

// readIntents reads the auth directory once and returns every intent record it
// holds, oldest stamp first. A record that does not read back is returned with
// its cause, so the caller reports it and keeps the file.
func (c *hubInstancesController) readIntents() ([]oauthIntentFile, error) {
	dir := filepath.Dir(authopenai.AuthFilePath(c.auth.stateDir, "instance"))
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	records, _ := classifyOAuthState(dir, entries)
	return records, nil
}

// completeRenameIntent finishes one rename whose intent record is on disk: it
// moves the stored key in the LIVE store, carries the copies still filed under
// the old name, files a record left at the old canonical path under the new name
// (or under a parked copy when that path is taken) and rewrites the record's
// Provider when it reads back. Both halves are idempotent - Store.Move is a
// no-op when the old name holds nothing, and the carry moves only what is still
// filed under the old name - so it is safe at every crash point and on every
// start: the overwrite guard below refuses only a move that would happen, so a
// rename whose key is already filed under the new name is finished rather than
// refused. It reports whether it moved anything, which is what asks the caller for
// the reload that publishes it, and what it could not finish; the record is
// spent only when the whole move landed.
//
// The config is what says whether the rename landed - it is written before any
// credential moves - so a config that cannot be read leaves the record for a
// later pass rather than guessing, and a config that still (or again) names the
// old instance means the rename never happened: nothing moves, and the record is
// spent.
func (c *hubInstancesController) completeRenameIntent(store *credentials.Store, intentPath, oldName, newName string, layer *registry.Layer, cfgErr error) (bool, []string) {
	return completeRenameIntentAt(c.auth.stateDir, store, intentPath, oldName, newName, layer, cfgErr)
}

// completeRenameIntentAt is completeRenameIntent over a state root, so the
// startup pass - which has no controller - can finish a rename it finds.
func completeRenameIntentAt(stateDir string, store *credentials.Store, intentPath, oldName, newName string, layer *registry.Layer, cfgErr error) (bool, []string) {
	if cfgErr != nil {
		return false, []string{fmt.Sprintf("finish the rename of %q to %q: the config that says whether it landed could not be read (%v)", oldName, newName, cfgErr)}
	}
	if !configCarriesName(layer, newName) {
		// The config never reached the new name, so the rename did not happen:
		// nothing migrates, and the record is spent.
		if err := removeOAuthIntent(intentPath); err != nil {
			return false, []string{fmt.Sprintf("spend the rename record %s of %q to %q, which the config says never landed (%v)", intentPath, oldName, newName, err)}
		}
		return false, nil
	}
	if store == nil {
		// Without the store this pass cannot tell whether the rename's stored
		// key still needs moving, and guessing would spend a record whose key
		// never moved. The record stays for the pass that can.
		return false, []string{fmt.Sprintf("finish the rename of %q to %q: the credentials store was not provided, so its stored key cannot be moved", oldName, newName)}
	}
	// A key already filed under the new name is the user's - supplied after the
	// rename was interrupted - and Store.Move replaces its destination, so moving
	// this rename's key onto it would destroy a key nothing else holds. The live
	// rename refuses this same case ("would overwrite ...; clear that first"), and
	// recovery must not do what the live path refuses: the move is refused, the
	// key that blocks it is named, and the record stays for the pass that can
	// finish the rename once the destination is clear.
	//
	// The refusal belongs to a move that would happen. With nothing filed under
	// the old name there is no move to make - Store.Move is a documented no-op
	// then - and a key under the new name can only be this rename's OWN: the pass
	// that moved it reached the move and then reported a problem, which is what
	// kept the record for this pass. Reading it as the user's would refuse for
	// ever over a state a previous pass created, leaving the credential filed
	// under a name the config no longer points at.
	if _, moving := store.Get(oldName); moving {
		if _, taken := store.Get(newName); taken {
			return false, []string{fmt.Sprintf("finish the rename of %q to %q: a stored key is already filed under %q, and moving this rename's key onto it would overwrite it; clear that key to let the rename finish", oldName, newName, newName)}
		}
	}
	// One persist, so the key is never briefly filed under both names or neither.
	if err := store.Move(oldName, newName); err != nil {
		return false, []string{fmt.Sprintf("finish the rename of %q to %q: move its stored key (%v)", oldName, newName, err)}
	}
	problems := finishRenameAt(stateDir, oldName, newName)
	if _, statErr := os.Lstat(authopenai.AuthFilePath(stateDir, oldName)); !errors.Is(statErr, os.ErrNotExist) {
		problems = append(problems, fmt.Sprintf("finish the rename of %q to %q: its record is still filed under the old name", oldName, newName))
	}
	if len(problems) > 0 {
		return true, problems
	}
	if err := removeOAuthIntent(intentPath); err != nil {
		problems = append(problems, fmt.Sprintf("finish the rename of %q to %q: spend its record %s (%v)", oldName, newName, intentPath, err))
	}
	return true, problems
}

// finishPendingRename resolves any rename INTO name that never finished, before
// a later rename takes that name away: a pending rename record whose new name is
// this one says a credential is still filed under the name it came from, and
// chaining a later rename onto it would strand those bytes under a name neither
// the config nor any recovery pass reads any more. What it cannot finish is
// returned, so the caller refuses rather than chains.
func (c *hubInstancesController) finishPendingRename(name string, layer *registry.Layer, cfgErr error) []string {
	records, err := c.readIntents()
	if err != nil {
		return []string{fmt.Sprintf("read the intent records before renaming %q (%v)", name, err)}
	}
	var problems []string
	var pending []oauthIntentFile
	for _, f := range records {
		if f.parseErr != nil {
			problems = append(problems, fmt.Sprintf("read the intent %s (%v)", f.path, f.parseErr))
			continue
		}
		if f.intent.op == oauthOpRename && f.intent.new == name {
			pending = append(pending, f)
		}
	}
	// Oldest first: a rename chains onto the name an earlier one handed over, so
	// the earlier record is the one whose credential is furthest behind.
	slices.SortStableFunc(pending, func(a, b oauthIntentFile) int {
		if a.stamp != b.stamp {
			return cmp.Compare(a.stamp, b.stamp)
		}
		return strings.Compare(a.path, b.path)
	})
	for _, f := range pending {
		_, more := c.completeRenameIntent(c.auth.creds, f.path, f.intent.inst, f.intent.new, layer, cfgErr)
		problems = append(problems, more...)
	}
	return problems
}

// finishRenameAt completes a rename a crash interrupted. The rename's intent
// record says which name became which, so this replays the two things the rename
// does: it carries the copies still filed under the OLD name to the new one -
// where the pass that follows reads them - and it moves a record left at the old
// canonical path (the carry promotes the newest copy onto it) to the new name's
// record path, or to a parked copy under the new name when that path is taken.
// Nothing is left under a name no reader looks at. It reports what it could not
// move.
func finishRenameAt(stateDir, oldName, newName string) []string {
	var problems []string
	if _, carryProblems := carryOAuthAsidesForRenameAt(stateDir, oldName, newName); len(carryProblems) > 0 {
		problems = append(problems, carryProblems...)
	}
	dir := filepath.Dir(authopenai.AuthFilePath(stateDir, "instance"))
	oldRecord := authopenai.AuthFilePath(stateDir, oldName)
	newRecord := authopenai.AuthFilePath(stateDir, newName)
	switch info, statErr := os.Lstat(oldRecord); {
	case errors.Is(statErr, os.ErrNotExist):
		// Nothing at the old path: the carry either never promoted or the record
		// was already saved under the new name.
	case statErr != nil:
		problems = append(problems, fmt.Sprintf("finish the rename of %q to %q: check its record at %s (%v)", oldName, newName, oldRecord, statErr))
	case !info.Mode().IsRegular():
		// Only a record is a file the hub reads. Anything else at that path was
		// not written as one, and moving it into an aside or a canonical name
		// would take it where the copy rules skip directories and strand what it
		// holds.
		problems = append(problems, fmt.Sprintf("finish the rename of %q to %q: %s is not a regular file (%v), so it was left where it is", oldName, newName, oldRecord, info.Mode()))
	default:
		dst := newRecord
		if _, takenErr := os.Lstat(newRecord); takenErr == nil {
			// The renamed instance's record path is taken: these bytes must not
			// replace it, and must not be left where nothing reads them either.
			aside, ok := freeAsideName(dir, newName, 0)
			if !ok {
				problems = append(problems, fmt.Sprintf("finish the rename of %q to %q: no fresh aside name under %q was free to file the record %s under, so those bytes are still at %q", oldName, newName, newName, oldRecord, oldRecord))
				break
			}
			dst = aside
		} else if !errors.Is(takenErr, os.ErrNotExist) {
			problems = append(problems, fmt.Sprintf("finish the rename of %q to %q: check %s before moving the record there (%v)", oldName, newName, newRecord, takenErr))
			break
		}
		if rerr := renameNoReplace(oldRecord, dst); rerr != nil {
			problems = append(problems, fmt.Sprintf("finish the rename of %q to %q: move its record from %s to %s (%v)", oldName, newName, oldRecord, dst, rerr))
			break
		}
		if dst != newRecord {
			// Filed as an aside, not as the record: the copy rules read it from
			// there and the provider field is rewritten when recovery restores it.
			break
		}
		// The bytes now sit under the renamed instance's own name, but the record
		// still identifies the instance it came from. The live rename rewrites
		// that field through the store's atomic writer (moveCredentials), and
		// recovery has to do the same: a record whose provider names another
		// instance is one the registry resolves against the wrong name.
		// A record the hub cannot read has no provider field to rewrite, and its
		// bytes are exactly what recovery was asked to preserve: they stay where
		// the move put them, and this pass reports nothing - refusing here would
		// block every later recovery over a file only a re-sign-in can fix.
		if rec, lerr := authopenai.LoadAuth(stateDir, newName); lerr == nil {
			rec.Provider = newName
			if serr := authopenai.SaveAuth(stateDir, newName, rec); serr != nil {
				problems = append(problems, fmt.Sprintf("finish the rename of %q to %q: rewrite the record's provider field (%v)", oldName, newName, serr))
			}
		}
	}
	return problems
}

// remarkUncarriedOAuthAside re-files a copy a rename could not carry so startup
// recovery restores it to the RENAMED instance instead of sweeping it, and
// returns the carry problem to report. providers.toml already names only the new
// instance, so a copy left under the OLD name is debris of a name the config no
// longer carries - swept at the next start - and the renamed instance would be
// left without the credential the carry was trying to preserve. The copy is
// therefore filed under the NEW name as that name's parked copy, chosen the same
// collision-safe way the carry chooses a destination (freeAsideName, which keeps
// the all-digits stamp parseOAuthAside requires) and landed without replacing
// anything (renameNoReplace). Startup recovery then puts it back at the new
// record path when that path is free, or leaves it where the pass that owns the
// name can see it. When even that cannot land, the report says plainly that the
// bytes are still filed under the old name in a shape startup will not restore.
func remarkUncarriedOAuthAside(dir, name, newName string, cause error) string {
	// The copy's own stamp is preferred, so the fallback orders the same way the
	// carry would have. A stamp that cannot be ordered (past an int64) starts the
	// search at zero and freeAsideName steps past whatever is taken.
	var want int64
	if s, perr := strconv.ParseInt(oauthAsideStampText(name), 10, 64); perr == nil {
		want = s
	}
	dst, ok := freeAsideName(dir, newName, want)
	if !ok {
		return fmt.Sprintf("OAuth copy %q not carried to %q, and no fresh parked name under %q was free to re-file it, so its bytes are still filed under the old name, where only the rename's own record keeps startup from reading them as debris (%v)", name, newName, newName, cause)
	}
	if rerr := renameNoReplace(filepath.Join(dir, name), dst); rerr != nil {
		return fmt.Sprintf("OAuth copy %q not carried to %q, and it could not be re-filed under %q, so its bytes are still filed under the old name, where only the rename's own record keeps startup from reading them as debris (%v; %v)", name, newName, filepath.Base(dst), cause, rerr)
	}
	return fmt.Sprintf("OAuth copy %q not carried to %q and re-filed under the new name as %q, which startup recovery restores to the renamed instance rather than deleting the credential (%v)", name, newName, filepath.Base(dst), cause)
}

// configCarriesName reports whether providers.toml still carries an instance: an
// authored [providers.<name>] entry or a `default` pointer naming it. A removal
// that only cleared `default` still wrote the file (Remove's configChanged), so
// `default` is one of the facts that decides the record's kind, and recovery must
// read the config the same way - a config-backed removal whose name a `default`
// pointer still names is one that never reached its write, and its copy is put
// back rather than swept.
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
// removed while the auth directory kept its copies all read that way, and
// treating it as authoritative would sweep every config-backed removal's copy
// that a readable config would have put back.
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
// records and copies of a pass unclassified, in the two spellings the pass
// distinguishes: no path at all, and a path it could not read.
func asideConfigProblem(providersConfigPath string, cfgErr error) string {
	if providersConfigPath == "" {
		return fmt.Sprintf("%v, so only credential-only removals were resolved and every config-dependent record and copy was left untouched", cfgErr)
	}
	return fmt.Sprintf("read %s to classify the config-backed records and the copies the config decides, so only credential-only removals were resolved and every config-dependent record and copy was left untouched (%v)", providersConfigPath, cfgErr)
} // oauthIntentFile is one intent record found in the auth directory: the path,
// the instance and stamp its NAME carries, and the record itself (parseErr is
// set when it does not read back, and the file is then kept and reported rather
// than guessed at).
type oauthIntentFile struct {
	path      string
	inst      string
	stampText string
	stamp     int64
	ranked    bool
	// landed reports that the record was carried to its commit-point name: the
	// rename that filed it there is the evidence a credential-only removal stood.
	landed   bool
	intent   oauthIntent
	parseErr error
}

// oauthCopyFile is one parked copy found in the auth directory.
type oauthCopyFile struct {
	name      string
	inst      string
	stampText string
	stamp     int64
	ranked    bool
}

// classifyOAuthState reads one directory listing and sorts it into the intent
// records and the parked copies it holds, parsing each name once and each record
// once. Every other entry - a live record, a temp file, anything else - is not
// this pass's business and is left alone.
func classifyOAuthState(dir string, entries []os.DirEntry) ([]oauthIntentFile, []oauthCopyFile) {
	var records []oauthIntentFile
	var copies []oauthCopyFile
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if inst, stampText, landed, ok := parseOAuthIntent(e.Name()); ok {
			f := oauthIntentFile{path: filepath.Join(dir, e.Name()), inst: inst, stampText: stampText, landed: landed}
			if s, perr := strconv.ParseInt(stampText, 10, 64); perr == nil {
				f.stamp, f.ranked = s, true
			}
			raw, rerr := os.ReadFile(f.path)
			if rerr != nil {
				f.parseErr = rerr
			} else if i, perr := parseOAuthIntentRecord(raw, f.path); perr != nil {
				f.parseErr = perr
			} else if i.inst != inst {
				// The name and the record must agree about which mutation this is:
				// a record filed under one instance but naming another classifies
				// nothing, and guessing which side is right could sweep or restore
				// the wrong name's credential.
				f.parseErr = fmt.Errorf("%s records the instance %q but is filed under %q", f.path, i.inst, inst)
			} else {
				f.intent = i
			}
			records = append(records, f)
			continue
		}
		if inst, stampText, ok := parseOAuthAside(e.Name()); ok {
			c := oauthCopyFile{name: e.Name(), inst: inst, stampText: stampText}
			if s, perr := strconv.ParseInt(stampText, 10, 64); perr == nil {
				c.stamp, c.ranked = s, true
			}
			copies = append(copies, c)
		}
	}
	return records, copies
}

// oauthInstanceState is what one pass knows about one instance name.
type oauthInstanceState struct {
	name    string
	records []oauthIntentFile
	copies  []oauthCopyFile
	// parkedBy[i] reports whether a removal record of this name was written at or
	// before copies[i]'s stamp, which is what makes the copy that record's to
	// judge: a mutation writes its record BEFORE it parks anything, so a record at
	// or below a copy's stamp is one that could have parked it - and a clock too
	// coarse to separate the two reads can only make the stamps EQUAL, never
	// invert the order. A copy whose stamp cannot be ordered is never parkedBy.
	parkedBy []bool
	// loneSweep[i] is whether a swept copies[i] must be REPORTED: no removal record
	// precedes it at all, or one of the records that do is credential-only. Two
	// records can share one stamp (a coarse clock, a snapshot, two mutations in a
	// tick), and then the copy cannot be told apart between them, so the tie is
	// read in the conservative direction - a delete is reported when ANY record
	// that could have parked those bytes is credential-only.
	loneSweep []bool
	// needsConfig reports whether any of this name's state cannot be judged
	// without providers.toml: a rename record, a config-backed removal record, a
	// record that does not read back, or a copy no record claims.
	needsConfig bool
}

// restoreUncommittedOAuthAsides resolves the OAuth state a mutation left behind:
// put back a credential a failed removal parked, sweep the copies a standing
// removal no longer wants, finish a rename a crash interrupted. It reports what
// it could not do - never silently - and whether it restored or moved anything,
// which is what asks the caller for the reload that publishes a credential the
// registry's list was computed without.
//
// It reads the auth directory ONCE and classifies every entry by the ONE
// grammar: an intent record, a parked copy, a live record, or something else.
// What a copy is for is never guessed from its name; it is read from the intent
// that recorded the mutation, which carries the op, the instance, the removal's
// kind and the phase the mutation reached.
//
// The rules, oldest mutation first:
//
//   - A config-backed removal is judged by the CONFIG, which is the durable
//     evidence of whether it landed: providers.toml is written before the
//     removal's commit point and is what a reload reads. A config that no longer
//     carries the name means the removal stood, so the copies it parked are
//     swept; a config that still carries it means the removal did not stand, so
//     the newest copy goes back at the record path when that path is free.
//   - A credential-only removal has no config entry to ask - the record file was
//     the whole of what made the instance exist - so the PHASE is its evidence: a
//     landed record means the removal reached its commit point and its copies are
//     swept, a started one means the removal is still in doubt and the newest
//     copy goes back.
//   - A rename is completed forward when the config carries the new name and
//     forgotten when it carries the old one, because the config write precedes
//     every credential move.
//   - An orphan - a parked copy with no intent - is judged by the config alone: a
//     name the config carries gets its newest copy back, and a name it does not
//     carry has its copies swept.
//
// The evidence is DATED: a copy parked by a later removal than the one that
// stood is that later removal's, and is judged by it, so a re-created instance's
// interrupted removal is not swept by the older standing removal's proof.
//
// An unreadable (or absent) config is never guessed at: change nothing, keep
// every record, and report. Only the work that does not need the config - a
// credential-only removal's own record - still runs, and an instance that has
// config-dependent state of its own is held back WHOLE, because resolving its
// credential-only copy first would take the record path and leave a stale
// credential standing in for the record a readable config would have put back.
// A pass with nothing recorded and nothing parked reports nothing at all, so a
// fresh install without a providers.toml starts clean.
//
// A copy goes back only when the record path is free: bytes at that path are the
// instance's record now (a later sign-in, a landed rollback), and a parked copy
// beside it stays as debris rather than replacing it. Every move is a
// no-replace move for the same reason, and a copy whose stamp cannot be ordered
// (a name no removal wrote) is left for a human rather than being handed the
// newest rank.
//
// The credentials store is the LIVE one the hub was built with (main.go), so a
// rename's key move lands in the store the rest of the process writes, and every
// move this pass makes sets the returned bool.
func restoreUncommittedOAuthAsides(stateDir, providersConfigPath string, store *credentials.Store) (bool, error) {
	dir := filepath.Dir(authopenai.AuthFilePath(stateDir, "instance"))
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			// No directory is nothing set aside: a state root that never held a
			// record has no copy of one.
			return false, nil
		}
		// The directory exists but cannot be read. That is recovery work this
		// pass could not do, and NOTHING was recovered: no entry was read, so no
		// copy was put back and none was classified - a credential may still be
		// sitting in flight under this directory. The config failure (which the
		// pass would have needed) is reported beside it, but not through
		// asideConfigProblem: that text says which copies were put back and which
		// were left for a later pass, and here there is no such answer.
		if _, cfgErr := readAsideConfig(providersConfigPath); cfgErr != nil {
			return false, fmt.Errorf("startup OAuth recovery could not finish: nothing was recovered - the OAuth state directory %s could not be listed (%w), and the config that would classify what it holds could not be read either (%w)", dir, err, cfgErr)
		}
		return false, fmt.Errorf("put back the OAuth records a failed removal parked: %w", err)
	}
	records, copies := classifyOAuthState(dir, entries)
	if len(records) == 0 && len(copies) == 0 {
		// Nothing recorded and nothing parked is nothing to recover, so a config
		// failure here prevented no recovery work and is not reported.
		return false, nil
	}
	layer, cfgErr := readAsideConfig(providersConfigPath)
	var problems []string
	// Everything one name's state is, grouped once: a name's records and copies
	// are one story, and the name is the unit the pass decides - and defers - in.
	states := make(map[string]*oauthInstanceState)
	var order []string
	stateFor := func(name string) *oauthInstanceState {
		st, ok := states[name]
		if !ok {
			st = &oauthInstanceState{name: name}
			states[name] = st
			order = append(order, name)
		}
		return st
	}
	for _, f := range records {
		st := stateFor(f.inst)
		st.records = append(st.records, f)
	}
	for _, c := range copies {
		st := stateFor(c.inst)
		st.copies = append(st.copies, c)
	}
	slices.Sort(order)
	for _, name := range order {
		st := states[name]
		st.parkedBy = make([]bool, len(st.copies))
		st.loneSweep = make([]bool, len(st.copies))
		for ci := range st.copies {
			c := st.copies[ci]
			if !c.ranked {
				continue
			}
			for ri := range st.records {
				f := st.records[ri]
				if f.parseErr != nil || !f.ranked || f.intent.op != oauthOpRemove {
					continue
				}
				if f.stamp > c.stamp {
					continue
				}
				st.parkedBy[ci] = true
				if f.intent.kind == oauthKindCredentialOnly {
					st.loneSweep[ci] = true
				}
			}
			if !st.parkedBy[ci] {
				// Nothing wrote a record before these bytes were parked: an
				// orphan, and the config alone decides it.
				st.loneSweep[ci] = true
			}
		}
		for _, f := range st.records {
			if f.parseErr != nil || f.intent.op == oauthOpRename || f.intent.kind == oauthKindConfigBacked {
				st.needsConfig = true
			}
		}
		for ci := range st.copies {
			if !st.parkedBy[ci] && st.copies[ci].ranked {
				st.needsConfig = true
			}
		}
	}
	// A record that does not read back classifies nothing: its name is held back
	// whole (change nothing, keep every byte) and the cause is reported.
	held := make(map[string]bool)
	for _, name := range order {
		for _, f := range states[name].records {
			if f.parseErr != nil {
				held[name] = true
				problems = append(problems, fmt.Sprintf("read the intent %s (%v)", f.path, f.parseErr))
			}
		}
	}
	// The config this pass could not read holds back every name with
	// config-dependent state - and with it any credential-only record of the same
	// name, because resolving one first would take the record path and leave a
	// stale credential standing in for the record the config would have judged.
	var heldBack []string
	if cfgErr != nil {
		for _, name := range order {
			if !states[name].needsConfig {
				continue
			}
			held[name] = true
			for _, f := range states[name].records {
				if f.parseErr == nil && f.intent.op == oauthOpRemove && f.intent.kind == oauthKindCredentialOnly {
					heldBack = append(heldBack, name)
					break
				}
			}
		}
		if len(held) > 0 {
			problems = append(problems, asideConfigProblem(providersConfigPath, cfgErr))
		}
	}
	if len(heldBack) > 0 {
		problems = append(problems, fmt.Sprintf("held back the credential-only records of %s: the config that would classify a config-dependent record or copy of the same instance could not be read, so resolving one could stand a stale credential in for the current record", strings.Join(heldBack, ", ")))
	}
	restored := false
	// A rename resolves first, oldest mutation first: a rename that created the
	// name a later mutation used must finish before that mutation is judged, or
	// the copies it is moving would be classified against a config that has
	// already moved on. Its name is the rename's business and nothing else's.
	renamed := make(map[string]bool)
	var recordOrder []int
	for ri := range records {
		recordOrder = append(recordOrder, ri)
	}
	slices.SortStableFunc(recordOrder, func(a, b int) int {
		if records[a].stamp != records[b].stamp {
			return cmp.Compare(records[a].stamp, records[b].stamp)
		}
		return strings.Compare(records[a].path, records[b].path)
	})
	for _, ri := range recordOrder {
		f := records[ri]
		if f.parseErr != nil || f.intent.op != oauthOpRename || held[f.inst] {
			continue
		}
		moved, more := completeRenameIntentAt(stateDir, store, f.path, f.intent.inst, f.intent.new, layer, cfgErr)
		if moved {
			restored = true
		}
		problems = append(problems, more...)
		// The old name is rename-owned only while a pass will still complete the
		// rename. A record the config says never landed is spent by the call above,
		// and nothing will come for the copies this skip holds back: they are the
		// instance's credential again, and the one rule has to classify them. A
		// record kept for a later pass (a config that could not be read, no store to
		// move a key with, a move that failed, a key blocking it) still owns the name
		// and still holds its copies back.
		if moved || intentStillFiled(f.path) {
			renamed[f.inst] = true
		}
	}
	// Every other name is resolved by the one rule.
	for _, name := range order {
		st := states[name]
		if held[name] || renamed[name] {
			continue
		}
		carries := cfgErr == nil && configCarriesName(layer, name)
		// stood[ri] is whether the record proves its removal landed: a
		// credential-only removal is proved by the record's NAME - it was carried
		// to its landed marker at the commit point, an atomic rename a torn or
		// lost write cannot fake - and a config-backed one by the config, which is
		// durable evidence the record cannot supply. proof[ri] dates that proof by
		// the stamp, and the marker is checked against it below: a landed record
		// older than a copy cannot settle that copy.
		stood := make([]bool, len(st.records))
		proof := make([]int64, len(st.records))
		for ri, f := range st.records {
			if f.parseErr != nil || !f.ranked || f.intent.op != oauthOpRemove {
				continue
			}
			if f.intent.kind == oauthKindCredentialOnly {
				stood[ri] = f.landed
			} else {
				stood[ri] = cfgErr == nil && !configCarriesName(layer, f.inst)
			}
			// A record's proof is dated by the newest copy it could have parked: a
			// copy at or after its own stamp that no LATER record could have parked
			// (a later record's stamp bounds the copies before it, so a re-created
			// instance's interrupted removal is not swept by an older removal's
			// proof). Records sharing one stamp bound each other the same way, so a
			// tie gives them identical proofs.
			mark := f.stamp
			for _, c := range st.copies {
				if !c.ranked || c.stamp < f.stamp {
					continue
				}
				later := false
				for rj := range st.records {
					g := st.records[rj]
					if g.parseErr != nil || !g.ranked || g.intent.op != oauthOpRemove {
						continue
					}
					if g.stamp > f.stamp && g.stamp <= c.stamp {
						later = true
						break
					}
				}
				if !later && c.stamp > mark {
					mark = c.stamp
				}
			}
			proof[ri] = mark
		}
		covered := func(c oauthCopyFile) bool {
			for ri := range st.records {
				if stood[ri] && proof[ri] >= c.stamp {
					return true
				}
			}
			return false
		}
		// wanted[ci] is whether the removal that parked this copy did NOT stand:
		// its bytes are the instance's credential. Everything else ranked is
		// debris of a removal that stood - or of a name nothing carries - and is
		// swept. A copy of a name a rename owns is left for that rename's
		// completion, and a copy whose stamp cannot be ordered is left untouched.
		wanted := make([]bool, len(st.copies))
		for ci, c := range st.copies {
			if !c.ranked {
				continue
			}
			if !st.parkedBy[ci] {
				wanted[ci] = carries
				continue
			}
			wanted[ci] = !covered(c)
		}
		var nameProblems []string
		// settled is whether every step this name needed actually ran. A report is
		// not an unsettled step: the lone-credential report below rides on a delete
		// that succeeded, and keeping the record for it would make the next start
		// report the same thing again, forever.
		settled := true
		newest := -1
		for ci, c := range st.copies {
			if wanted[ci] && (newest < 0 || c.stamp > st.copies[newest].stamp) {
				newest = ci
			}
		}
		restoredThis := false
		if newest >= 0 {
			back, more := putBackParkedCopy(dir, stateDir, st.copies[newest])
			nameProblems = append(nameProblems, more...)
			if len(more) > 0 {
				settled = false
			}
			if back {
				restoredThis = true
				restored = true
				// The copies the one just put back supersedes are credentials the
				// instance no longer has: the record path now holds the newest
				// bytes, so an older parked copy of the same name is debris nothing
				// restores and nothing else would collect.
				for ci, c := range st.copies {
					if !c.ranked || ci == newest || c.stamp >= st.copies[newest].stamp {
						continue
					}
					if err := os.Remove(filepath.Join(dir, c.name)); err != nil && !errors.Is(err, os.ErrNotExist) {
						settled = false
						nameProblems = append(nameProblems, fmt.Sprintf("delete the parked copy %s of %q, superseded by the copy put back as its record (%v)", c.name, name, err))
					}
				}
			}
		}
		// Sweep the debris: the copies whose removal stood (or whose name no
		// config carries). A copy that could have been the last bytes of a
		// credential - one no config-backed record parked, with nothing at the
		// record path - is REPORTED as it goes, because a delete must not be
		// silent when the bytes could have been put back by hand.
		for ci, c := range st.copies {
			if !c.ranked || wanted[ci] {
				continue
			}
			if restoredThis && c.stamp < st.copies[newest].stamp {
				// Already taken by the superseded delete above.
				continue
			}
			lone := st.loneSweep[ci]
			recordPathFree := false
			if _, statErr := os.Lstat(authopenai.AuthFilePath(stateDir, name)); errors.Is(statErr, os.ErrNotExist) {
				recordPathFree = true
			}
			if err := os.Remove(filepath.Join(dir, c.name)); err != nil {
				if !errors.Is(err, os.ErrNotExist) {
					settled = false
					nameProblems = append(nameProblems, fmt.Sprintf("delete the parked copy %s of %q, which no removal wants any more (%v)", c.name, name, err))
				}
				continue
			}
			if lone && recordPathFree {
				nameProblems = append(nameProblems, fmt.Sprintf("deleted the parked copy %s of %q: nothing else of that credential is on disk and no removal wants it, so those bytes were the last copy a human could have put back by hand", c.name, name))
			}
		}
		// A stored key staged beside a removal's record is the stored half of what
		// that removal cleared: put it back when the removal did NOT stand (the
		// staging is the only copy of the bytes left), and let it go with the
		// record when it did.
		for ri := range st.records {
			f := st.records[ri]
			if f.parseErr != nil || !f.ranked || f.intent.op != oauthOpRemove || stood[ri] {
				continue
			}
			if _, serr := os.Lstat(removedKeyPath(f.path)); serr != nil {
				continue
			}
			back, more := restoreStagedRemovalKey(f.path, name, store)
			nameProblems = append(nameProblems, more...)
			if back {
				// The store changed, so the registry's instance list - computed at
				// load, before this key was back - is missing any instance whose only
				// credential is that key. The caller reloads on this flag, the way it
				// does for a record this pass put back (main.go).
				restored = true
			}
			if len(more) > 0 {
				settled = false
			}
		}
		// The records are spent once the name they describe is resolved. A pass
		// that could not settle one keeps it, so the next start tries again.
		if settled {
			for _, f := range st.records {
				if f.parseErr != nil {
					continue
				}
				if f.intent.op == oauthOpRemove {
					if err := spendStagedRemovalKey(f.path); err != nil {
						nameProblems = append(nameProblems, fmt.Sprintf("spend the stored key staged beside the record %s of %q (%v)", f.path, name, err))
						continue
					}
				}
				if err := removeOAuthIntent(f.path); err != nil {
					nameProblems = append(nameProblems, fmt.Sprintf("spend the intent %s of %q, whose work is resolved (%v)", f.path, name, err))
				}
			}
		}
		problems = append(problems, nameProblems...)
	}
	if len(problems) > 0 {
		return restored, fmt.Errorf("startup OAuth recovery could not finish: %s", strings.Join(problems, ", "))
	}
	return restored, nil
}

// putBackParkedCopy puts one parked copy back at its instance's record path,
// when that path is free, and reports what it could not do. Bytes at the record
// path are the instance's record now - a later sign-in, a landed rollback - so
// the copy stays beside it as debris rather than replacing it; that is not a
// problem to report, and the record that owns the copy stays for a later pass to
// settle. A check that failed, or a move that would not land, is reported.
func putBackParkedCopy(dir, stateDir string, c oauthCopyFile) (bool, []string) {
	path := authopenai.AuthFilePath(stateDir, c.inst)
	switch _, statErr := os.Lstat(path); {
	case statErr == nil:
		return false, nil
	case !errors.Is(statErr, os.ErrNotExist):
		return false, []string{fmt.Sprintf("check %s before putting %s back (%v)", path, c.name, statErr)}
	}
	if err := renameNoReplace(filepath.Join(dir, c.name), path); err != nil {
		return false, []string{fmt.Sprintf("put %s back as %s (%v)", c.name, path, err)}
	}
	return true, nil
}

// reclaimOAuthAsides deletes the parked copies of one name's OAuth record that a
// removal of that name has just made unwanted: the copy the call parked and any
// an earlier removal of the name left behind. It is run once the removal has
// stood, after the reload that drops the instance from the registry, and it
// takes copies FILED UNDER THAT NAME ONLY.
//
// The removal's intent record is what keeps a crash before these deletes from
// resurrecting the removal: it is still on disk at its commit phase, and the next
// start sweeps the copies a landed record leaves - reporting the credential-only
// ones - instead of putting them back (restoreUncommittedOAuthAsides). The delete
// is the whole point here, and a failure is reported rather than ignored: the
// removal stands, and the caller is the only one left who can delete the copy the
// report names. The failure reaches the caller as the removal's own
// (removeAppliedError), because the removal stood and every client has to drop
// the row.
//
// The name is the whole of what makes a copy safe to take. A removal is the only
// call that can know the bytes are no longer wanted, so a copy under any other
// name - including one whose own removal failed after parking the record and
// could not put it back (restoreFailedRemoval) - may be the last surviving
// credential of an instance the user still has. Taking it from here would turn a
// repairable file-level failure into a forced sign-in, so it is left where it is,
// and the failure that stranded it named it at the time. Removing that name again
// is what reclaims it, because a removal of the name is the caller saying the
// credential is not wanted any more.
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
		return fmt.Errorf("collect the OAuth copies the removal of %s parked: %w", name, err)
	}
	var remaining []string
	for _, e := range entries {
		inst, _, aside := parseOAuthAside(e.Name())
		// A directory is skipped: this takes the copies the hub itself parked, and
		// deleting a directory's contents is not something a removal may do.
		// setAsideOAuthFile refuses anything but a regular file at the record path,
		// so one filed under a copy's name was not written by a removal.
		if e.IsDir() || !aside || inst != name {
			continue
		}
		path := filepath.Join(dir, e.Name())
		if delErr := c.auth.deleteAside(path); delErr != nil && !errors.Is(delErr, os.ErrNotExist) {
			remaining = append(remaining, fmt.Sprintf("%s (%v)", path, delErr))
		}
	}
	if len(remaining) > 0 {
		return fmt.Errorf("a credential the removal of %s parked is still on disk, and deleting it is what takes it away: %s", name, strings.Join(remaining, ", "))
	}
	// Every removal record of this name is spent with them: the copies they
	// describe are gone, so what is left for a later start to do is nothing at
	// all. A record that does not read back is left where it is - recovery keeps
	// and reports it - and so is a record of any other mutation (a rename's
	// record is the rename's to finish, and this call knows nothing about it).
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		inst, _, _, ok := parseOAuthIntent(e.Name())
		if !ok || inst != name {
			continue
		}
		path := filepath.Join(dir, e.Name())
		raw, rerr := os.ReadFile(path)
		if rerr != nil {
			continue
		}
		i, perr := parseOAuthIntentRecord(raw, path)
		if perr != nil || i.op != oauthOpRemove {
			continue
		}
		if rerr := removeOAuthIntent(path); rerr != nil {
			remaining = append(remaining, fmt.Sprintf("%s (%v)", path, rerr))
		}
	}
	if len(remaining) > 0 {
		return fmt.Errorf("a credential the removal of %s parked is still on disk, and deleting it is what takes it away: %s", name, strings.Join(remaining, ", "))
	}
	return nil
}

// oauthKeySidecar is the suffix that stages a removal's stored key beside its
// intent record. The record can bring the OAuth half of a crashed removal back;
// the stored key lives in credentials.toml, which the removal clears before its
// config write commits, so only a copy of the bytes can bring that half back.
// The staging is fsynced, spent once the key is back in the store or the removal
// stood, and deliberately not one of the classified record/copy shapes: recovery
// pairs it with its record by name and every copy rule skips it.
const oauthKeySidecar = ".key"

// removedKeyPath names the file that stages the stored key of the removal whose
// record is intentPath. The name does NOT follow the record's phase: the record
// is renamed to its landed name at its commit point, and a staging whose own name
// travelled with that rename would be left behind by a crash between the two -
// filed under a name no record pairs with, where the copy rules skip it and no
// pass ever spends it. Both phases of one record therefore share one staging
// name: the in-flight record's own name plus oauthKeySidecar.
func removedKeyPath(intentPath string) string {
	dir, name := filepath.Split(intentPath)
	if _, _, landed, ok := parseOAuthIntent(name); ok && landed {
		// The parser reads the TRAILING phase marker (an instance name may itself
		// hold the marker, which is why the grammar reads names that way), so the
		// splice takes the same one: directory, instance and stamp are kept, and
		// only the phase marker becomes the in-flight one.
		if i := strings.LastIndex(name, oauthLandedMarker); i >= 0 {
			name = name[:i] + oauthIntentMarker + name[i+len(oauthLandedMarker):]
		}
	}
	return filepath.Join(dir, name) + oauthKeySidecar
}

// stageRemovalKey durably stages the stored key a removal is about to clear.
func stageRemovalKey(intentPath, key string, present bool) error {
	if intentPath == "" || !present {
		return nil
	}
	path := removedKeyPath(intentPath)
	dir := filepath.Dir(path)
	f, err := os.CreateTemp(dir, "staged-key-*.tmp")
	if err != nil {
		return err
	}
	tmp := f.Name()
	if _, werr := f.WriteString(key); werr != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return werr
	}
	// The bytes are the only copy of the key the moment the store's entry is
	// cleared, so they are fsynced before the rename that publishes them.
	if serr := f.Sync(); serr != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return serr
	}
	if cerr := f.Close(); cerr != nil {
		_ = os.Remove(tmp)
		return cerr
	}
	if merr := os.Chmod(tmp, 0o600); merr != nil {
		_ = os.Remove(tmp)
		return merr
	}
	if rerr := os.Rename(tmp, path); rerr != nil {
		_ = os.Remove(tmp)
		return rerr
	}
	return syncDir(dir)
}

// spendStagedRemovalKey removes the key staged beside a removal's record. A
// missing staging is not an error: a removal of an instance with no stored key
// stages nothing, and a staging already spent lost nothing.
func spendStagedRemovalKey(intentPath string) error {
	if intentPath == "" {
		return nil
	}
	if err := os.Remove(removedKeyPath(intentPath)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// restoreStagedRemovalKey puts a removal's staged stored key back into the LIVE
// store, for a removal that did not stand: the key is the instance's again and
// the staging is the only copy of the bytes left. A key filed since the removal
// started is the user's newer one and is never overwritten. What it could not do
// is returned, so a staging that will not come back is never silent.
//
// It also reports whether the store now holds the key: a key that came back is a
// credential change like a record put back, and an instance whose ONLY credential
// is that key - a credential-only instance, which has no providers.toml entry to
// derive a row from - is missing from the instance list a registry computed
// before the key returned. The caller owes the reload such a change asks for
// (main.go's second Reload), so the answer travels back with the problems rather
// than being inferred from them: a staging that was absent, or one whose key the
// store already held, changed nothing and needs no reload.
func restoreStagedRemovalKey(intentPath, name string, store *credentials.Store) (bool, []string) {
	path := removedKeyPath(intentPath)
	raw, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, []string{fmt.Sprintf("read the stored key %s staged for %q (%v)", path, name, err)}
	}
	if store == nil {
		return false, []string{fmt.Sprintf("put the stored key staged for %q back (%s): the credentials store was not provided", name, path)}
	}
	if _, filed := store.Get(name); filed {
		return false, nil
	}
	if err := store.Set(name, string(raw)); err != nil {
		return false, []string{fmt.Sprintf("put the stored key staged for %q back (%s: %v)", name, path, err)}
	}
	return true, nil
}

// resolveRemovalIntent settles a removal's intent record after its rollback: a
// record whose name still has a parked copy stays, returned to its in-doubt name
// - the truth about a removal that did not finish moving the credential back,
// and what a credential-only removal's recovery reads - and one whose copies are
// all back where they belong is spent. The delete is best-effort: a
// record with no copy left is spent by the next start (its sweep has nothing to
// act on and removes it), not a reason to fail a rollback whose durable work
// landed. What it could not do is returned, so a phase that will not go back to
// started is never silent.
func (c *hubInstancesController) resolveRemovalIntent(intentPath, name string) []string {
	if intentPath == "" {
		return nil
	}
	// The stored key staged beside this record is spent exactly when it cannot be
	// needed any more: the store holds the key again, so the rollback put it back.
	// While it does not, the record and the staging are BOTH kept - the record is
	// the evidence the next start reads and the staging is the only copy of the
	// bytes - so startup can put the stored key back.
	if _, serr := os.Lstat(removedKeyPath(intentPath)); serr == nil {
		if _, filed := c.auth.creds.Get(name); !filed {
			kept := []string{fmt.Sprintf("keep the removal record %s and the stored key staged beside it (%s), so the next start puts the stored key of %q back: this rollback did not", intentPath, removedKeyPath(intentPath), name)}
			// The record is kept, but its NAME still has to say what startup must
			// do with the copy it parked: a record left landed reads as a removal
			// that stood, and a standing credential-only removal's copy is swept -
			// so the rollback returns the record to its in-doubt name even though
			// it cannot spend it.
			if _, uerr := unlandOAuthIntent(intentPath); uerr != nil {
				kept = append(kept, fmt.Sprintf("return the removal record %s of %q to its in-doubt name, so the copy it parked is put back rather than swept (%v)", intentPath, name, uerr))
			}
			return kept
		}
		if err := spendStagedRemovalKey(intentPath); err != nil {
			return []string{fmt.Sprintf("spend the stored key staged beside the removal record %s of %q, which the rollback has put back (%v)", intentPath, name, err)}
		}
	}
	left, err := c.parkedCopies(name)
	if err != nil {
		return []string{fmt.Sprintf("list %q's parked copies to settle its removal record %s (%v)", name, intentPath, err)}
	}
	if len(left) == 0 {
		_ = removeOAuthIntent(intentPath)
		return nil
	}
	if _, err := unlandOAuthIntent(intentPath); err != nil {
		return []string{fmt.Sprintf("return the removal record %s of %q to its in-doubt name, so the copy it parked is put back rather than swept (%v)", intentPath, name, err)}
	}
	return nil
}

// rollBackFailedRemoval undoes a removal that failed at or after its commit
// point - a reload that could not resolve the config it wrote, or a commit point
// whose phase record could not land - and reports what it could not put back.
// Both failures share this one path so they cannot drift apart. When the config
// was changed it writes before back first; a rollback write that cannot land
// leaves the entry gone - the file on disk is the one the REMOVAL wrote - and is
// reported as a persisted removal once the registry is reloaded over that file
// (a failed phase record never parked the registry, so without the reload the hub
// would keep serving an instance providers.toml no longer carries). It restores
// the credentials this call deleted and the record from the copy it parked
// (restoreFailedRemoval renames it back), settles the removal's intent record
// (resolveRemovalIntent), then reloads. On the rollback-write failure that reload
// is over the removal's file; on every other path it is over the restored
// pre-removal file, where the retry is the recovery for a failed reload that
// parked the registry on the implicit-only view a failed load leaves.
// configChanged is what selects both the rollback write and the wording of a
// retry that fails, so a caller can tell whether the config the rollback
// restored loads.
func (c *hubInstancesController) rollBackFailedRemoval(before *registry.Layer, name, storedKey string, hasStoredKey bool, intentPath, ownAside string, configChanged bool, frame string, supplies removalSupply, cause error) error {
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
			// brought us here does not do it: a failed phase record never parked
			// the registry, so it still holds the pre-removal view, and a failed
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
			// credential stays deleted and its copies are reclaimed; the removal
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
				// The removal stands, so the plaintext key staged beside its
				// record stands with it: the credential stays deleted and those
				// bytes are debris of a removal that stood, exactly like the copies
				// just reclaimed. Resolving the record instead would read the
				// staging as the only copy of bytes a later start must put back -
				// restoring the credential this branch exists to keep deleted - and
				// would even return the landed record to its in-doubt name so that
				// start acts on it. The record keeps the phase the removal's commit
				// point gave it, and a staging that will not come away is reported:
				// a plaintext key left in the auth directory is what this branch
				// must not leave behind.
				if serr := spendStagedRemovalKey(intentPath); serr != nil {
					remnant = fmt.Errorf("%w; the stored key staged beside the removal record %s of %q could not be spent with the standing removal (%w)", remnant, intentPath, name, serr)
				}
			} else {
				_, remnant = c.restoreFailedRemoval(name, storedKey, hasStoredKey, ownAside, remnant, "the entry is gone from the config", supplyAny)
				if more := c.resolveRemovalIntent(intentPath, name); len(more) > 0 {
					remnant = fmt.Errorf("%w; %s", remnant, strings.Join(more, " and "))
				}
			}
			// The removal stands in the file, so it is an applied write however
			// this call ends: the other clients are still listing an instance that
			// is gone. Unconditional, and re-marked here rather than left to
			// restoreFailedRemoval - which clears the mark when it puts every
			// credential back - because this branch applies to the config whatever
			// the credential restore does.
			c.applied.markApplied()
			if reloadErr := c.reg.Reload(); reloadErr != nil {
				return removeApplied(fmt.Errorf("%w; the registry could not be reloaded over the removal the config still carries either (%w)", remnant, reloadErr))
			}
			return removeApplied(remnant)
		}
	}
	// The credentials go back before the reload below, because a load resolves
	// each instance's credential from the stores: one that runs while this
	// call's deletions are still missing caches "none" as the source of the
	// instance the caller still has, and putting the key back afterwards does
	// not rebuild that view. The pane would then show a stored key beside no
	// active source, and the next launch would be refused for missing
	// credentials, until some later write happened to reload again.
	// The cause is neutral - "failed", not "was rolled back" - because this
	// rollback only lands when restoreFailedRemoval answers carried. When the
	// layer that carries the instance cannot be put back, that answer is the
	// standing frame, and a cause that already said "was rolled back" would have
	// the one sentence report both outcomes at once ("removing X was rolled
	// back: ...; the removal stands, ..."). The rollback wording is built below,
	// once the restore has actually carried the instance. frame and supplies
	// come from Remove, which classified the row under both locks: the registry
	// this call sees may already be parked on the implicit-only view a failed
	// load leaves, and re-asking it here would answer about the wrong world.
	carried, restored := c.restoreFailedRemoval(name, storedKey, hasStoredKey, ownAside,
		fmt.Errorf("removing %q failed: %w", name, cause), frame, supplies)
	if more := c.resolveRemovalIntent(intentPath, name); len(more) > 0 {
		restored = fmt.Errorf("%w; %s", restored, strings.Join(more, " and "))
	}
	if !carried {
		// The layer that carries the instance did not come back, so the removal
		// stands (restoreFailedRemoval bound the frame and the discriminator to
		// that answer). But the CONFIG did come back - write(before) above
		// succeeded, or nothing was ever written - so the instance the restored
		// config names still exists, and the copies this removal parked belong to
		// a removal that did not stand. resolveRemovalIntent returned them to the
		// shape startup recovery reads, so the credential is not stranded beside
		// an instance that cannot authenticate from its record path.
		//
		// The registry is reloaded over the config the rollback restored, the
		// way the carried path does: the failure that brought us here parked it
		// on the view of the file the removal wrote (or of the file it could not
		// load), and the file that stands now is the pre-removal one.
		if reloadErr := c.reg.Reload(); reloadErr != nil {
			return fmt.Errorf("%w; the registry could not be reloaded over the config the rollback restored either (%w)", restored, reloadErr)
		}
		return restored
	}
	rolledBack := fmt.Errorf("removing %q was rolled back: %w", name, cause)
	if leftovers, ok := errors.AsType[removalLeftoversError](restored); ok {
		// Two separate facts, and the error has to carry both. The instance is
		// configured again - the layer that carries it is back - so the removal is
		// a rollback and the message says so. A credential is still gone, though: a
		// deleted layer that could not be put back is a change every other client's
		// credential status for this name is stale against, so the credential
		// deletion's applied mark stays on the controller, which is what makes
		// instanceWrite broadcast evener/auth/updated.
		rolledBack = fmt.Errorf("%w; some credentials were not put back: %w", rolledBack, leftovers)
	}
	restored = rolledBack
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
