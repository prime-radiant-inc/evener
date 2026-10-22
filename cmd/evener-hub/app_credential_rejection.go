package hub

import (
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"sync"

	"primeradiant.com/evener/llm"
)

// credentialRejectionStatus reports whether err is the provider refusing the
// credential itself: an HTTP 401 or 403, or an llm authentication or
// access-denied failure. It is the one rule for what "rejected" means: the
// credential test's classifier uses it, and so does settleCredentialProbe,
// through which every probe outcome is recorded: Test connection, the hub's
// own model listings, and the probe a session's refused turn triggers
// (sessionCredentialWatch). Rate
// limits and quota (429), server errors, timeouts, network failures, a missing
// endpoint and local configuration errors are not rejections: they say nothing
// about whether the credential is good. status is the HTTP status when one is
// known, else 0.
//
// Some adapters surface an HTTP failure only as text ("HTTP 401",
// "status=401"), so the status is read from the message when the error
// carries none. Only the status number is kept; nothing else from the message.
// A rejection classified some other way (an "invalid key" message on a 400)
// reports status 0: the message must not name a status the rejection is not.
func credentialRejectionStatus(err error) (status int, rejected bool) {
	if err == nil {
		return 0, false
	}
	if _, ok := errors.AsType[*llm.ConfigurationError](err); ok {
		return 0, false
	}
	if llmErr, ok := errors.AsType[llm.Error](err); ok {
		status = llmErr.StatusCode()
	}
	if status == 0 {
		if match := credentialRejectionText.FindStringSubmatch(err.Error()); match != nil {
			status, _ = strconv.Atoi(match[1])
		}
	}
	if rejectedCredentialStatus(status) {
		return status, true
	}
	if kind := llm.Kind(err); kind == llm.KindAuthentication || kind == llm.KindAccessDenied {
		return 0, true
	}
	return 0, false
}

// rejectedCredentialStatus reports whether an HTTP status is the provider
// refusing the credential: 401 or 403.
func rejectedCredentialStatus(status int) bool {
	return status == http.StatusUnauthorized || status == http.StatusForbidden
}

// credentialRejectionText finds a 401 or 403 in an error's text, as a whole
// number: "HTTP 4010" is not one.
var credentialRejectionText = regexp.MustCompile(`(?:HTTP |status=)(401|403)\b`)

// credentialRejectedMessage is AuthStatusResponse.Error for a rejected
// credential: words the hub writes, with the HTTP status when it is known and
// never anything the provider said, since a provider's error body can carry a
// key fragment or the user's own request. It says what went wrong and what to
// do (the redesign spec's rule for errors, section 5), so every client shows
// the same sentence.
func credentialRejectedMessage(status int) string {
	const fix = " Replace the key or sign in again."
	if status == 0 {
		return "The provider rejected this credential." + fix
	}
	return fmt.Sprintf("The provider rejected this credential (HTTP %d).", status) + fix
}

// credentialRejections is the hub's in-memory record of the credentials a
// provider rejected, by instance name. A rejection holds only while the
// instance's credential configuration revision is the one it was seen under
// (a new endpoint or source is not the credential that was refused), and any
// credential write the hub makes for the instance drops it, as does removing
// or renaming the instance. It is not persisted: a hub restart forgets it, and
// the next probe finds it again, which is safer than carrying a rejection
// across an environment the hub no longer reads.
//
// The revision does not cover the secret itself, so a key another process
// writes straight into credentials.toml leaves the rejection standing until
// the next probe that reaches the provider, or a restart.
type credentialRejections struct {
	mu sync.Mutex
	// writes counts credential writes by instance name. A probe notes its
	// instance's count when it starts and records its outcome only if no write
	// to that instance landed since: the key a probe dialed may already be
	// replaced by the time the provider answers.
	writes map[string]uint64
	byName map[string]credentialRejection
	// settlementEntered, when set, runs in settleCredentialProbe immediately
	// before credMu is taken, after the fingerprint-key resolution and the
	// rejection classification above it. A test that parks an instance edit
	// between its publication and its fence uses it to know the probe's
	// outcome is back and settlement has reached the shared lock, so the
	// bounded observation that follows watches the narrowest interval this
	// seam can mark. It is an acknowledgment of position, not a
	// synchronization guarantee. Nil in production.
	settlementEntered func()
}

type credentialRejection struct {
	revision string
	message  string
}

// credentialProbeStart is what a probe captures before it dials: the
// instance, the configuration revision it is probing, the instance's write
// count, and whether an outcome may be recorded as a credential rejection.
type credentialProbeStart struct {
	name     string
	revision string
	writes   uint64
	// recordable separates configuration identity from rejection eligibility:
	// a keyless instance still has an identity to compare (its endpoint or
	// auth scheme can move while the probe is in flight), but a 401 it receives
	// is no credential the provider rejected and nothing the user could
	// replace.
	recordable bool
}

// beginCredentialProbe captures name's probe start. A probe begins before
// anything reads the credential it sends (the client is built after), so a
// write or access edit that lands in between voids it.
//
// The fingerprint key is resolved before credMu is taken (a resolution can
// repair the key file); the capture itself takes credMu's shared side, so it
// cannot interleave with an instance edit's privileged section. That is what
// makes an edit's published access and its probe fence one boundary for a
// capture: the capture sees the old state before the edit or the new state
// after it, never a new revision with an unfenced old counter. A nil
// controller begins a probe that settles to nothing, for a caller with no hub
// credential state to feed.
func (c *hubAuthController) beginCredentialProbe(name string) credentialProbeStart {
	if c == nil {
		return credentialProbeStart{}
	}
	key, _ := resolveEndpointFingerprintKey(c.stateDir)
	c.credMu.RLock()
	defer c.credMu.RUnlock()
	revision, recordable := c.currentCredentialRevisionForKey(key, name)
	c.rejections.mu.Lock()
	defer c.rejections.mu.Unlock()
	return credentialProbeStart{name: name, revision: revision, writes: c.rejections.writes[name], recordable: recordable}
}

