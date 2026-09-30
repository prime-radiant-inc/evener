package hub

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"

	"primeradiant.com/evener/llm"
)

// credentialRejectionStatus reports whether err is the provider refusing the
// credential itself: an HTTP 401 or 403, or an llm authentication or
// access-denied failure. It is the one rule for what "rejected" means: the
// credential test's classifier uses it, and so does settleCredentialProbe,
// through which every probe outcome is recorded. Rate
// limits and quota (429), server errors, timeouts, network failures, a missing
// endpoint and local configuration errors are not rejections: they say nothing
// about whether the credential is good. status is the HTTP status when one is
// known, else 0.
//
// Some adapters surface an HTTP failure only as text ("HTTP 401",
// "status=401"), so the status is read from the message when the error
// carries none. Only the status number is kept; nothing else from the message.
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
		message := err.Error()
		for _, code := range []int{401, 403} {
			if strings.Contains(message, "HTTP "+strconv.Itoa(code)) || strings.Contains(message, "status="+strconv.Itoa(code)) {
				status = code
				break
			}
		}
	}
	if status == 401 || status == 403 {
		return status, true
	}
	if kind := llm.Kind(err); kind == llm.KindAuthentication || kind == llm.KindAccessDenied {
		return status, true
	}
	return status, false
}

// credentialRejectedMessage is AuthStatusResponse.Error for a rejected
// credential: words the hub writes, with the HTTP status when it is known and
// never anything the provider said, since a provider's error body can carry a
// key fragment or the user's own request.
func credentialRejectedMessage(status int) string {
	if status == 0 {
		return "The provider rejected this credential."
	}
	return fmt.Sprintf("The provider rejected this credential (HTTP %d).", status)
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
}

type credentialRejection struct {
	revision string
	message  string
}

// credentialProbeStart is what a probe captures before it dials: the
// instance, the configuration revision it is probing, and the instance's
// write count.
type credentialProbeStart struct {
	name     string
	revision string
	writes   uint64
}

// beginCredentialProbe captures name's probe start. It resolves the
// configuration revision, so the caller must not hold credMu
// (currentCredentialRevision).
func (c *hubAuthController) beginCredentialProbe(name string) credentialProbeStart {
	revision := c.currentCredentialRevision(name)
	c.rejections.mu.Lock()
	defer c.rejections.mu.Unlock()
	return credentialProbeStart{name: name, revision: revision, writes: c.rejections.writes[name]}
}

// currentCredentialRevision is name's credential configuration revision now,
// the one its status reports. It resolves the fingerprint key, which can
// repair the key file, so the caller must not hold credMu.
func (c *hubAuthController) currentCredentialRevision(name string) string {
	key, _ := resolveEndpointFingerprintKey(c.stateDir)
	return c.credentialConfigRevisionForKey(key, name)
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
// A probe with no revision (the hub could not resolve its fingerprint key)
// records nothing: its rejection could not tell the configuration it was
// about from any other.
func (c *hubAuthController) settleCredentialProbe(start credentialProbeStart, listing llm.ModelListing, err error) {
	status, rejected := credentialRejectionStatus(err)
	verified := err == nil && listing.Live
	if (!rejected && !verified) || start.revision == "" {
		return
	}
	if c.currentCredentialRevision(start.name) != start.revision {
		return
	}
	r := &c.rejections
	r.mu.Lock()
	if r.writes[start.name] != start.writes {
		r.mu.Unlock()
		return
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
	if changed && c.credentialRejectionChanged != nil {
		c.credentialRejectionChanged(start.name)
	}
}

// forgetCredentialRejection drops name's rejection and voids name's probes in
// flight: a credential write for it landed, so no earlier probe describes the
// credential now stored.
func (c *hubAuthController) forgetCredentialRejection(name string) {
	c.rejections.mu.Lock()
	defer c.rejections.mu.Unlock()
	if c.rejections.writes == nil {
		c.rejections.writes = map[string]uint64{}
	}
	c.rejections.writes[name]++
	delete(c.rejections.byName, name)
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
