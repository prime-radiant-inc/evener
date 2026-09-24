package hub

import (
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
	// Resolve the row's instance once: the endpoint fingerprint and the
	// credential-configuration revision both describe this one generation of
	// providers.toml, and instanceStatus derives the revision from this
	// resolution instead of resolving the name a second time.
	resolved, resolvedOK := resolvedRowInstance(r, inst)
	// A bare controller (a construction with no auth controller wired) still
	// describes the registry it holds: there is no credential layer to derive a
	// status from, so the row carries an empty one rather than dereferencing a
	// nil controller.
	var status appwire.AuthStatusResponse
	if c.auth != nil {
		status = c.auth.instanceStatusKeyed(key, inst, resolved)
	}
	entry := appwire.InstanceEntry{
		Name:                inst.Name,
		Base:                inst.Base,
		ProviderID:          inst.ProviderID,
		Protocol:            inst.Protocol,
		Surface:             inst.Surface,
		Auth:                inst.Auth,
		BaseURL:             sanitizeEndpointURL(inst.BaseURL),
		EndpointFingerprint: rowEndpointFingerprint(key, inst, resolved, resolvedOK),
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
		ConfigRevision:      status.ConfigRevision,
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

// destinationInstance reports the instance behind inst when it has a
// destination this hub can name at all: not hidden, resolvable, and carrying a
// base URL. Whether the hub can *key* that destination's fingerprint is a
// separate question - the key file can be unreadable while the destination is
// perfectly real - and the credential guards must not read one as the other
// (endpointFingerprintFor's empty answer conflates them; app_auth.go's
// endpointHasDestination asks this question instead).
func destinationInstance(r *registry.Registry, inst registry.Instance) (registry.Resolved, bool) {
	if inst.Hidden {
		return registry.Resolved{}, false
	}
	resolved, ok := resolvedRowInstance(r, inst)
	if !ok || !hasRowDestination(inst, resolved) {
		return registry.Resolved{}, false
	}
	return resolved, true
}

// resolvedRowInstance resolves inst once, for the row fields that describe one
// generation of providers.toml: the endpoint fingerprint and the
// credential-configuration revision. ok is false when the name does not resolve
// here at all, and the zero Resolved that travels with a false carries no
// revision (CredentialConfigRevisionResolved). A listing row resolves with this
// and hands the result to both consumers, so it resolves the name once instead
// of once per field.
func resolvedRowInstance(r *registry.Registry, inst registry.Instance) (registry.Resolved, bool) {
	if r == nil {
		return registry.Resolved{}, false
	}
	resolved, err := r.ResolveInstance(inst.Name)
	if err != nil {
		return registry.Resolved{}, false
	}
	return resolved, true
}

// hasRowDestination reports whether a resolved instance names a destination
// this hub can fingerprint at all: not hidden, and carrying a base URL.
// Whether the hub can *key* that fingerprint is a separate question - the key
// file can be unreadable while the destination is perfectly real - which is why
// destinationInstance's callers ask this of the resolved value rather than of
// the key (see its doc).
func hasRowDestination(inst registry.Instance, resolved registry.Resolved) bool {
	return !inst.Hidden && strings.TrimSpace(resolved.Transport.BaseURL) != ""
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
	resolved, ok := resolvedRowInstance(r, inst)
	return rowEndpointFingerprint(key, inst, resolved, ok)
}

// rowEndpointFingerprint is destinationFingerprintKeyed over a resolution the
// caller already holds, so a row that resolved inst for its
// credential-configuration revision does not resolve the name again here. Its
// answer is destinationFingerprintKeyed's exactly: empty when the name did not
// resolve or names no destination this hub can fingerprint.
func rowEndpointFingerprint(key []byte, inst registry.Instance, resolved registry.Resolved, ok bool) string {
	if !ok || !hasRowDestination(inst, resolved) {
		return ""
	}
	return fingerprintWithKey(key, hubcore.DestinationIdentity(resolved))
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

// requireCredentialStore refuses a mutation that has to read or move the
// credentials store when this controller has no usable one. hubCredentialStore
// leaves creds nil when credentials.toml cannot be loaded, and Store.Get, Move
// and Clear all take the receiver's lock, so a nil store panics inside the RPC
// handler. That unreadable store is a first-class state - the credential
// surfaces answer from it rather than dereferencing it (hubAuthController's
// storedKey and credentialsUnavailable) - so the instance mutations answer the
// same way: typed, and before any providers.toml write, rather than panicking or
// silently leaving a credential behind under a name they just gave up.
//
// Every caller runs requireAuth first, so c.auth is non-nil here.
func (c *hubInstancesController) requireCredentialStore() error {
	if c.auth.creds == nil {
		return c.auth.credentialsUnavailable()
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
		// The destination check below and the move at the end of this call both
		// read and move the credentials store, and neither can be done without
		// one: an unreadable credentials.toml (creds nil) would panic a direct
		// Store.Get, and - worse than the panic - a rename that reached
		// writeAndReload and then failed to move the key would leave the key
		// stranded under the old name after providers.toml had moved. So the
		// refusal is made here, before the destination check and long before any
		// config change is persisted, rather than after the file is re-keyed.
		if err := c.requireCredentialStore(); err != nil {
			return err
		}
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
		// A non-rename edit has no rename to report, so it keeps the error as
		// it came.
		if renaming && c.applied.peekApplied() {
			return renamePersistedError{err}
		}
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
			// mark is what the handler broadcasts on.
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
	// Through storedKey, which answers "no key" rather than panicking when the
	// hub has no usable store: the rename flow refuses such a rename up front
	// (requireCredentialStore), but this read must be safe wherever it is
	// reached from. "Not found" is still the only signal that nothing is there,
	// so a record that exists but does not read back still counts as present.
	if _, ok := c.auth.storedKey(name); ok {
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
// Nothing it calls takes credMu, which the caller still holds.
func (c *hubInstancesController) moveCredentials(oldName, newName string) error {
	var problems []string
	// A backstop for the rename flow's own up-front refusal: with no usable
	// store there is no Move to make, and dereferencing the nil store would
	// panic. The flow refuses before it writes providers.toml; this keeps the
	// primitive itself honest if a caller ever reaches it another way.
	if err := c.requireCredentialStore(); err != nil {
		return err
	}
	// One persist, so the key is never briefly filed under both names or
	// neither: a copy-then-clear pair whose second half failed would leave
	// the old name resolving a credential the config no longer names.
	if err := c.auth.creds.Move(oldName, newName); err != nil {
		problems = append(problems, fmt.Sprintf("stored key not copied: %v", err))
	}
	record, err := c.auth.loadAuth(c.auth.stateDir, oldName)
	switch {
	case errors.Is(err, authopenai.ErrAuthNotFound):
	case err != nil:
		problems = append(problems, fmt.Sprintf("OAuth record not read: %v", err))
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
		// The rename reached the file, so it is as persisted as one that ended
		// cleanly: renamePersistedError is what the RPC handler reads to hand
		// the client the discriminator, and the config write the rename landed
		// marked the applied state the handler broadcasts on.
		return renamePersistedError{fmt.Errorf("renamed %q to %q, but: %s", oldName, newName, strings.Join(problems, "; "))}
	}
	return nil
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
	inst, ok := c.reg.Get().Instance(name)
	if !ok {
		return appwire.InvalidParams(fmt.Sprintf("instance %q not found", name))
	}
	if environmentBacked(inst) {
		return appwire.InvalidParams(fmt.Sprintf("%s exists from the environment (%s); %s", name, describeImplicit(inst), removalRemedy(inst)))
	}

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
		// The same caller-fixable condition the pre-lock check classified as
		// InvalidParams, so the locked re-check answers the same way rather than
		// letting a concurrent credential clear change the wire class of one
		// refusal (see Remove's doc comment).
		return appwire.InvalidParams(fmt.Sprintf("%s exists from the environment (%s); %s", name, describeImplicit(locked), removalRemedy(locked)))
	}

	// The confirmation this removal carries names the row the client listed, so
	// a name another client has re-pointed since (a removal and a recreation
	// under it, or an edit to its base_url) is refused rather than having its
	// replacement instance removed. Asked here, under the exclusive lock, so it
	// describes the instance the cleanup below acts on.
	if err := c.auth.verifyEndpointFingerprintWithKey(name, params.ExpectedEndpointFingerprint, key, keyErr); err != nil {
		return err
	}

	// The credential cleanup below reads and clears the store, and a removal
	// that cannot see it would either panic (a direct Store.Get) or leave a
	// stored key behind under a name nothing curates once the config entry is
	// gone. As on the rename path, the refusal is made before anything is
	// deleted, so no half-removal reaches providers.toml.
	if err := c.requireCredentialStore(); err != nil {
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
	storedKey, hasStoredKey := c.auth.storedKey(name)
	oauthBytes, hasOAuth, err := c.captureOAuthFile(name)
	if err != nil {
		return err
	}

	// Whether the config authored this instance is read before the cleanup, so
	// its failure below is classified by the same question every later branch
	// asks: an authored entry resolves from the config, an implicit instance
	// from the layer supplyOf names. A pure map read, so moving it ahead of the
	// cleanup changes nothing it observes.
	_, authored := before.Providers[name]

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
		_, restoreErr := c.restoreFailedRemoval(name, storedKey, hasStoredKey && removed.storedKey, oauthBytes, hasOAuth && removed.oauthRecord, err, frame, supplies)
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
	configChanged := authored || l.Default != before.Default
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
			restored, restoreErr := c.restoreFailedRemoval(name, storedKey, hasStoredKey, oauthBytes, hasOAuth, err, frame, supplies)
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
	if err := c.reg.Reload(); err != nil {
		if !configChanged {
			// Nothing was written, so there is no file to put back: restoring
			// the credentials this call deleted is the whole rollback, and a
			// write here would create the providers.toml the guard above
			// exists to avoid. The question is what actually carries this
			// instance - it is implicit (nothing was authored), so that is the
			// layer it resolved from, not "every file this call deleted came
			// back" (supplyOf). When that layer is back the instance is
			// configured again and the removal was rolled back, even if a stray
			// other credential could not be restored; only when the carrying
			// layer itself could not be put back does the removal stand, and
			// reporting a rolled-back removal as a standing one - and skipping
			// the reload that would republish the row - would tell the caller
			// the opposite of the truth. The reload is retried only once the
			// credentials are back: the failure above parked the registry on
			// the implicit-only view a failed load leaves (writes refused, this
			// row missing), and the state the file describes is unchanged, so a
			// second attempt is the recovery rather than a repetition.
			cause := fmt.Errorf("removing %q failed: %w", name, err)
			restored, restoreErr := c.restoreFailedRemoval(name, storedKey, hasStoredKey, oauthBytes, hasOAuth,
				cause, "the removal stands", supplyOf(locked))
			if !restored {
				return restoreErr
			}
			rolledBack := fmt.Errorf("removing %q was rolled back: %w", name, err)
			if leftovers, ok := errors.AsType[removalLeftoversError](restoreErr); ok {
				// Two separate facts, and the error has to carry both. The
				// instance is configured again - the layer that carries it is
				// back - so the removal is a rollback and the message says so.
				// A credential is still gone, though: a deleted layer that
				// could not be put back is a change every other client's
				// credential status for this name is stale against, so the
				// credential deletion's applied mark stays on the controller,
				// which is what makes instanceWrite broadcast
				// evener/auth/updated.
				rolledBack = fmt.Errorf("%w; some credentials were not put back: %w", rolledBack, leftovers)
			}
			if reloadErr := c.reg.Reload(); reloadErr != nil {
				return fmt.Errorf("%w; the registry could not be reloaded either, so instance writes stay refused until it can be (%w)", rolledBack, reloadErr)
			}
			return rolledBack
		}
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
			// The rollback could not land, so the entry stays gone and only the
			// credentials can be put back - under the name the caller re-authors
			// once this removal is reported as standing. No reload: the failure
			// above already left the registry on the implicit-only view a load
			// of this file produces, and writing is what is broken, not loading.
			// The removal stands in the file, so it is an applied write
			// however this call ends: the other clients are still listing an
			// instance that is gone. Unconditional: unlike the other call
			// sites, this one applies to the config regardless of whether the
			// credential restore below also fails, so the applied state is
			// re-marked here rather than left to restoreFailedRemoval, which
			// clears it when it puts every credential back.
			_, standingErr := c.restoreFailedRemoval(name, storedKey, hasStoredKey, oauthBytes, hasOAuth,
				fmt.Errorf("%w; the rollback could not be written, so the removal stands in the config (%w)", err, restoreErr),
				"the entry is gone from the config", supplyAny)
			c.applied.markApplied()
			return removeApplied(standingErr)
		}
		// The credentials go back before the reload below, because a load
		// resolves each instance's credential from the stores: one that runs
		// while this call's deletions are still missing caches "none" as the
		// source of the instance the caller still has, and putting the key back
		// afterwards does not rebuild that view. The pane would then show a
		// stored key beside no active source, and the next launch would be
		// refused for missing credentials, until some later write happened to
		// reload again.
		// The config rolled back cleanly, so this would be a plain refusal -
		// unless the credential restore itself failed, in which case a stored
		// key or OAuth record stays deleted under an instance that is back in
		// the config, which every other client's status for it is stale
		// against; restoreFailedRemoval wraps its own result in that case.
		//
		// Which layer the restore has to put back - and so what a failed
		// restore means - depends on what held the instance up. An authored
		// entry is back in the config, so the instance resolves from it whatever
		// happens to its credentials: the configured frame with supplyAny's
		// strict question is the honest report. An implicit instance has no
		// entry to fall back on - configChanged is true only because a `default`
		// pointer named it - so the layer supplyOf names is what carries it.
		// When that layer is a credential this call deleted, a restore that
		// cannot put it back leaves the instance unresolvable: the removal
		// stands, the frame must say so, and the discriminator goes with it
		// because instanceRemoveError reads it to tell the client the removal
		// applied rather than leaving it to retry a removal that stands.
		// supplyConfig is the provider carrying the row, with no credential of
		// the user's at stake, so it keeps the configured frame.
		frame, supplies := "the instance is still configured", supplyAny
		if !authored {
			supplies = supplyOf(locked)
			if supplies != supplyConfig {
				frame = "the removal stands"
			}
		}
		// The cause is neutral - "failed", not "was rolled back" - because this
		// rollback only lands when restoreFailedRemoval answers carried. When the
		// layer that carries the instance is a credential this call deleted and
		// the restore cannot put it back, that answer is the standing frame, and
		// a cause that already said "was rolled back" would have the one sentence
		// report both outcomes at once ("removing X was rolled back: ...; the
		// removal stands, ..."). The rollback wording is built below, once the
		// restore has actually carried the instance, the way the !configChanged
		// sibling builds it.
		restored, restoreErr := c.restoreFailedRemoval(name, storedKey, hasStoredKey, oauthBytes, hasOAuth,
			fmt.Errorf("removing %q failed: %w", name, err), frame, supplies)
		if restored {
			// The carrying layer is back, so the config this rollback restored is
			// the pre-removal one and the removal was rolled back - the message
			// says so. A stray other layer that stayed deleted is still a change
			// every other client's credential status for this name is stale
			// against, so it is folded in with the applied mark, exactly as the
			// !configChanged sibling folds its leftovers.
			rolledBack := fmt.Errorf("removing %q was rolled back: %w", name, err)
			if leftovers, ok := errors.AsType[removalLeftoversError](restoreErr); ok {
				rolledBack = fmt.Errorf("%w; some credentials were not put back: %w", rolledBack, leftovers)
			}
			restoreErr = rolledBack
		}
		// The file this rollback put back is the pre-removal one, and the reload
		// that just failed read the file this call wrote - so if the config was
		// already unresolvable before the removal (Remove's own guard reads the
		// registry, which had not been reloaded since and so never saw the change
		// that broke it), this reload fails on the very file the rollback
		// restored. The registry stays on the implicit-only view a failed load
		// leaves and refuses instance writes until the file loads (registry.go's
		// WritesRefused, spec §10). Reinstating the previous registry view would
		// instead have the hub serve and rewrite a config it cannot load, which
		// is what that refusal exists to prevent - so the failure reports what is
		// left: the rollback landed, and the config still does not load. A caller
		// told only that the removal "was rolled back" would read the hub as
		// healthy.
		if reloadErr := c.reg.Reload(); reloadErr != nil {
			return fmt.Errorf("%w; the config it restored does not load either, so instance writes stay refused until it does (%w)", restoreErr, reloadErr)
		}
		return restoreErr
	}
	return nil
}

// captureOAuthFile reads the OAuth state file a removal's cleanup is about to
// unlink, so a later failure can write those bytes back. It captures the raw
// bytes rather than the parsed record because DeleteAuth deletes by path: a
// record the hub cannot parse (corrupt) or validate is one it will still
// delete, and only the bytes can put it back. A missing file is (nil, false,
// nil); one that exists but cannot be read is refused here, before anything is
// deleted, because the removal cannot promise to restore what it cannot read.
func (c *hubInstancesController) captureOAuthFile(name string) ([]byte, bool, error) {
	raw, err := os.ReadFile(authopenai.AuthFilePath(c.auth.stateDir, name))
	if err == nil {
		return raw, true, nil
	}
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	return nil, false, fmt.Errorf("remove %s: read OAuth state to preserve it: %w", name, err)
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
func (c *hubInstancesController) restoreFailedRemoval(name, storedKey string, hasStoredKey bool, oauthBytes []byte, hasOAuth bool, cause error, frame string, supplies removalSupply) (bool, error) {
	var problems []string
	storedKeyRestored := true
	if hasStoredKey {
		if err := c.auth.setCredential(name, storedKey); err != nil {
			storedKeyRestored = false
			problems = append(problems, fmt.Sprintf("its stored key could not be restored (%v)", err))
		}
	}
	oauthRestored := true
	if hasOAuth {
		// Through the writer the auth store uses, so the record this puts back
		// is replaced atomically: an in-place rewrite of a credential is a
		// file a reader can catch half written, and a crash inside it leaves
		// truncated state where this call exists to restore the whole thing.
		if err := authopenai.WriteAuthFile(authopenai.AuthFilePath(c.auth.stateDir, name), oauthBytes); err != nil {
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
	// A backstop for the removal flow's own up-front refusal (see
	// requireCredentialStore): the read below must not dereference a nil store,
	// and the clear is a write the nil-store seam already refuses.
	if err := c.requireCredentialStore(); err != nil {
		return deleted, err
	}
	if _, stored := c.auth.storedKey(name); stored {
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
	deleted.oauthRecord = removedRecord
	if removedRecord {
		c.applied.markApplied()
	}
	return deleted, nil
}

// deletedCredentials names which credential layers a removal's cleanup
// actually removed, so a restore rewrites only those.
type deletedCredentials struct {
	storedKey   bool
	oauthRecord bool
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