// currentCredentialRevisionForKey is name's credential configuration revision
// now, the one its status reports, and whether an outcome may be recorded as a
// credential rejection. The revision describes a resolved keyless instance
// too: its endpoint, protocol and auth scheme can all move while a probe is in
// flight, and the probe's answer is only about the configuration it read. An
// empty revision means the hub could not establish an identity at all (no
// registry, no resolution, or no fingerprint key), not that the access is
// unchanged; it records nothing (settleCredentialProbe). A keyless instance's
// 401 or 403 is no credential the provider rejected, so recordable is false
// even though its identity is real.
//
// The caller holds credMu (shared or exclusive) and has already resolved key;
// that shared side orders this read against an instance edit's privileged
// publish-and-fence section.
func (c *hubAuthController) currentCredentialRevisionForKey(key []byte, name string) (revision string, recordable bool) {
	r := c.registry()
	if r == nil {
		return "", false
	}
	res, err := r.ResolveInstancePresence(name)
	if err != nil {
		return "", false
	}
	return c.credentialConfigRevisionForKey(key, name, res), res.Credential.Source != "none"
}

// settleCredentialProbe records what a probe's model listing says about the
// credential: a rejection records one, a live listing clears one, and
// anything else (an unreachable endpoint, a listing the provider did not
// serve) says nothing and leaves the record as it was. A credential write to
// the instance since the probe started voids its outcome, and so does a change
// to its configuration: the outcome is about a credential or an endpoint the
// instance no longer has. A change is announced through
// credentialRejectionChanged; the caller must not hold credMu, since the
// announcement reads the instance's status.
//
// It reports whether the probe is still current. A false verdict means the
// outcome describes neither the credential nor the configuration the instance
// has now, so a caller that publishes the outcome as a verdict must refuse it;
// callers that only feed rejection state may ignore the result, which they
// already did. Freshness is not "a rejection was recorded": a keyless probe or
// an inconclusive one is still a valid answer about the same state.
//
// A probe with no revision records nothing: the hub could not resolve the
// configuration identity or its fingerprint key, and the rejection could not
// tell the configuration it was about from any other. A keyless probe has an
// identity and is compared like any other, but records nothing: it sent no
// credential to reject.
//
// The freshness comparison and the write-count check take credMu's shared
// side together. An instance edit holds the exclusive side from its write
// through the probe fence, so a probe cannot read the replacement access while
// the fence is still at the old value. The key is resolved before the lock,
// and the change callback runs after both mutexes are released.
func (c *hubAuthController) settleCredentialProbe(start credentialProbeStart, listing llm.ModelListing, err error) bool {
	if c == nil {
		return true
	}
	key, _ := resolveEndpointFingerprintKey(c.stateDir)
	status, rejected := credentialRejectionStatus(err)
	verified := err == nil && listing.Live
	if c.rejections.settlementEntered != nil {
		c.rejections.settlementEntered()
	}
	c.credMu.RLock()
	if start.revision != "" {
		current, _ := c.currentCredentialRevisionForKey(key, start.name)
		if current != start.revision {
			c.credMu.RUnlock()
			return false
		}
	}
	r := &c.rejections
	r.mu.Lock()
	if r.writes[start.name] != start.writes {
		r.mu.Unlock()
		c.credMu.RUnlock()
		return false
	}
	if (!rejected && !verified) || start.revision == "" || !start.recordable {
		r.mu.Unlock()
		c.credMu.RUnlock()
		return true
	}
	previous, had := r.byName[start.name]
	changed := false
	if rejected {
		next := credentialRejection{revision: start.revision, message: credentialRejectedMessage(status)}
		changed = !had || previous != next
		if r.byName == nil {
			r.byName = map[string]credentialRejection{}
		}
		r.byName[start.name] = next
	} else if had {
		delete(r.byName, start.name)
		changed = true
	}
	r.mu.Unlock()
	c.credMu.RUnlock()
	if changed && c.credentialRejectionChanged != nil {
		c.credentialRejectionChanged(start.name)
	}
	return true
}

// forgetCredentialRejection drops name's rejection and voids name's probes in
// flight: a credential write for it landed, so no earlier probe describes the
// credential now stored.
func (c *hubAuthController) forgetCredentialRejection(name string) {
	c.rejections.mu.Lock()
	defer c.rejections.mu.Unlock()
	c.rejections.voidProbesLocked(name)
	delete(c.rejections.byName, name)
}

// voidCredentialProbes voids name's probes in flight and keeps its rejection,
// for a credential change that may still be rolled back.
func (c *hubAuthController) voidCredentialProbes(name string) {
	c.rejections.mu.Lock()
	defer c.rejections.mu.Unlock()
	c.rejections.voidProbesLocked(name)
}

func (r *credentialRejections) voidProbesLocked(name string) {
	if r.writes == nil {
		r.writes = map[string]uint64{}
	}
	r.writes[name]++
}

// credentialRejectionError is AuthStatusResponse.Error for name at revision:
// the recorded rejection's message while the configuration is the one that
// was rejected, and empty otherwise.
func (c *hubAuthController) credentialRejectionError(name, revision string) string {
	c.rejections.mu.Lock()
	defer c.rejections.mu.Unlock()
	rejection, ok := c.rejections.byName[name]
	if !ok || rejection.revision != revision {
		return ""
	}
	return rejection.message
}
