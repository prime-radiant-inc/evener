package hub

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"os"
	"sort"
	"strings"

	"primeradiant.com/evener/agent/plugin"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/appsource"
	"primeradiant.com/evener/cmd/evener-hub/internal/fspaths"
	"primeradiant.com/evener/cmd/evener-hub/internal/hostreg"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/cmdutil"
	"primeradiant.com/evener/internal/appserver"
	"primeradiant.com/evener/internal/plugins"
)

// remoteHostSourceSeams are the per-host seams one remote hub source resolves
// through: the dialing client func, the attached-only client/handshake/facts
// lookups, and the online signal. Startup reads them straight from WebConfig;
// the host manager carries the same seams in its own config.
type remoteHostSourceSeams struct {
	client           appsource.RemoteHubClientFunc
	clientIfAttached func(host string) (*appwire.Client, bool)
	handshake        func(host string, client *appwire.Client) (appwire.InitializeResponse, bool)
	facts            func(ctx context.Context, host string, client *appwire.Client) (appsource.HostFacts, error)
	online           func(host string) bool
}

// registerRemoteHubSource constructs and wires one remote host's appsource
// source and registers it — the one construction path startup
// (newHubSourceRegistry) and the runtime add (hubHostManager.registerSource)
// share, so a host added at runtime is wired exactly like a configured one.
// The non-dialing seams every non-explicit read path resolves through are
// set here: the attached-only client lookup and the attach handshake facts
// (component 05, §"Registration and default-source selection"). The online
// signal fails open when none is wired — the pre-06 default — so an
// explicitly attached host stays usable by every source-mediated call that
// gates on Online(), while hostRow never trusts the signal alone: its
// attached-client guard still decides Attached, so the fail-open default
// cannot render a channel-less row online.
//
// The identity generation is assigned in the remote-thread cache BEFORE the
// source becomes registry-visible: a refresh walk may enumerate the source
// the moment it is added, and the walk captures the source's generation
// immediately before it reads it, so every enumerable source must carry a
// generation from the instant it is enumerable, or the publish drops its
// rows as unowned. Configured hosts never pass through the host manager, so
// theirs is assigned on this path; the local source needs none — the walk
// skips it, so it can never own walk-published rows.
func registerRemoteHubSource(registry *appsource.Registry, cache *hubcore.RemoteThreadCache, host hostreg.Host, seams remoteHostSourceSeams) {
	source := appsource.NewRemoteHubSource(host.Name, host.Roots, seams.client)
	source.SetHostClientIfAttached(seams.clientIfAttached)
	source.SetHostFacts(seams.facts)
	source.SetHostHandshake(seams.handshake)
	source.SetHostOnline(func() bool {
		return seams.online == nil || seams.online(host.Name)
	})
	if cache != nil {
		cache.RegisterSource(host.Name)
	}
	registry.Add(source)
}

// newHubSourceRegistry builds the hub's sources over cfg.Roster. The hub always
// wires a roster (main.go). Without one there is no local source at all, so a
// lookup of a local ref fails as "source not found" rather than finding a
// source that lists nothing.
func newHubSourceRegistry(cfg hubcore.WebConfig) *appsource.Registry {
	registry := appsource.NewRegistry()
	roster := cfg.Roster
	if roster != nil {
		local := appsource.NewLocalDaemonSourceWithEntries("local", func() []appsource.LocalDaemonEntry {
			return localDaemonEntriesFromRoster(roster.List())
		}, http.DefaultClient)
		// A daemon that leaves for good is announced by the roster, the one place
		// that sees its process or its file go; the relay tells that daemon's
		// subscribers to re-read.
		// The roster's resolved session id is passed along: a legacy entry names
		// no session of its own, and its relay session is keyed by the resolved one.
		roster.SetOnSessionGone(func(gone hubcore.LiveEntry) { local.AnnounceDaemonGone(gone.Entry, gone.SessionID) })
		registry.Add(local)
	}
	if len(cfg.RemoteHosts) > 0 {
		if cfg.RemoteHostClient == nil {
			names := make([]string, 0, len(cfg.RemoteHosts))
			for _, host := range cfg.RemoteHosts {
				names = append(names, host.Name)
			}
			_, _ = fmt.Fprintf(os.Stderr, "[hub] remote hosts skipped (no SSH client wired): %s\n", strings.Join(names, ", "))
		} else {
			for _, host := range cfg.RemoteHosts {
				registerRemoteHubSource(registry, cfg.RemoteThreadCache, host, remoteHostSourceSeams{
					client:           cfg.RemoteHostClient,
					clientIfAttached: cfg.RemoteHostClientIfAttached,
					handshake:        cfg.RemoteHostHandshake,
					facts:            cfg.RemoteHostFacts,
					online:           cfg.RemoteHostOnline,
				})
			}
		}
	}
	return registry
}

// localDaemonEntriesFromRoster is the local source's view of a roster's live
// entries: crashed ones skipped, in-process descendants addressed as their own
// AppWire threads served by their owner's endpoint.
func localDaemonEntriesFromRoster(live []hubcore.LiveEntry) []appsource.LocalDaemonEntry {
	entries := make([]appsource.LocalDaemonEntry, 0, len(live))
	for _, item := range live {
		if item.Crashed {
			continue
		}
		entry := appsource.LocalDaemonEntry{
			Entry:             item.Entry,
			SessionID:         item.SessionID,
			Status:            item.Status,
			PendingAsk:        item.PendingAsk,
			PendingEscalation: item.PendingEscalation,
			RunningJobs:       item.RunningJobs,
			CompletedJobs:     item.CompletedJobs,
			Watches:           item.Watches,
			Capabilities:      item.Capabilities,
			CapabilitiesKnown: item.CapabilitiesKnown,
		}
		entries = append(entries, entry)
		// In-process descendants are addressed as their own AppWire
		// threads, but are served by their owner's daemon endpoint.
		for _, childID := range item.RunningSubagentIDs {
			child := entry
			child.OwnerSessionID = entry.SessionID
			child.SessionID = childID
			// The alias carries the child's OWN watches, sampled by
			// the prober into ChildWatches. Inheriting the root
			// entry's Watches would put the root's rows on the
			// child row (and, for a read-only alias, they were
			// suppressed anyway), losing the child's own.
			child.Watches = appwire.CloneEvenerWatches(item.ChildWatches[childID])
			// The child's own projected status when the daemon carries
			// it — inheriting the parent's status would render a
			// settled delegate as working (or vice versa). "" (old
			// daemon) keeps the inherited status, the pre-states
			// behavior.
			if childState := strings.TrimSpace(item.RunningSubagentStates[childID]); childState != "" {
				child.Status = childState
			}
			child.ReadOnlyAlias = true
			entries = append(entries, child)
		}
	}
	return entries
}

var (
	resolveTurnStartSource = sourceForThread
	resumeTurnStartThread  = hubThreadAutoResume
	authLoginComplete      = func(c *hubAuthController, ctx context.Context, p appwire.AuthLoginCompleteParams) (appwire.AuthLoginCompleteResponse, error) {
		return c.LoginComplete(ctx, p)
	}
	authDevicePoll = func(c *hubAuthController, ctx context.Context, p appwire.AuthDevicePollParams) (appwire.AuthDevicePollResponse, error) {
		return c.DevicePoll(ctx, p)
	}
	launchTrustRepo = func(c *hubLaunchController, ctx context.Context, p appwire.LaunchConfigTrustRepoParams) (appwire.LaunchConfigResolved, error) {
		return c.TrustRepo(ctx, p)
	}
)

type threadReadRelayPolicy interface {
	// RelayOnThreadRead reports whether a plain (non-Subscribe) thread/read
	// starts a relay. A Subscribe read still overrides it.
	RelayOnThreadRead() bool
}

// threadRelayCapableSource reports whether a source can serve thread relays at
// all. A source that cannot is never relayed, even for a Subscribe read:
// startRelay calls SubscribeThread, so relaying it would fail the read instead
// of returning the snapshot. Sources that implement only RelayOnThreadRead keep
// the Subscribe-overrides-plain-read policy.
type threadRelayCapableSource interface {
	SupportsThreadRelay() bool
}

func sourceSupportsThreadRelay(source appsource.Source) bool {
	if capable, ok := source.(threadRelayCapableSource); ok {
		return capable.SupportsThreadRelay()
	}
	return true
}

// threadReadLocalImagePolicy reports whether a source's threads describe files
// on this hub's own filesystem. A remote hub source serves its transcript but
// not its filesystem, so its CWDs and tool-argument paths name another machine.
type threadReadLocalImagePolicy interface {
	EnrichThreadFileBackedImages() bool
}

// enrichSourcedThreadImages stamps fetchable image URLs and, for a source whose
// files live on this hub, adds file-backed output-image descriptors by reading
// the session's working directory.
//
// A source whose images are not local is neither stamped nor enriched: the
// thread is returned with its remote-supplied image routes neutralized. Running
// the local file pass on a remote CWD would probe unrelated controller-local
// paths and could attach descriptors for files the thread never wrote, and
// stampThreadImageURLs mints this hub's sha-addressed /s/<session>/images/<sha>
// route, which handleSessionImage resolves against this hub's own Past index. A
// remote session is never in it, so a remote-supplied route would 404 (or, on a
// session-id collision, serve another session's bytes) once the browser
// requested it from this hub. Neutralizing it leaves the descriptor's SHA for
// the controller-side proxy that will resolve it, which is not yet part of this
// read path.
func enrichSourcedThreadImages(source appsource.Source, thread appwire.Thread) appwire.Thread {
	if !threadImagesLocal(source) {
		return stripRemoteImageRoutes(thread)
	}
	return enrichLocalSourcedThreadImages(thread)
}

// threadImagesLocal reports whether a source's threads describe files on this hub's
// own filesystem. A source that does not implement the policy serves this hub's own
// sessions, so its threads are local.
func threadImagesLocal(source appsource.Source) bool {
	if policy, ok := source.(threadReadLocalImagePolicy); ok {
		return policy.EnrichThreadFileBackedImages()
	}
	return true
}

// enrichLocalSourcedThreadImages runs the two image passes that need this hub's own
// view of the thread: the sha-addressed route stamp, which is minted from the
// session id, and the file-backed pass, which reads the thread's CWD here.
func enrichLocalSourcedThreadImages(thread appwire.Thread) appwire.Thread {
	thread = stampThreadImageURLs(thread)
	return enrichThreadFileBackedOutputImages(thread)
}

// stripRemoteImageRoutes removes hub-relative image routes from a thread whose
// images live on another hub. A relative route is meaningful only against the
// origin that minted it: left on a remote thread, it would make the browser
// request this hub's own route for a session this hub does not have. The remote
// hub mints both /s/<session>/images/<sha> (stamped by stampThreadImageURLs) and
// /doc/image?session=<session>&path=<rel> (attached to file-backed output images
// by outputImagesForToolCall/resolveOutputImageFile), so any root-relative path
// that still names a bare session must be neutralized, not just the /s/... one.
// External URLs and data: URLs are untouched, because the browser resolves them
// against their own origin.
//
// A route whose session id already names another source (`<sourceID>:<session>`)
// is the exception: the source's own outbound translation writes it as the
// host-qualified controller route this hub serves by proxying to that host (see
// appsource's image URL visitor), so neutralization must leave it alone. Only a
// route still naming a bare session — one the remote hub minted for itself — is
// removed.
func stripRemoteImageRoutes(thread appwire.Thread) appwire.Thread {
	for turnIndex := range thread.Turns {
		items := thread.Turns[turnIndex].Items
		for itemIndex := range items {
			for imageIndex := range items[itemIndex].Images {
				if isHubRelativeImageRoute(items[itemIndex].Images[imageIndex].URL) &&
					!hostQualifiedImageRoute(items[itemIndex].Images[imageIndex].URL) {
					items[itemIndex].Images[imageIndex].URL = ""
				}
			}
			for imageIndex := range items[itemIndex].OutputImages {
				if isHubRelativeImageRoute(items[itemIndex].OutputImages[imageIndex].URL) &&
					!hostQualifiedImageRoute(items[itemIndex].OutputImages[imageIndex].URL) {
					items[itemIndex].OutputImages[imageIndex].URL = ""
				}
			}
		}
	}
	return thread
}

// hostQualifiedImageRoute reports whether raw is a controller image route whose
// session id already names another source, in the host-qualified form
// `/s/<sourceID>:<session>/images/<sha>` or
// `/doc/image?session=<sourceID>:<session>`. Such a route is one this hub serves
// by proxying to the owning host, so it is not a route the remote hub minted for
// itself and must not be neutralized. The grammar lives with the visitor that
// writes those routes, so the two cannot drift.
func hostQualifiedImageRoute(raw string) bool {
	return appsource.HostQualifiedControllerImageRoute(raw)
}

// isHubRelativeImageRoute reports whether raw is an origin-relative image route
// of the form a hub mints for itself, e.g. /s/<session>/images/<sha> or
// /doc/image?session=<session>&path=<rel>. A leading slash makes a URL resolve
// against the serving origin, so it is meaningful only on the hub that minted
// it. A network-path reference (//host/...) and scheme URLs (http:, https:,
// data:, ...) name their own origin and are left untouched.
func isHubRelativeImageRoute(raw string) bool {
	trimmed := strings.TrimSpace(raw)
	return strings.HasPrefix(trimmed, "/") && !strings.HasPrefix(trimmed, "//")
}

func relayOnThreadRead(source appsource.Source) bool {
	if policy, ok := source.(threadReadRelayPolicy); ok {
		return policy.RelayOnThreadRead()
	}
	return true
}

// listItemTurns returns a packed item-mode page when the source has item
// candidates or when its source page contains data. A legacy source with
// no data or a ListTurns error is left for the caller's saved-transcript
// fallback; candidate and packing errors are terminal just as they are for a
// native ItemCandidateSource.
func listItemTurns(
	ctx context.Context,
	source appsource.Source,
	params appwire.ThreadTurnsListParams,
	logf func(format string, args ...any),
) (appwire.ThreadTurnsListResponse, bool, error) {
	itemLimit, err := appwire.NormalizeTranscriptItemLimit(params.ItemLimit)
	if err != nil {
		return appwire.ThreadTurnsListResponse{}, true, err
	}
	params.ItemsView = string(appwire.TurnItemsViewFragment)
	var live appwire.ThreadTurnsListResponse
	var candidates transcriptItemCandidateResult
	if _, native := source.(appsource.ItemCandidateSource); native {
		candidates, err = sourceItemCandidateResultForList(ctx, source, params, live)
		if err != nil {
			return appwire.ThreadTurnsListResponse{}, true, err
		}
	} else {
		live, err = source.ListTurns(ctx, params)
		if err != nil || len(live.Data) == 0 {
			return live, false, err
		}
		candidates, err = sourceItemCandidateResultForList(ctx, source, params, live)
		if err != nil {
			return appwire.ThreadTurnsListResponse{}, true, err
		}
	}

	meta, metaErr := source.ReadThread(ctx, appwire.ThreadReadParams{Ref: params.Ref, ThreadID: params.ThreadID, IncludeTurns: false})
	if metaErr != nil && logf != nil {
		logf("thread turns metadata enrichment unavailable: %v", metaErr)
	}
	packed, packErr := packThreadTurnsItemCandidates(candidates, func(response appwire.ThreadTurnsListResponse) (appwire.ThreadTurnsListResponse, error) {
		thread := appwire.Thread{Turns: response.Data}
		if metaErr == nil {
			thread.ID = meta.Thread.ID
			thread.SessionID = meta.Thread.SessionID
			thread.CWD = meta.Thread.CWD
		}
		// Image handling is independent of the optional metadata read. A remote
		// source's root-relative routes are neutralized whether or not that read
		// succeeded — nothing about neutralizing them needs the session id or CWD it
		// would have supplied, and a route left on the page resolves against this
		// hub's origin for a session this hub does not have. The two local passes do
		// need those, so they run only after a successful read.
		switch {
		case !threadImagesLocal(source):
			thread = stripRemoteImageRoutes(thread)
		case metaErr == nil:
			thread = enrichLocalSourcedThreadImages(thread)
		}
		response.Data = thread.Turns
		return response, nil
	}, itemLimit)
	if packErr != nil {
		return appwire.ThreadTurnsListResponse{}, true, packErr
	}
	return packed, true, nil
}

func blockedUnknownMutationError(clientMutationID string, err error) error {
	if isDaemonRestartRequiredError(err) || isSessionRecoveryAdmissionError(err) {
		return blockedAdmissionMutationError(err, clientMutationID)
	}
	return appwire.WireError{
		Code:    appwire.CodeInternalError,
		Message: err.Error(),
		Data: appwire.ErrorData{
			EvenerErrorInfo:  appwire.ErrorMutationOutcomeUnknown,
			ClientMutationID: clientMutationID,
			MutationOutcome:  appwire.MutationOutcomeUnknown,
			RetryDisposition: appwire.RetryDispositionBlocked,
			Cause:            "persistenceUnavailable",
		},
	}
}

// mutationResumeFailureError reports a resume failure the way the mutation that
// needed the resume must see it.
//
// A resume that failed because the CALLER'S TARGET was deleted is not an unknown
// outcome: that target is gone, and the deletion is this caller's to reconcile,
// so the refusal keeps its own meaning (MutationOutcomeTargetDeleted /
// RetryDispositionNone) and is named for this caller. Without that, a mutation
// whose target was deleted between the failed attempt and the auto-resume would
// come back unknown/blocked and its record would be retained rather than
// reconciled as orphaned.
//
// A resume fences TWO sets, however, and only the first is this caller's target.
// resumeThreadLockedLaunch fences the requested target alone and then every alias
// of its ownership group (deletionFenceErrorForGroup), and it reports the first
// alias it finds deleted. Since the requested target's own fence runs first, a
// group-fence failure the mutation sees is by construction about a SIBLING alias
// -- another name of the session, not the target the caller addressed (see
// deletionFenceErrorForGroup's doc and the force-stop path's identical
// sibling-alias fence). Settling the caller's record as orphaned on a sibling's
// deletion would discard a mutation addressed to a target that may still exist.
//
// The decision therefore asks the fence every admission path already asks --
// deletionFenceError, the single-target fence, which answers for the requested
// target and names the caller's own id -- instead of trusting whichever alias the
// resume reported. When it answers, that refusal is returned (the deletion, named
// for this caller, with its own outcome); when it does not, the failure keeps
// exactly the blocked-unknown envelope it gets today, as does every non-deletion
// resume failure. Nothing here touches the id the daemon stored.
func mutationResumeFailureError(cfg hubcore.WebConfig, ref, threadID, clientMutationID string, resumeErr error) error {
	if clientMutationID == "" {
		return resumeErr
	}
	if isTargetDeletedError(resumeErr) {
		if own := deletionFenceError(cfg, ref, threadID, clientMutationID); own != nil {
			return own
		}
		// A stable alias can resolve to a different CURRENT target, and the resume
		// fences that resolved ownership too (resumeThread resolves it and hands the
		// resolved target to the locked launch's fence), so a deletion of the target
		// this caller's alias resolves to is the caller's own even though the alias
		// itself is not the deleted record. The resume discards the resolved target
		// when it fails, so re-resolve it with the same resolver instead of
		// guessing. Only these two ends of the caller's own request are claimed, so a
		// SIBLING alias's deletion stays out of this caller's record.
		if resolved := resolvedOwnershipTarget(cfg, ref, threadID); resolved != "" {
			if own := deletionFenceError(cfg, "", resolved, clientMutationID); own != nil {
				return own
			}
		}
	}
	return blockedUnknownMutationError(clientMutationID, resumeErr)
}

// resolvedOwnershipTarget returns the current target a mutation's ref resolves
// to, computed with the same resolver the resume used (resumeOwnership), or ""
// when it cannot be resolved: the hub holds no resume authority to resolve with,
// the ref is not a local one, the request names no id, or the ownership chain
// refuses to resolve (a pending recovery obligation, a cycle).
//
// It mirrors resumeThread's own derivation of the requested identity, so the
// chain walked here is the chain that resume walked; a resolution that has moved
// on since the resume failed simply yields a target the fence does not recognize,
// which keeps the caller's record retained rather than misattributed.
func resolvedOwnershipTarget(cfg hubcore.WebConfig, ref, threadID string) string {
	if cfg.ResumeLocks == nil {
		return ""
	}
	parsed, err := appwire.ParseRef(ref)
	if err != nil || parsed.SourceID != "local" {
		return ""
	}
	requestedID := strings.TrimSpace(threadID)
	if requestedID == "" {
		requestedID = parsed.ThreadID
	}
	if requestedID == "" {
		return ""
	}
	target, _, err := resumeOwnership(cfg, requestedID, parsed.ThreadID)
	if err != nil {
		return ""
	}
	return target
}

// canonicalMutationID returns the form two clientMutationId values are compared
// in.
//
// The hub does not trim the caller's id: it echoes exactly what the caller sent,
// because every client correlates its outbox record by that id. The daemon does
// trim it, at its own handler boundary, before a receipt or a refusal ever names
// it (server/appwire_runtime.go's handleAppTurn*/handleAppThreadClear set
// params.ClientMutationID = strings.TrimSpace(params.ClientMutationID), and
// agent/session_notes_rpc.go does the same). A byte-exact hub comparison would
// therefore fail to see a padded caller id (" mut-1 ") in the normalized id the
// daemon named ("mut-1"), and treat a known rejection or deletion as if it
// belonged to someone else.
func canonicalMutationID(id string) string { return strings.TrimSpace(id) }

// mutationIDsMatch reports whether a caller's clientMutationId and the id a
// hub-visible error names refer to the same mutation, compared canonically (see
// canonicalMutationID). Only a non-empty error id can match: an error that names
// no mutation never names the caller's.
func mutationIDsMatch(callerID, errorID string) bool {
	canonical := canonicalMutationID(errorID)
	return canonical != "" && canonical == canonicalMutationID(callerID)
}

// errorNamesClientMutation reports whether err already carries clientMutationID,
// the id of the mutation the caller submitted.
//
// Every client judges a failed mutation by that id alone: the web outbox's
// dispatcher refuses to correlate a failure that names none and a different id
// (appwire-client/typescript/state/mutation/dispatcher.ts), so the record stays
// "submitting" -- the prompt is neither delivered nor surfaced as failed, and
// the user has to retype it. A refusal the daemon minted for the caller's own
// mutation (rejectClientMutation sets the id) needs no help; a refusal that
// names a different mutation is no more correlatable for this caller than one
// that names none, and one that names none has to be wrapped before it leaves
// the hub.
//
// The comparison is canonical, not byte-exact (see mutationIDsMatch): the hub
// does not trim the caller's id, but the daemon names the id it trimmed, so a
// padded caller id would otherwise fail to recognize its own rejection. This is
// a comparison-time canonicalization only -- an id the hub echoes back stays
// exactly what the caller sent (see adoptCallerMutationID), because the client
// correlates byte-for-byte.
//
// The wire client decodes ErrorData as a map on some paths and as the typed
// struct on others, so both shapes are read -- the same convention
// app_retirement_resume.go's isLifecycleRetiringError follows.
func errorNamesClientMutation(err error, clientMutationID string) bool {
	if clientMutationID == "" {
		return false
	}
	wire, ok := wireErrorFromError(err)
	if !ok {
		return false
	}
	return mutationIDsMatch(clientMutationID, clientMutationIDFromData(wire.Data))
}

// adoptCallerMutationID hands err back with its clientMutationId rewritten to
// the caller's own id when the error names the same mutation in canonical form
// but not byte-for-byte -- the daemon trims the id before naming it, the caller
// submitted it padded.
//
// The rewrite is what keeps the response echoable: every client correlates its
// outbox record byte-for-byte against the id it submitted
// (appwire-client/typescript/state/mutation/dispatcher.ts compares
// data.clientMutationId !== record.clientMutationId), so a response carrying the
// daemon's normalized id would never settle a record that submitted a padded
// one. The id is never rewritten to a trimmed form -- the caller's own id is
// always the one echoed.
//
// err is returned unchanged when there is nothing to rewrite: it is not a
// WireError, it names no id, it already names the caller's id, or it names a
// mutation that is not the caller's. The rewrite handles both decoded shapes
// (typed ErrorData and map[string]any), the convention nameTargetDeletedFailure
// follows.
func adoptCallerMutationID(err error, clientMutationID string) error {
	if clientMutationID == "" {
		return err
	}
	wire, ok := wireErrorFromError(err)
	if !ok || wire.Data == nil {
		return err
	}
	named := clientMutationIDFromData(wire.Data)
	if named == clientMutationID || !mutationIDsMatch(clientMutationID, named) {
		return err
	}
	switch data := wire.Data.(type) {
	case appwire.ErrorData:
		data.ClientMutationID = clientMutationID
		wire.Data = data
	case map[string]any:
		updated := maps.Clone(data)
		updated["clientMutationId"] = clientMutationID
		wire.Data = updated
	default:
		return err
	}
	return wire
}

// adoptCallerMutationReceipt returns receipt with its ClientMutationID rewritten
// to the caller's own id when the receipt names the same mutation in canonical
// form but not byte-for-byte -- the daemon trims the id before it mints the
// receipt, while the caller submitted it padded.
//
// A successful mutation's receipt is what settles the caller's outbox record,
// and every client correlates that record byte-for-byte against the id it
// submitted (appwire-client/typescript/state/mutation/dispatcher.ts compares
// receipt.clientMutationId !== record.clientMutationId). A receipt naming the
// daemon's normalized id would therefore leave a padded record submitting even
// though the mutation applied: the success-path twin of the failure-path
// rewrite adoptCallerMutationID performs. Only the id changes -- disposition,
// thread/instance/turn ids, queue entry ids and projection state are untouched
// -- and the id is never rewritten to a trimmed form.
func adoptCallerMutationReceipt(receipt appwire.MutationReceipt, clientMutationID string) appwire.MutationReceipt {
	if clientMutationID == "" || receipt.ClientMutationID == clientMutationID {
		return receipt
	}
	if !mutationIDsMatch(clientMutationID, receipt.ClientMutationID) {
		return receipt
	}
	receipt.ClientMutationID = clientMutationID
	return receipt
}

// adoptFailureClientMutationID adopts the caller's own id onto a failure a
// direct mutation path returned: first nameTargetDeletedFailure's stamp for an
// ID-LESS target deletion, then adoptCallerMutationID's canonical rewrite.
//
// A target deletion is the one failure the hub can always attribute to the
// caller whose mutation hit it, even when the error says nothing about which
// mutation that was: a preflight thread/read deletion relayed from a remote hub
// reaches the hub with no clientMutationId at all (app_relay.go's startTurn
// hands it back untouched when the hub holds no deletion record of its own), and
// the deleting client's record is the caller's. Stamping the caller's id keeps
// the deletion's own outcome (targetDeleted / none) so the client reconciles the
// record as orphaned instead of leaving it submitting. nameTargetDeletedFailure
// is reused exactly as the retry path uses it -- it already refuses to touch a
// deletion that names a different mutation.
//
// Nothing else acquires an id here. An error that already names a DIFFERENT
// mutation is not this caller's to own (adoptCallerMutationID leaves it, and
// nameTargetDeletedFailure declines it), and an ID-LESS error that is not a
// deletion could belong to any caller, so it is left exactly as it is rather
// than claimed for this one.
func adoptFailureClientMutationID(err error, clientMutationID string) error {
	if err == nil || clientMutationID == "" {
		return err
	}
	if isTargetDeletedError(err) {
		if enriched := nameTargetDeletedFailure(clientMutationID, err); enriched != nil {
			return enriched
		}
	}
	return adoptCallerMutationID(err, clientMutationID)
}

// adoptResponseClientMutationID adopts the caller's own clientMutationId onto
// BOTH halves of a hub mutation result: the error with
// adoptFailureClientMutationID (which also stamps an id-less target deletion, see
// there) and the response's mutation receipt with adoptCallerMutationReceipt.
// Everything else passes through untouched.
//
// Both halves need it for the same reason. The daemon trims the caller's id at
// its own boundary before it mints a receipt OR a refusal
// (server/appwire_runtime.go's handleAppTurn*/handleAppThreadClear,
// agent/session_notes_rpc.go), while the hub holds and echoes the caller's
// verbatim id, and every client correlates its outbox record byte-for-byte
// (appwire-client/typescript/state/mutation/dispatcher.ts). A failure named with
// the daemon's normalized id would leave the record submitting exactly as a
// mismatched receipt would. adoptCallerMutationID is not widened for this: it
// already returns unchanged anything that is not a WireError, names no id, or
// names a different mutation canonically, and it never rewrites an id to a
// trimmed form.
//
// It is the single place that knows which responses carry the receipt field,
// wired where each caller-id-bearing mutation path returns: turn/start's first
// attempt and its post-resume retry (both through attemptStart), the direct turn
// mutations (steer, interrupt, queue, drainAsSteer, promoteQueuedAsSteer,
// cancelQueued), the resume relays (thread/clear, notes/human/set), and
// urls/remove -- which has no receipt to adopt but can still return a
// daemon-minted refusal naming the id. Goal-set and the EmptyResponse paths
// carry no caller id in their response and no id-naming error of their own, so
// they are not wired. Nothing here touches the id the daemon stored, only the id
// this caller's result carries back.
func adoptResponseClientMutationID[R any](resp R, err error, clientMutationID string) (R, error) {
	if clientMutationID == "" {
		return resp, err
	}
	if err != nil {
		return resp, adoptFailureClientMutationID(err, clientMutationID)
	}
	adopt := func(receipt appwire.MutationReceipt) appwire.MutationReceipt {
		return adoptCallerMutationReceipt(receipt, clientMutationID)
	}
	switch typed := any(resp).(type) {
	case appwire.TurnStartResponse:
		typed.Receipt = adopt(typed.Receipt)
		return any(typed).(R), nil
	case appwire.TurnSteerResponse:
		typed.Receipt = adopt(typed.Receipt)
		return any(typed).(R), nil
	case appwire.TurnInterruptResponse:
		typed.Receipt = adopt(typed.Receipt)
		return any(typed).(R), nil
	case appwire.TurnQueueResponse:
		typed.Receipt = adopt(typed.Receipt)
		return any(typed).(R), nil
	case appwire.TurnDrainAsSteerResponse:
		typed.Receipt = adopt(typed.Receipt)
		return any(typed).(R), nil
	case appwire.TurnPromoteQueuedAsSteerResponse:
		typed.Receipt = adopt(typed.Receipt)
		return any(typed).(R), nil
	case appwire.TurnCancelQueuedResponse:
		typed.Receipt = adopt(typed.Receipt)
		return any(typed).(R), nil
	case appwire.ThreadClearResponse:
		typed.Receipt = adopt(typed.Receipt)
		return any(typed).(R), nil
	case appwire.NotesHumanSetResponse:
		typed.Receipt = adopt(typed.Receipt)
		return any(typed).(R), nil
	}
	return resp, nil
}

// isShapeRefusal reports whether err refuses the request's shape: appwire's
// invalid-params and invalid-request codes carrying the invalidParams
// discriminant, decided before anything executes.
//
// The code alone is not enough. CodeInvalidParams is shared with
// resourceNotFound, transcriptItemCursorStale, and invalidHostField, which mean
// something entirely different to the caller; matching the code alone would let
// any of them masquerade as a deterministic shape refusal. Requiring the
// discriminant is what separates a true shape refusal -- resending the identical
// payload can never answer differently, and the web outbox already recovers from
// an uncorrelated one, so wrapping it as an unknown mutation outcome would tell
// the caller less than the refusal itself does -- from those other refusals.
func isShapeRefusal(err error) bool {
	wire, ok := wireErrorFromError(err)
	if !ok || wire.Data == nil {
		return false
	}
	if wire.Code != appwire.CodeInvalidParams && wire.Code != appwire.CodeInvalidRequest {
		return false
	}
	return evenerErrorInfoFromData(wire.Data) == string(appwire.ErrorInvalidParams)
}

// shapeRefusalNamesOtherMutation reports whether a shape refusal names a
// clientMutationId that belongs to a caller other than this one: a non-empty id
// that is not the caller's. The client dispatcher correlates by that id alone,
// so such a refusal is unrelated to this caller's record and must not be
// returned as the shape refusal it is.
//
// The comparison is canonical, like errorNamesClientMutation's: the daemon names
// the trimmed id while the hub holds the caller's verbatim one, so a padded
// caller id must still recognize its own shape refusal rather than have it
// treated as another caller's.
func shapeRefusalNamesOtherMutation(err error, clientMutationID string) bool {
	wire, ok := wireErrorFromError(err)
	if !ok || wire.Data == nil {
		return false
	}
	id := clientMutationIDFromData(wire.Data)
	canonical := canonicalMutationID(id)
	return canonical != "" && canonical != canonicalMutationID(clientMutationID)
}

// correlateRetryFailure decides what a retry that an earlier failure's resume
// made possible must report when it fails in turn.
//
// Several failures keep their own meaning and are returned unchanged: the
// caller's own cancellation (context.Canceled / context.DeadlineExceeded, which
// is not a mutation outcome at all), a refusal that already names this caller's
// mutation, and a true shape refusal (see isShapeRefusal) that is this caller's
// to own -- one that names NO mutation id or names the caller's own. A shape
// refusal that names a DIFFERENT mutation belongs to that other caller: it is
// not passed through, because the client would treat it as unrelated to its
// record and skip the no-id recovery path it applies to invalid-params, leaving
// this caller's mutation stuck submitting. The one exemption whose meaning is
// kept but whose id is added is a target deletion that names no mutation
// (below). Everything else is a failure no client's mutation dispatcher can
// classify -- one that names no clientMutationId, or names a different mutation
// (the web outbox correlates by that id alone) -- and is wrapped in the
// blocked-unknown envelope so the mutation is retained for a retry rather than
// left submitting forever (see blockedUnknownMutationError).
//
// The one exemption that is enriched rather than returned unchanged is a target
// deletion that names no mutation: it is handed back with this caller's
// clientMutationId stamped on it (keeping MutationOutcomeTargetDeleted) so the
// dispatcher can settle it as orphaned rather than be left unable to classify
// it. A deletion that names a DIFFERENT mutation is not this caller's to settle,
// so it is blocked-unknown instead. See nameTargetDeletedFailure.
//
// A nil return means err keeps its own meaning; callers return err unchanged.
// Shared by turn/start's retryAfterResume and withSessionResume's post-resume
// retry so every resume-once-then-retry mutation correlates its retry failure
// the same way.
func correlateRetryFailure(clientMutationID string, err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return nil
	}
	if errorNamesClientMutation(err, clientMutationID) {
		return nil
	}
	// A shape refusal is preserved only when it is this caller's to own: one that
	// names no mutation, or names the caller's own (returned above). A refusal
	// naming a DIFFERENT mutation is not this caller's to act on, and passing it
	// through unchanged would leave this caller's record submitting, so it falls
	// through to the blocked-unknown default below.
	if isShapeRefusal(err) && !shapeRefusalNamesOtherMutation(err, clientMutationID) {
		return nil
	}
	if isTargetDeletedError(err) {
		// A target deletion is the caller's to settle only when it names this
		// caller's mutation (returned unchanged above) or names none at all. A
		// deletion that names none -- e.g. a preflight thread/read deletion
		// relayed from a remote hub -- leaves the dispatcher unable to
		// correlate the record, so it is stamped with this caller's id rather
		// than hidden behind a generic outage. A deletion naming a DIFFERENT
		// mutation is not this caller's to settle: restamping it would make
		// that other caller's record settle as orphaned, so it falls through to
		// the blocked-unknown default below, retaining this caller's record.
		if clientMutationID == "" {
			return nil
		}
		if enriched := nameTargetDeletedFailure(clientMutationID, err); enriched != nil {
			return enriched
		}
	}
	return blockedUnknownMutationError(clientMutationID, err)
}

// nameTargetDeletedFailure enriches a target-deletion refusal with the caller's
// mutation id when it names NO mutation at all, keeping the deletion's own
// outcome (MutationOutcomeTargetDeleted / RetryDispositionNone). A deletion
// that already names the caller's own mutation needs no help; a deletion that
// names a DIFFERENT mutation is not this caller's to restamp -- doing so would
// let this caller's dispatcher settle the other mutation as orphaned -- so it
// is left to correlateRetryFailure's blocked-unknown default.
//
// A nil return means the refusal already names this caller's mutation (or
// names a different one, or carries no WireError to enrich), so the caller
// returns it unchanged (or falls through to blocked-unknown). A non-nil return
// is the refusal with clientMutationId set, which the caller returns in its
// place.
func nameTargetDeletedFailure(clientMutationID string, err error) error {
	if clientMutationID == "" || errorNamesClientMutation(err, clientMutationID) {
		return nil
	}
	wire, ok := wireErrorFromError(err)
	if !ok {
		return nil
	}
	// Only a deletion that names NO mutation is enriched. One that names a
	// different mutation belongs to that other caller and must not be restamped
	// as this caller's, or this caller's dispatcher would settle the other
	// mutation as orphaned.
	if clientMutationIDFromData(wire.Data) != "" {
		return nil
	}
	// The wire client decodes Data as the typed appwire.ErrorData on some paths
	// and as map[string]any on others; both are stamped the same way
	// blockedAdmissionMutationError stamps its own, without disturbing the
	// deletion outcome the refusal already carries.
	switch data := wire.Data.(type) {
	case appwire.ErrorData:
		data.ClientMutationID = clientMutationID
		wire.Data = data
	case map[string]any:
		updated := maps.Clone(data)
		updated["clientMutationId"] = clientMutationID
		wire.Data = updated
	default:
		return nil
	}
	return wire
}

// allowsPastFallbackAfterLiveReadFailure preserves atomic rejoin once a live
// relay is available. A subscribed local read with no rendezvous entry never
// acquired a relay, so it may still hydrate the persisted transcript.
func allowsPastFallbackAfterLiveReadFailure(source appsource.Source, params appwire.ThreadReadParams, err error) bool {
	if !params.Subscribe {
		return true
	}
	_, requiresLiveHandoff := source.(appsource.RelaySessionSource)
	return !requiresLiveHandoff || isDeadSessionError(err)
}

// hubLaunchConfigRoot resolves cfg.LaunchConfigRoot, falling back to
// cmdutil.DefaultConfigRoot() when unset (a zero-value WebConfig built
// directly, as some tests do).
func hubLaunchConfigRoot(cfg hubcore.WebConfig) string {
	if cfg.LaunchConfigRoot != "" {
		return cfg.LaunchConfigRoot
	}
	return cmdutil.DefaultConfigRoot()
}

// hubAuthStateRoot is where the auth controller keeps OAuth records: the
// registry's state root, because registry credential resolution reads
// auth/<instance>.json from there. The hub loads its registry at
// cmdutil.DefaultStateRoot() whatever hub_state_root says, so a record kept
// under HubStateRoot would be a login the registry, the credential probe and
// every spawned child never see. Before the first successful load, or with no
// registry wired (a bare test config), that same default is the answer.
func hubAuthStateRoot(reg *hubcore.ProviderRegistry) string {
	if reg != nil {
		if r := reg.Get(); r != nil {
			return r.StateRoot()
		}
	}
	return cmdutil.DefaultStateRoot()
}

func newHubAppServer(cfg hubcore.WebConfig, sources *appsource.Registry) *appserver.Server {
	return newHubAppServerWithNavigation(cfg, sources, nil, nil)
}

func newHubAppServerWithNavigation(cfg hubcore.WebConfig, sources *appsource.Registry, navigation *NavigationService, resolve topLevelSessionResolver) *appserver.Server {
	server, _, _ := newHubAppServerWithNavigationAndTrace(cfg, sources, navigation, resolve, nil)
	return server
}

// newHubAppServerWithNavigationAndTrace builds the RPC server and registers
// every handler. cfg.PluginManager, when set, is the one every plugin
// handler here uses (newWebServer constructs it and wires it, after this
// function returns, to the very server it built, so it wires nothing here);
// nil falls back to a fresh plugins.NewManager(cfg.PluginRoot), wired to this
// server directly, for a caller that never builds through newWebServer
// (most tests, and any embedder calling this constructor's exported
// wrappers directly).
func newHubAppServerWithNavigationAndTrace(cfg hubcore.WebConfig, sources *appsource.Registry, navigation *NavigationService, resolve topLevelSessionResolver, appwireTrace *appserver.WebSocketTrace) (*appserver.Server, *hubHostAdminController, *hubHostManager) {
	// One fallback registry when cfg carries no live one, built once here so
	// every host surface below — attach, management, and the admin proxy —
	// validates against the same instance: a host added at runtime must be
	// attachable and administrable, never "unknown" to a sibling handler
	// that built its own copy from the configured entries. main.go always
	// threads the live registry; the fallback is the embedder/test shape
	// (newWebServer nil-defaults its own cfg copy the same way).
	if cfg.RemoteHostRegistry == nil {
		cfg.RemoteHostRegistry = hostRegistryFromConfig(cfg)
	}
	capability := &appwire.NavigationCapability{Version: 1}
	var capabilityProvider func() *appwire.NavigationCapability
	if navigation != nil {
		capability = nil
		capabilityProvider = func() *appwire.NavigationCapability {
			return navigation.Capability()
		}
	}
	hubLogf := func(format string, args ...any) {
		fmt.Fprintf(os.Stderr, "[hub] "+format+"\n", args...)
	}
	server := appserver.NewServer(appserver.ServerConfig{
		ServerName:           "evener-hub",
		Version:              Version,
		SourceID:             "local",
		WebSocketTrace:       appwireTrace,
		Navigation:           capability,
		NavigationCapability: capabilityProvider,
		Logf:                 hubLogf,
		ConnectionAdmissionContext: func(ctx context.Context) context.Context {
			return admitSessionConnection(ctx, cfg)
		},
		RequestAdmissionContext: func(ctx context.Context, message appwire.Message) context.Context {
			return admitSessionRecovery(ctx, cfg, message)
		},
		SubscriptionAdmissionResolverV2: func(msg appwire.Message) appserver.SubscriptionAdmissionResolution {
			notSubscribe := appserver.SubscriptionAdmissionResolution{Intent: appserver.SubscriptionAdmissionNotSubscribe}
			if msg.Request == nil || (msg.Request.Method != appwire.MethodThreadRead && msg.Request.Method != appwire.MethodThreadUnsubscribe) {
				return notSubscribe
			}
			var params appwire.ThreadReadParams
			if msg.Request.Method == appwire.MethodThreadUnsubscribe {
				var unsubscribe appwire.ThreadUnsubscribeParams
				if json.Unmarshal(msg.Request.Params, &unsubscribe) != nil {
					return appserver.SubscriptionAdmissionResolution{Intent: appserver.SubscriptionAdmissionInvalid}
				}
				params.Ref, params.ThreadID = unsubscribe.Ref, unsubscribe.ThreadID
			} else if json.Unmarshal(msg.Request.Params, &params) != nil {
				return appserver.SubscriptionAdmissionResolution{Intent: appserver.SubscriptionAdmissionInvalid}
			}
			source, err := sourceForThread(sources, params.Ref, params.ThreadID)
			if err != nil {
				if _, parseErr := appwire.ParseRef(strings.TrimSpace(params.Ref)); params.Ref != "" && parseErr != nil {
					return appserver.SubscriptionAdmissionResolution{Intent: appserver.SubscriptionAdmissionInvalid}
				}
				if msg.Request.Method == appwire.MethodThreadRead && params.Subscribe {
					if past, ok := pastEntryForRead(cfg, params); ok && past.ID != "" {
						return appserver.SubscriptionAdmissionResolution{Key: "local:" + past.ID, Intent: appserver.SubscriptionAdmissionResolved}
					}
				}
				return appserver.SubscriptionAdmissionResolution{Intent: appserver.SubscriptionAdmissionUnresolved}
			}
			if msg.Request.Method == appwire.MethodThreadRead && !params.Subscribe && !relayOnThreadRead(source) {
				return notSubscribe
			}
			// Stable refs and current IDs name one pending admission, but
			// must not rewrite the relay's notification delivery identity.
			if daemon, ok := source.(*appsource.LocalDaemonSource); ok {
				ref, err := daemon.ResolveSubscriptionAdmission(params)
				if err != nil && msg.Request.Method == appwire.MethodThreadRead && params.Subscribe {
					if past, pastOK := pastEntryForRead(cfg, params); pastOK && past.ID != "" {
						return appserver.SubscriptionAdmissionResolution{Key: "local:" + past.ID, Intent: appserver.SubscriptionAdmissionResolved}
					}
					if cfg.Roster != nil {
						if key, ok := cfg.Roster.RestartRequiredRootRef(normalizedAdmissionRef(params)); ok {
							return appserver.SubscriptionAdmissionResolution{Key: key, Intent: appserver.SubscriptionAdmissionResolved}
						}
					}
				}
				if err != nil {
					if msg.Request.Method == appwire.MethodThreadUnsubscribe {
						delivery := normalizedAdmissionRef(params)
						if source != nil {
							if resolvedDelivery, _, deliveryErr := threadRelayTarget(source, params); deliveryErr == nil {
								delivery = resolvedDelivery
							}
						}
						if delivery != "" {
							return appserver.SubscriptionAdmissionResolution{Key: delivery, SecondaryKey: normalizedAdmissionRef(params), Intent: appserver.SubscriptionAdmissionUnresolved}
						}
					}
					return appserver.SubscriptionAdmissionResolution{Intent: appserver.SubscriptionAdmissionUnresolved}
				}
				if delivery, _, deliveryErr := threadRelayTarget(source, params); deliveryErr == nil {
					return appserver.SubscriptionAdmissionResolution{Key: ref.String(), SecondaryKey: delivery, Intent: appserver.SubscriptionAdmissionResolved}
				}
				return appserver.SubscriptionAdmissionResolution{Key: ref.String(), Intent: appserver.SubscriptionAdmissionResolved}
			}
			// A federated source is keyed by the ref it was addressed with
			// (relayDeliveryTarget), so a read carrying both the stable ref and
			// the thread's current ID admits under the identity a ref-only
			// thread/unsubscribe resolves.
			key, _, err := relayDeliveryTarget(source, params)
			if err != nil {
				return appserver.SubscriptionAdmissionResolution{Intent: appserver.SubscriptionAdmissionInvalid}
			}
			return appserver.SubscriptionAdmissionResolution{Key: key, Intent: appserver.SubscriptionAdmissionResolved}
		},
		Features: appwire.FeatureSet{
			ThreadList:                true,
			ThreadTurnsList:           true,
			TurnStart:                 true,
			TurnSteer:                 true,
			ThreadClear:               true,
			ThreadShutdown:            true,
			ForkFromTurn:              true,
			Tasks:                     true,
			TranscriptList:            true,
			ModelList:                 true,
			DirectoryComplete:         true,
			Auth:                      true,
			TranscriptDisplaySettings: true,
			KeybindingsSettings:       true,
		},
	})
	// One credentials store for every credential surface this server builds.
	// One store for every credential surface, resolved once by the auth
	// controller's constructor (hubCredentialStore) and read back below: each
	// surface resolving the fallback on its own let a hub built with no explicit
	// CredsStore — the embedder shape, and the one these constructors tolerate —
	// serve evener/auth/apiKey/set while refusing the push with "credential push
	// requires a local credentials store".
	authStateRoot := hubAuthStateRoot(cfg.Registry)
	authController := newHubAuthControllerWithStore(authStateRoot, cfg.CredsStore)
	authController.reg = cfg.Registry
	authController.providersConfigPath = cfg.ProvidersConfigPath
	authController.noUserLayer = cfg.NoUserLayer
	var instancesController *hubInstancesController
	if cfg.Registry != nil && cfg.ProvidersConfigPath != "" {
		instancesController = &hubInstancesController{
			reg:                 cfg.Registry,
			providersConfigPath: cfg.ProvidersConfigPath,
			auth:                authController,
		}
	}
	// cfg.PluginManager, when the caller (newWebServer) already set it, is the
	// one Manager every plugin surface below shares: this controller,
	// registerPluginAutoUpgradeHandlers, and — via cfg, which
	// registerThreadHandlers below captures by value — hubThreadStart and
	// hubSpawnSlashCatalog's ResolveForLaunch. A caller that never builds
	// through newWebServer (most tests, and any embedder calling
	// newHubAppServer/newHubAppServerWithNavigation directly) leaves it nil:
	// this constructs one and wires it to this server itself, the same way
	// newWebServer wires cfg.PluginManager, so plugin/marketplace mutations
	// and checkNow on this server still broadcast rather than going silent.
	mgr := cfg.PluginManager
	if mgr == nil {
		mgr = plugins.NewManager(cfg.PluginRoot)
		wirePluginStoreBroadcast(mgr, server)
	}
	cfg.PluginManager = mgr
	pluginsController := &hubPluginsController{mgr: mgr, launchConfigRoot: hubLaunchConfigRoot(cfg)}
	relayFunctions := newHubRelayFunctions(server, cfg, sources)
	if observeHubRelayFunctions != nil {
		observeHubRelayFunctions(relayFunctions)
	}
	registerThreadHandlers(server, cfg, sources, relayFunctions, hubLogf)
	registerThreadNameSetHandler(server, cfg, sources, navigation)
	registerAuthHandlers(server, authController)
	registerInstanceHandlers(server, instancesController)
	// launch.toml is user-editable configuration, so its root is the config
	// root, not HubStateRoot (machine-generated state).
	launchController := newHubLaunchController(hubLaunchConfigRoot(cfg), cfg.APILogDefault)
	registerLaunchHandlers(server, launchController)
	registerPluginHandlers(server, pluginsController)
	registerMobilePairingHandler(server, cfg)
	registerNavigationReadHandler(server, navigation)
	registerFavoriteHandler(server, cfg, navigation)
	registerArchiveHandler(server, cfg, sources, func() *NavigationService { return navigation })
	registerDaemonHandlers(server, cfg, sources)
	registerSessionDeleteHandler(server, nil)
	registerPinSectionHandlers(server, cfg, navigation, resolve)
	registerMiscHandlers(server, cfg, sources)
	// Component 06's Connect action: the browser-reachable explicit attach
	// trigger. It wraps the Ensure-backed dialing seam and is the only method
	// that may dial a remote host on the user's behalf.
	registerHostAttachHandler(server, cfg, sources, cfg.RemoteHostRegistry)
	// Component 08's host registry surface (add/list/status/remove from slice
	// 1; update from slice 2). Controller-local, never dials; add and remove
	// invalidate the manifest's sources, and an edit that changes the roots
	// retires and re-registers them, so the picker converges without a refresh
	// tick.
	// The manager, the live host registry, and the selected hub.toml path all
	// come from cfg — main.go threads the real sshconn.Manager, the one
	// registry shared with the attach handler, and the config path the
	// surface rewrites in place, so the surface is wired, not a
	// placeholder. It returns the manager so newWebServer can expose it
	// (main.go binds its event recorder to the SSH manager's lifecycle).
	hostManage := registerHostManageHandlers(server, sources, cfg, cfg.RemoteHostRegistry, navigation, hubLogf)
	registerPluginAutoUpgradeHandlers(server, pluginsController.mgr)
	registerTranscriptDisplayHandlers(server, cfg.TranscriptDisplayStore)
	registerKeybindingsHandlers(server, cfg.KeybindingsStore)
	registerAgentsDocHandlers(server, hubAgentsDocPath(cfg))
	// Component 07a: the remote-admin proxy and its host-tagged config
	// notification fan-out. Nothing here reads or writes a credential.
	//
	// The fan-out is a server-lifetime worker, not a per-connection one: it must
	// stay subscribed while no browser is connected so that a host's config
	// change is still relayed when one returns, and it re-subscribes itself
	// across client reconnects. Its context is the RPC server's own lifetime
	// handle, which Shutdown cancels when shutdown begins. Bound
	// this way the fan-out stops with the server it belongs to: a hub server
	// recreated in-process no longer leaves the previous server's fan-outs
	// subscribed forever (one goroutine per remote host, each still holding the
	// old server's sources and broadcaster, which would also duplicate every
	// host notification once a replacement subscribed too). Pinned by
	// TestHostAdminFanOutStopsWhenServerShutdown here and by
	// TestHostAdminFanOutStopsWhenContextCanceled at the controller level.
	// The binding only holds if something actually shuts the server down: the
	// hub's top-level lifecycle drains it unconditionally on the way out
	// (main.go), not only on the tracing path, and does so before the SSH
	// manager closes the transports these fan-outs read from.
	// The returned controller owns the per-host fan-out wakeups: newWebServer
	// (web.go) keeps the handle so main.go can bind hostAttached to the
	// sshconn EventAttached path, waking a backoff-sleeping fan-out the
	// moment its host's fresh channel is installed.
	// The push gets that same store, with the error of resolving it when there
	// was none: a credentials.toml that cannot be read is then one fact both
	// surfaces answer from (the controller's writes refuse, and so does the
	// push) instead of a nil store one of them dereferences.
	credsStore, credsErr := authController.credentialStore()
	hostAdmin := registerHostAdminHandlers(server.Lifetime(), server, cfg.RemoteHostRegistry, sources, credsStore, credsErr)
	return server, hostAdmin, hostManage
}

func normalizedAdmissionRef(params appwire.ThreadReadParams) string {
	if ref := strings.TrimSpace(params.Ref); ref != "" {
		return ref
	}
	if threadID := strings.TrimSpace(params.ThreadID); threadID != "" {
		return "local:" + threadID
	}
	return ""
}

// registerThreadHandlers registers the thread- and turn-lifecycle RPC handlers
// on the server. The relay closures (startRelay, startTurn, startRelayForThread)
// are constructed by newHubAppServer and passed in so the handlers close over
// the same relay state.
func registerThreadHandlers(
	server *appserver.Server,
	cfg hubcore.WebConfig,
	sources *appsource.Registry,
	relays hubRelayFunctions,
	logf func(format string, args ...any),
) {
	// evener/session/image is the AppWire counterpart of the local image routes:
	// the controller's host-qualified image routes proxy through it to the hub
	// that owns the session (multi-host component 05). It resolves only against
	// this hub's own local session state.
	appserver.HandleTyped(server.Router(), appwire.MethodEvenerSessionImage, func(_ context.Context, params appwire.SessionImageParams) (appwire.SessionImageResponse, error) {
		return sessionImageFromHub(cfg, params)
	})
	appserver.HandleTyped(server.Router(), appwire.MethodThreadList, func(ctx context.Context, params appwire.ThreadListParams) (appwire.ThreadListResponse, error) {
		return hubThreadList(ctx, cfg, sources, params)
	})
	appserver.HandleTyped(server.Router(), appwire.MethodThreadRead, func(ctx context.Context, params appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
		if err := appwire.ValidateThreadReadParams(params); err != nil {
			return appwire.ThreadReadResponse{}, err
		}
		itemLimit, err := appwire.NormalizeTranscriptItemLimit(params.ItemLimit)
		if err != nil {
			return appwire.ThreadReadResponse{}, err
		}
		if params.Ref != "" {
			if _, err := appwire.ParseRef(params.Ref); err != nil {
				return appwire.ThreadReadResponse{}, appwire.InvalidParams(err.Error())
			}
		}
		if cfg.Roster != nil {
			if _, required, ownershipErr := restartRequiredDaemon(ctx, cfg, params.Ref, params.ThreadID); required || ownershipErr != nil {
				if err := hubRosterRefresh(ctx, cfg.Roster); err != nil {
					// Refresh may fail on an unrelated marker. Recheck the target
					// before allowing its saved, non-authoritative projection.
					_, required, ownershipErr = restartRequiredDaemon(ctx, cfg, params.Ref, params.ThreadID)
					if ctx.Err() != nil || (!required && !isDaemonDiscoveryError(ownershipErr)) {
						return appwire.ThreadReadResponse{}, appwire.Unavailable(err.Error())
					}
				}
			}
		}
		source, err := sourceForThreadWithDeletionFence(ctx, cfg, sources, params.Ref, params.ThreadID)
		if err != nil {
			if isTargetDeletedError(err) {
				return appwire.ThreadReadResponse{}, err
			}
			resp, ok, pastErr := unavailableThreadReadResponse(ctx, cfg, sources, params)
			if pastErr != nil {
				return appwire.ThreadReadResponse{}, pastErr
			}
			if ok {
				return resp, nil
			}
			return appwire.ThreadReadResponse{}, err
		}
		read, err := relays.readThread(ctx, source, params)
		if err != nil {
			allowPast := allowsPastFallbackAfterLiveReadFailure(source, params, err)
			if _, local := localPastThreadID(params); local && cfg.Roster != nil && daemonOwnershipMayHaveChanged(err) {
				refreshErr := hubRosterRefresh(ctx, cfg.Roster)
				_, required, ownershipErr := restartRequiredDaemon(ctx, cfg, params.Ref, params.ThreadID)
				if ctx.Err() != nil {
					return appwire.ThreadReadResponse{}, ctx.Err()
				}
				if required {
					allowPast = true
					err = daemonRestartRequiredError(ctx, cfg, params.Ref, params.ThreadID, "")
				} else if isDaemonDiscoveryError(ownershipErr) {
					allowPast = true
				} else {
					if refreshErr != nil {
						return appwire.ThreadReadResponse{}, appwire.Unavailable(refreshErr.Error())
					}
					if ownershipErr != nil {
						return appwire.ThreadReadResponse{}, appwire.Unavailable(ownershipErr.Error())
					}
				}
			}
			if allowPast {
				saved, ok, pastErr := unavailableThreadReadResponse(ctx, cfg, sources, params)
				if pastErr != nil {
					return appwire.ThreadReadResponse{}, pastErr
				}
				if ok {
					return saved, nil
				}
			}
			return appwire.ThreadReadResponse{}, err
		}
		resp := read.response
		liveItemCandidatesEmpty := false
		if params.IncludeTurns {
			if read.hasItemCandidates {
				liveItemCandidatesEmpty = len(read.itemCandidates.Candidates.Candidates) == 0
			} else if candidates, candidateErr := itemCandidateResultFromReadResponse(resp); candidateErr == nil {
				liveItemCandidatesEmpty = len(candidates.Candidates.Candidates) == 0
			}
		}

		// A past session (a daemon that does not own the ref answers with an
		// empty live thread) gets its turns from the windowed past item page,
		// so that page runs BEFORE the merge and the merge never asks for the
		// full past-turn projection. The old order computed the O(transcript)
		// full projection first and then replaced its turns with this window,
		// making every click on a long session pay for a projection it
		// discarded in the same request.
		var pastPage *appwire.ThreadReadResponse
		if params.IncludeTurns && liveItemCandidatesEmpty {
			past, ok, pastErr := pastThreadItemReadResponse(ctx, cfg, params)
			if pastErr != nil {
				read.finish(false)
				return appwire.ThreadReadResponse{}, pastErr
			}
			if ok {
				pastPage = &past
			}
		}
		// The merge still supplies past turns when the item page could not (a
		// live source with candidates but no turns, or no past entry at all),
		// preserving the old includePastTurns decision exactly.
		wantPastTurns := params.IncludeTurns && pastPage == nil && len(resp.Thread.Turns) == 0
		resp.Thread, err = mergePastThreadForRead(ctx, cfg, params, resp.Thread, wantPastTurns)
		resp.Thread = applyThreadResumeRequirement(ctx, cfg, params.Ref, params.ThreadID, resp.Thread)
		resp.Thread = applyHubForkCapability(cfg, resp.Thread)
		if err != nil {
			read.finish(false)
			return appwire.ThreadReadResponse{}, err
		}
		if params.IncludeTurns {
			if pastPage != nil {
				resp.Thread.Turns = pastPage.Thread.Turns
				resp.OlderCursor = pastPage.OlderCursor
				resp.Thread = enrichSourcedThreadImages(source, resp.Thread)
				annotateThreadProjects([]appwire.Thread{resp.Thread})
			} else {
				candidates := transcriptItemCandidateResultFromSource(read.itemCandidates)
				if !read.hasItemCandidates {
					var candidateErr error
					candidates, candidateErr = sourceItemCandidateResultForRead(ctx, source, params, resp)
					if candidateErr != nil {
						read.finish(false)
						return appwire.ThreadReadResponse{}, candidateErr
					}
				}
				packed, packErr := packThreadReadItemCandidates(candidates, func(response appwire.ThreadReadResponse) (appwire.ThreadReadResponse, error) {
					response.Thread = threadWithPackedTurns(resp.Thread, response.Thread.Turns)
					// A live daemon's turns carry sha-addressed tool-result descriptors
					// with no route on them (the daemon does not serve the bytes; this
					// hub does), so route stamping stays inside the final packer.
					response.Thread = enrichSourcedThreadImages(source, response.Thread)
					annotateThreadProjects([]appwire.Thread{response.Thread})
					return response, nil
				}, itemLimit)
				if packErr != nil {
					read.finish(false)
					return appwire.ThreadReadResponse{}, packErr
				}
				resp = packed
			}
		} else {
			// A live daemon's turns carry sha-addressed tool-result descriptors with
			// no route on them (the daemon does not serve the bytes; this hub does),
			// so the route is stamped here before the file-backed pass adds any
			// /doc/image descriptors of its own.
			resp.Thread = enrichSourcedThreadImages(source, resp.Thread)
			annotateThreadProjects([]appwire.Thread{resp.Thread})
		}
		// Local forks copy persisted history in the hub. A live daemon's
		// own unsupported fork flag does not describe this hub-owned action.
		resp.Thread = applyHubForkCapability(cfg, resp.Thread)
		if err := appwire.ValidateThreadReadItemResponse(resp); err != nil {
			read.finish(false)
			return appwire.ThreadReadResponse{}, err
		}
		read.response = resp
		if read.handoff != nil {
			if !relays.captureThreadRead(ctx, params, read) {
				return appwire.ThreadReadResponse{}, appwire.SessionUnavailable("thread subscription is unavailable")
			}
		} else if sourceSupportsThreadRelay(source) && (params.Subscribe || relayOnThreadRead(source)) {
			// A source with no relay fan-out is never relayed, even for a
			// Subscribe read: startRelay calls SubscribeThread, so relaying one
			// would fail the read. Subscribing callers still get the snapshot.
			if err := relays.startRelay(ctx, source, params, resp.Thread); err != nil {
				return appwire.ThreadReadResponse{}, err
			}
		}
		return resp, nil
	})
	// thread/unsubscribe drops only the calling connection's downstream
	// subscription — the browser's own read of a thread it is navigating away
	// from. The relay key is derived by the same helper thread/read's relay
	// uses (relayDeliveryTarget), so the removal lands on the exact registry
	// entry Subscribe created. That helper is deliberately NOT
	// threadRelayTarget: for a federated source (anything but the local
	// daemon) the ref's suffix wins over the caller's bare threadId, because
	// the ref is the stable identity that survives an identity replacement
	// while the thread's current ID moves. Keying such a read by the threadId
	// registered the relay under "host:<currentID>" while a ref-addressed
	// unsubscribe resolved "host:<stableRef>", so the downstream entry — and
	// the source-side subscription behind it — was never dropped. Both ends
	// must keep resolving through relayDeliveryTarget; re-deriving either from
	// threadRelayTarget reintroduces that mismatch. Resolution deliberately
	// uses the plain registry lookup without session activation because an
	// unsubscribe must not start a session just to stop delivering to it. When
	// no source resolves, the ref's own namespace (parsed from the ref itself)
	// is the best key available; Unsubscribe is conn-scoped and idempotent, so
	// a missed key costs only a subscription the connection-close cleanup
	// reaps anyway.
	appserver.HandleTyped(server.Router(), appwire.MethodThreadUnsubscribe, func(ctx context.Context, params appwire.ThreadUnsubscribeParams) (appwire.EmptyResponse, error) {
		source, err := sourceForThread(sources, params.Ref, params.ThreadID)
		if err != nil {
			if isTargetDeletedError(err) {
				return appwire.EmptyResponse{}, err
			}
			if parsed, parseErr := appwire.ParseRef(strings.TrimSpace(params.Ref)); parseErr == nil && parsed.SourceID != "" {
				appserver.UnsubscribeLifecycle(ctx, parsed.SourceID+":"+parsed.ThreadID)
				return appwire.EmptyResponse{}, nil
			}
			appserver.UnsubscribeLifecycle(ctx, "local:"+strings.TrimSpace(params.ThreadID))
			return appwire.EmptyResponse{}, nil
		}
		relayKey, _, keyErr := relayDeliveryTarget(source, appwire.ThreadReadParams{ThreadID: params.ThreadID, Ref: params.Ref})
		if keyErr != nil {
			return appwire.EmptyResponse{}, keyErr
		}
		appserver.UnsubscribeLifecycle(ctx, relayKey)
		return appwire.EmptyResponse{}, nil
	})
	appserver.HandleTyped(server.Router(), appwire.MethodThreadTurnsList, func(ctx context.Context, params appwire.ThreadTurnsListParams) (appwire.ThreadTurnsListResponse, error) {
		if err := appwire.ValidateThreadTurnsListParams(params); err != nil {
			return appwire.ThreadTurnsListResponse{}, err
		}
		// Live source first; fall back to the saved transcript (paged on the
		// hub) for past/not-loaded sessions.
		source, srcErr := sourceForThreadWithDeletionFence(ctx, cfg, sources, params.Ref, params.ThreadID)
		if isTargetDeletedError(srcErr) {
			return appwire.ThreadTurnsListResponse{}, srcErr
		}
		var live appwire.ThreadTurnsListResponse
		var liveErr error
		var liveItemHandled bool
		if srcErr == nil {
			_, liveItemNative := source.(appsource.ItemCandidateSource)
			live, liveItemHandled, liveErr = listItemTurns(ctx, source, params, logf)
			if liveItemHandled && liveErr == nil && (!liveItemNative || len(live.Data) > 0) {
				return live, nil
			}
			if liveErr == nil && len(live.Data) > 0 {
				if meta, err := source.ReadThread(ctx, appwire.ThreadReadParams{Ref: params.Ref, ThreadID: params.ThreadID, IncludeTurns: false}); err == nil {
					// File-backed output-image enrichment is intentionally page-local
					// here: args can only be correlated from command-call items present
					// in this returned page (or on the completed item itself).
					thread := enrichSourcedThreadImages(source, appwire.Thread{
						ID:        meta.Thread.ID,
						SessionID: meta.Thread.SessionID,
						CWD:       meta.Thread.CWD,
						Turns:     live.Data,
					})
					live.Data = thread.Turns
				}
				return live, nil
			}
		}
		saved, ok, pastErr := pastThreadTurnsList(ctx, cfg, params)
		if pastErr != nil {
			return appwire.ThreadTurnsListResponse{}, pastErr
		}
		if ok {
			return saved, nil
		}
		if srcErr != nil {
			return appwire.ThreadTurnsListResponse{}, srcErr
		}
		return live, liveErr
	})
	appserver.HandleTyped(server.Router(), appwire.MethodEvenerSubagentPreview, func(ctx context.Context, params appwire.EvenerSubagentPreviewParams) (appwire.EvenerSubagentPreviewResponse, error) {
		ref := strings.TrimSpace(params.Ref)
		if ref == "" {
			return appwire.EvenerSubagentPreviewResponse{}, appwire.InvalidParams("ref required")
		}
		source, err := sourceForThreadWithDeletionFence(ctx, cfg, sources, ref, "")
		if err != nil {
			if isTargetDeletedError(err) {
				return appwire.EvenerSubagentPreviewResponse{}, err
			}
			thread, ok, pastErr := pastThreadForRead(ctx, cfg, appwire.ThreadReadParams{Ref: ref, IncludeTurns: true, ItemsView: "full"})
			if pastErr != nil {
				return appwire.EvenerSubagentPreviewResponse{}, pastErr
			}
			if ok {
				return subagentPreviewFromThread(thread, ref, params.Limit), nil
			}
			return appwire.EvenerSubagentPreviewResponse{}, err
		}
		resp, err := source.ReadThread(ctx, appwire.ThreadReadParams{Ref: ref, IncludeTurns: true, ItemsView: "full"})
		if err != nil {
			thread, ok, pastErr := pastThreadForRead(ctx, cfg, appwire.ThreadReadParams{Ref: ref, IncludeTurns: true, ItemsView: "full"})
			if pastErr != nil {
				return appwire.EvenerSubagentPreviewResponse{}, pastErr
			}
			if ok {
				return subagentPreviewFromThread(thread, ref, params.Limit), nil
			}
			return appwire.EvenerSubagentPreviewResponse{}, err
		}
		// A remote source returns the remote hub's origin-relative image routes,
		// which only resolve against the remote origin; neutralization must run
		// here exactly as it does on the thread/read path.
		resp.Thread = enrichSourcedThreadImages(source, resp.Thread)
		return subagentPreviewFromThread(resp.Thread, ref, params.Limit), nil
	})
	appserver.HandleTyped(server.Router(), appwire.MethodThreadStart, func(ctx context.Context, params appwire.ThreadStartParams) (appwire.ThreadStartResponse, error) {
		resp, err := hubThreadStart(ctx, cfg, sources, params)
		if err != nil {
			return appwire.ThreadStartResponse{}, err
		}
		if err := relays.startRelayForThread(ctx, resp.Thread); err != nil {
			appserver.Notify(ctx, appwire.NotifyWarning, appwire.WarningParams{
				ThreadID: resp.Thread.ID,
				Ref:      resp.Thread.Evener.Ref,
				Source:   "hub",
				Title:    "Live updates unavailable",
				Message:  "thread started, but Hub could not attach live updates: " + err.Error(),
			})
		}
		return resp, nil
	})
	appserver.HandleTyped(server.Router(), appwire.MethodThreadResume, func(ctx context.Context, params appwire.ThreadResumeParams) (appwire.ThreadResumeResponse, error) {
		resp, err := hubThreadResume(ctx, cfg, sources, params)
		if err != nil {
			return appwire.ThreadResumeResponse{}, err
		}
		if err := relays.startRelayForThread(ctx, resp.Thread); err != nil {
			return appwire.ThreadResumeResponse{}, err
		}
		return resp, nil
	})
	appserver.HandleTyped(server.Router(), appwire.MethodThreadFork, func(ctx context.Context, params appwire.ThreadForkParams) (appwire.ThreadForkResponse, error) {
		return hubThreadFork(ctx, cfg, sources, params)
	})
	appserver.HandleTyped(server.Router(), appwire.MethodTurnStart, func(ctx context.Context, params appwire.TurnStartParams) (appwire.TurnStartResponse, error) {
		if err := validateAppWireInputItems(params.Input); err != nil {
			return appwire.TurnStartResponse{}, appwire.InvalidParams(err.Error())
		}
		if strings.TrimSpace(params.ClientMutationID) == "" {
			return appwire.TurnStartResponse{}, appwire.InvalidParams("clientMutationId is required")
		}
		resolved := false
		// initialPreDispatch records whether the ORIGINAL attempt is proven to
		// have failed before anything was dispatched: it stayed true only when the
		// first attempt never resolved a source. Any other first-attempt failure
		// resolved the source first, so it may have dispatched; see
		// retryAfterResume's not-accepted rule.
		initialPreDispatch := false
		attemptStart := func() (appwire.TurnStartResponse, error) {
			source, err := withDeletionTargetOwnership(ctx, cfg, params.Ref, params.ThreadID, params.ClientMutationID, func() (appsource.Source, error) {
				return resolveTurnStartSource(sources, params.Ref, params.ThreadID)
			})
			if err != nil {
				return appwire.TurnStartResponse{}, err
			}
			resolved = true
			resp, err := relays.startTurn(ctx, source, params)
			return adoptResponseClientMutationID(resp, err, params.ClientMutationID)
		}
		// retryAfterResume runs the attempt a resume this request performed made
		// possible. A failure there that gives the caller no way to judge its own
		// mutation is wrapped in the resume path's own blocked-unknown envelope
		// (see blockedUnknownMutationError), so the prompt is retained for a
		// retry rather than left submitting forever.
		//
		// correlateRetryFailure's exemptions are consulted first, so a refusal
		// that already carries its own meaning is never rewritten: a refusal
		// naming this caller's mutation (recovery admission, daemon-restart-
		// required, a shape refusal) and a target deletion keep their own
		// outcome. Only a genuinely uncorrelated failure is left. It is reported
		// not-accepted only when the WHOLE operation is proven pre-dispatch --
		// both the retry and the original attempt failed before reaching a
		// source -- because a retry that fails source resolution proves nothing
		// about an earlier attempt that already reached a source and may have
		// applied the mutation before its response was lost. The caller's
		// cancellation is handled before either, since it is not a mutation
		// outcome at all.
		retryAfterResume := func() (appwire.TurnStartResponse, error) {
			resolved = false
			resp, err := attemptStart()
			if err == nil {
				return resp, nil
			}
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return resp, err
			}
			// The resumed daemon names the id it trimmed; rewrite it back to the
			// caller's own id before anything compares or returns it, so a
			// canonical match is recognized and the response still carries exactly
			// the id the caller submitted.
			err = adoptCallerMutationID(err, params.ClientMutationID)
			if wrapped := correlateRetryFailure(params.ClientMutationID, err); wrapped != nil {
				// A target deletion keeps its own outcome even pre-dispatch: a
				// deleted target never accepts the mutation, but the caller must
				// still be told the target is gone rather than re-offered it as
				// not-accepted. correlateRetryFailure hands it back enriched.
				if !resolved && initialPreDispatch && !isTargetDeletedError(err) {
					// Source resolution failed before the retry reached a source,
					// and the original attempt never reached one either, so nothing
					// was dispatched and the mutation's outcome is known -- not
					// accepted -- rather than unknown.
					return appwire.TurnStartResponse{}, appwire.MutationNotAccepted(params.ClientMutationID, err.Error())
				}
				return appwire.TurnStartResponse{}, wrapped
			}
			return resp, err
		}
		resp, err := attemptStart()
		if err == nil {
			return resp, nil
		}
		initialPreDispatch = !resolved
		if !resolved {
			if wire, ok := errors.AsType[appwire.WireError](err); ok && wire.Code == appwire.CodeInvalidParams {
				return appwire.TurnStartResponse{}, err
			}
			if isTargetDeletedError(err) || isDaemonRestartRequiredError(err) || isSessionRecoveryAdmissionError(err) {
				return appwire.TurnStartResponse{}, err
			}
			if _, resumeErr := resumeTurnStartThread(ctx, cfg, sources, appwire.ThreadResumeParams{Ref: params.Ref, Session: params.ThreadID}); resumeErr != nil {
				return appwire.TurnStartResponse{}, mutationResumeFailureError(cfg, params.Ref, params.ThreadID, params.ClientMutationID, resumeErr)
			}
			return retryAfterResume()
		}
		if isLifecycleRetiringError(err) {
			// The owning daemon refused the mutation because it is retiring and
			// still owns the session. Resolve the race under existing recovery
			// authority — admission fences, ownership alias locks, confirmed exit,
			// one resume — then retry the original request verbatim.
			if resumeErr := resumeAfterConfirmedRetirement(ctx, cfg, sources, params); resumeErr != nil {
				// This path fails with refusals that name no mutation at all -- the
				// retirement/lifecycle "retiring" refusal, an ownership or roster
				// error, an admission fence -- and its ownership-group fence can
				// name a sibling alias, so hand it the same treatment as every
				// other resume failure (see mutationResumeFailureError): the
				// deletion outcome only when it is this caller's own target's, and
				// otherwise the blocked-unknown envelope carrying the caller's id,
				// rather than an unnamed error the client can never correlate.
				return appwire.TurnStartResponse{}, mutationResumeFailureError(cfg, params.Ref, params.ThreadID, params.ClientMutationID, resumeErr)
			}
			return retryAfterResume()
		}
		if params.Ref != "" && !hubKnowsRef(cfg, params.Ref) {
			return appwire.TurnStartResponse{}, err
		}
		if !shouldResumeAfterTurnStartError(err) {
			return appwire.TurnStartResponse{}, err
		}
		if _, resumeErr := resumeTurnStartThread(ctx, cfg, sources, appwire.ThreadResumeParams{Ref: params.Ref, Session: params.ThreadID}); resumeErr != nil {
			return appwire.TurnStartResponse{}, mutationResumeFailureError(cfg, params.Ref, params.ThreadID, params.ClientMutationID, resumeErr)
		}
		return retryAfterResume()
	})
	appserver.HandleTyped(server.Router(), appwire.MethodTurnSteer, func(ctx context.Context, params appwire.TurnSteerParams) (appwire.TurnSteerResponse, error) {
		if err := validateAppWireInputItems(params.Input); err != nil {
			return appwire.TurnSteerResponse{}, appwire.InvalidParams(err.Error())
		}
		if strings.TrimSpace(params.ClientMutationID) == "" {
			return appwire.TurnSteerResponse{}, appwire.InvalidParams("clientMutationId is required")
		}
		resp, err := withDeletionTargetOwnership(ctx, cfg, params.Ref, params.ThreadID, params.ClientMutationID, func() (appwire.TurnSteerResponse, error) {
			source, err := sourceForThread(sources, params.Ref, params.ThreadID)
			if err != nil {
				return appwire.TurnSteerResponse{}, err
			}
			if err := ensureSkillInputSupported(ctx, source, params.Ref, params.ThreadID, params.Input); err != nil {
				return appwire.TurnSteerResponse{}, err
			}
			return source.SteerTurn(ctx, params)
		})
		return adoptResponseClientMutationID(resp, err, params.ClientMutationID)
	})
	appserver.HandleTyped(server.Router(), appwire.MethodTurnInterrupt, func(ctx context.Context, params appwire.TurnInterruptParams) (appwire.TurnInterruptResponse, error) {
		if strings.TrimSpace(params.ClientMutationID) == "" {
			return appwire.TurnInterruptResponse{}, appwire.InvalidParams("clientMutationId is required")
		}
		resp, err := withDeletionTargetOwnership(ctx, cfg, params.Ref, params.ThreadID, params.ClientMutationID, func() (appwire.TurnInterruptResponse, error) {
			source, err := sourceForThread(sources, params.Ref, params.ThreadID)
			if err != nil {
				return appwire.TurnInterruptResponse{}, err
			}
			return source.InterruptTurn(ctx, params)
		})
		return adoptResponseClientMutationID(resp, err, params.ClientMutationID)
	})
	appserver.HandleTyped(server.Router(), appwire.MethodEvenerSandboxEscalationResolve, func(ctx context.Context, params appwire.SandboxEscalationResolveParams) (appwire.EmptyResponse, error) {
		return withSessionActionOwnership(ctx, cfg, params.Ref, params.ThreadID, func() (appwire.EmptyResponse, error) {
			if err := refreshDaemonRestartRequiredError(ctx, cfg, params.Ref, params.ThreadID, ""); err != nil {
				return appwire.EmptyResponse{}, err
			}
			source, err := sourceForThread(sources, params.Ref, params.ThreadID)
			if err != nil {
				return appwire.EmptyResponse{}, err
			}
			return appwire.EmptyResponse{}, source.ResolveSandboxEscalation(ctx, params)
		})
	})
	appserver.HandleTyped(server.Router(), appwire.MethodTurnQueue, func(ctx context.Context, params appwire.TurnQueueParams) (appwire.TurnQueueResponse, error) {
		if err := validateAppWireInputItems(params.Input); err != nil {
			return appwire.TurnQueueResponse{}, appwire.InvalidParams(err.Error())
		}
		if strings.TrimSpace(params.ClientMutationID) == "" {
			return appwire.TurnQueueResponse{}, appwire.InvalidParams("clientMutationId is required")
		}
		// A queued message is a session mutation a past thread advertises, so it
		// carries the same exited == never-exited contract as turn/start: the hub
		// resumes the session and retries the write (withSessionResume). Without
		// it a queue write against a thread whose daemon has exited was refused
		// outright, which is exactly the route the web composer chooses for a
		// message sent while it already has a send in flight against a finished
		// session (appwire-client/typescript/sendQueueAvailability.ts) — so the
		// message was dropped instead of being queued behind the resume that send
		// had started.
		resp, err := withSessionResume(ctx, cfg, sources, params.Ref, params.ClientMutationID, func() (appwire.TurnQueueResponse, error) {
			source, err := sourceForThread(sources, params.Ref, "")
			if err != nil {
				// Resolution failed before anything reached a source, so a resume
				// retry that fails the same way proves nothing was dispatched.
				return appwire.TurnQueueResponse{}, preDispatchRefusalError{err}
			}
			if err := ensureSkillInputSupported(ctx, source, params.Ref, "", params.Input); err != nil {
				return appwire.TurnQueueResponse{}, err
			}
			return source.QueueTurn(ctx, params)
		})
		return adoptResponseClientMutationID(resp, err, params.ClientMutationID)
	})
	appserver.HandleTyped(server.Router(), appwire.MethodTurnDrainAsSteer, func(ctx context.Context, params appwire.TurnDrainAsSteerParams) (appwire.TurnDrainAsSteerResponse, error) {
		if err := validateAppWireInputItems(params.Input); err != nil {
			return appwire.TurnDrainAsSteerResponse{}, appwire.InvalidParams(err.Error())
		}
		if strings.TrimSpace(params.ClientMutationID) == "" {
			return appwire.TurnDrainAsSteerResponse{}, appwire.InvalidParams("clientMutationId is required")
		}
		resp, err := withDeletionTargetOwnership(ctx, cfg, params.Ref, "", params.ClientMutationID, func() (appwire.TurnDrainAsSteerResponse, error) {
			source, err := sourceForThread(sources, params.Ref, "")
			if err != nil {
				return appwire.TurnDrainAsSteerResponse{}, err
			}
			if err := ensureSkillInputSupported(ctx, source, params.Ref, "", params.Input); err != nil {
				return appwire.TurnDrainAsSteerResponse{}, err
			}
			return source.DrainAsSteer(ctx, params)
		})
		return adoptResponseClientMutationID(resp, err, params.ClientMutationID)
	})
	appserver.HandleTyped(server.Router(), appwire.MethodTurnPromoteQueuedAsSteer, func(ctx context.Context, params appwire.TurnPromoteQueuedAsSteerParams) (appwire.TurnPromoteQueuedAsSteerResponse, error) {
		if params.Index < 0 {
			return appwire.TurnPromoteQueuedAsSteerResponse{}, appwire.InvalidParams("index must be >= 0")
		}
		if strings.TrimSpace(params.ClientMutationID) == "" {
			return appwire.TurnPromoteQueuedAsSteerResponse{}, appwire.InvalidParams("clientMutationId is required")
		}
		if strings.TrimSpace(params.ExpectedEntryID) == "" {
			return appwire.TurnPromoteQueuedAsSteerResponse{}, appwire.InvalidParams("expectedEntryId is required")
		}
		resp, err := withDeletionTargetOwnership(ctx, cfg, params.Ref, "", params.ClientMutationID, func() (appwire.TurnPromoteQueuedAsSteerResponse, error) {
			source, err := sourceForThread(sources, params.Ref, "")
			if err != nil {
				return appwire.TurnPromoteQueuedAsSteerResponse{}, err
			}
			return source.PromoteQueuedAsSteer(ctx, params)
		})
		return adoptResponseClientMutationID(resp, err, params.ClientMutationID)
	})
	appserver.HandleTyped(server.Router(), appwire.MethodTurnCancelQueued, func(ctx context.Context, params appwire.TurnCancelQueuedParams) (appwire.TurnCancelQueuedResponse, error) {
		if params.Index < 0 {
			return appwire.TurnCancelQueuedResponse{}, appwire.InvalidParams("index must be >= 0")
		}
		if strings.TrimSpace(params.ClientMutationID) == "" {
			return appwire.TurnCancelQueuedResponse{}, appwire.InvalidParams("clientMutationId is required")
		}
		if strings.TrimSpace(params.ExpectedEntryID) == "" {
			return appwire.TurnCancelQueuedResponse{}, appwire.InvalidParams("expectedEntryId is required")
		}
		resp, err := withDeletionTargetOwnership(ctx, cfg, params.Ref, "", params.ClientMutationID, func() (appwire.TurnCancelQueuedResponse, error) {
			source, err := sourceForThread(sources, params.Ref, "")
			if err != nil {
				return appwire.TurnCancelQueuedResponse{}, err
			}
			return source.CancelQueued(ctx, params)
		})
		return adoptResponseClientMutationID(resp, err, params.ClientMutationID)
	})
	appserver.HandleTyped(server.Router(), appwire.MethodThreadClear, func(ctx context.Context, params appwire.ThreadClearParams) (appwire.ThreadClearResponse, error) {
		if strings.TrimSpace(params.ClientMutationID) == "" {
			return appwire.ThreadClearResponse{}, appwire.InvalidParams("clientMutationId is required")
		}
		if strings.TrimSpace(params.ExpectedInstanceID) == "" {
			return appwire.ThreadClearResponse{}, appwire.InvalidParams("expectedInstanceId is required")
		}
		resp, err := clearThreadWithResume(ctx, cfg, sources, params)
		return adoptResponseClientMutationID(resp, err, params.ClientMutationID)
	})
	appserver.HandleTyped(server.Router(), appwire.MethodThreadCompactStart, func(ctx context.Context, params appwire.ThreadCompactStartParams) (appwire.EmptyResponse, error) {
		return appwire.EmptyResponse{}, compactThreadWithResume(ctx, cfg, sources, params)
	})
	appserver.HandleTyped(server.Router(), appwire.MethodEvenerThreadForceStop, func(ctx context.Context, params appwire.ThreadForceStopParams) (appwire.EmptyResponse, error) {
		return appwire.EmptyResponse{}, forceStopThread(ctx, cfg, params, sources)
	})
	appserver.HandleTyped(server.Router(), appwire.MethodThreadShutdown, func(ctx context.Context, params appwire.ThreadShutdownParams) (appwire.EmptyResponse, error) {
		return appwire.EmptyResponse{}, shutdownThreadTolerateExited(ctx, cfg, sources, params)
	})
	appserver.HandleTyped(server.Router(), appwire.MethodThreadModelSet, func(ctx context.Context, params appwire.ThreadModelSetParams) (appwire.EmptyResponse, error) {
		return appwire.EmptyResponse{}, setThreadModelWithResume(ctx, cfg, sources, params)
	})
	appserver.HandleTyped(server.Router(), appwire.MethodThreadVisionModelSet, func(ctx context.Context, params appwire.ThreadVisionModelSetParams) (appwire.EmptyResponse, error) {
		return appwire.EmptyResponse{}, setThreadVisionModelWithResume(ctx, cfg, sources, params)
	})
	appserver.HandleTyped(server.Router(), appwire.MethodThreadReasoningEffortSet, func(ctx context.Context, params appwire.ThreadReasoningEffortSetParams) (appwire.EmptyResponse, error) {
		return withSessionActionOwnership(ctx, cfg, params.Ref, "", func() (appwire.EmptyResponse, error) {
			if err := refreshDaemonRestartRequiredError(ctx, cfg, params.Ref, "", ""); err != nil {
				return appwire.EmptyResponse{}, err
			}
			source, err := sourceForThread(sources, params.Ref, "")
			if err != nil {
				return appwire.EmptyResponse{}, err
			}
			// No capability gate: there is no reasoning-effort thread capability, and
			// the daemon/source already reject the call when it is unsupported (a
			// non-evener source, or a daemon without the effort hook).
			return appwire.EmptyResponse{}, source.SetThreadReasoningEffort(ctx, params)
		})
	})
	appserver.HandleTyped(server.Router(), appwire.MethodGoalSet, func(ctx context.Context, params appwire.GoalSetParams) (appwire.GoalSetResponse, error) {
		return setGoalWithResume(ctx, cfg, sources, params)
	})
	appserver.HandleTyped(server.Router(), appwire.MethodNotesHumanSet, func(ctx context.Context, params appwire.NotesHumanSetParams) (appwire.NotesHumanSetResponse, error) {
		resp, err := setNotesHumanWithResume(ctx, cfg, sources, params)
		return adoptResponseClientMutationID(resp, err, params.ClientMutationID)
	})
	appserver.HandleTyped(server.Router(), appwire.MethodUrlsRemove, func(ctx context.Context, params appwire.UrlsRemoveParams) (appwire.UrlsRemoveResponse, error) {
		resp, err := removeURLWithResume(ctx, cfg, sources, params)
		return adoptResponseClientMutationID(resp, err, params.ClientMutationID)
	})
}

// registerAuthHandlers registers the evener/auth/* RPC handlers, routed to the
// auth controller. Successful mutations broadcast evener/auth/updated.
func registerAuthHandlers(server *appserver.Server, authController *hubAuthController) {
	appserver.HandleTyped(server.Router(), appwire.MethodEvenerAuthStatus, func(_ context.Context, params appwire.AuthStatusParams) (appwire.AuthStatusResponse, error) {
		return authController.Status(params)
	})
	appserver.HandleTyped(server.Router(), appwire.MethodEvenerAuthTest, func(ctx context.Context, params appwire.AuthTestParams) (appwire.AuthTestResponse, error) {
		return authController.TestCredentials(ctx, params)
	})
	appserver.HandleTyped(server.Router(), appwire.MethodEvenerAuthLoginStart, func(_ context.Context, params appwire.AuthLoginStartParams) (appwire.AuthLoginStartResponse, error) {
		return authController.LoginStart(params)
	})
	appserver.HandleTyped(server.Router(), appwire.MethodEvenerAuthLoginComplete, func(ctx context.Context, params appwire.AuthLoginCompleteParams) (appwire.AuthLoginCompleteResponse, error) {
		resp, err := authLoginComplete(authController, ctx, params)
		notifyAuthWrite(server, err, resp.Status, params.OriginClientId)
		return resp, err
	})
	appserver.HandleTyped(server.Router(), appwire.MethodEvenerAuthLogout, func(ctx context.Context, params appwire.AuthLogoutParams) (appwire.AuthLogoutResponse, error) {
		resp, err := authController.Logout(params)
		notifyAuthWrite(server, err, resp.Status, params.OriginClientId)
		return resp, err
	})
	appserver.HandleTyped(server.Router(), appwire.MethodEvenerAuthList, func(_ context.Context, params appwire.EmptyParams) (appwire.AuthListResponse, error) {
		return authController.List(params)
	})
	// ApiKeySet, ApiKeyClear and CredentialJsonSet all answer with a bare
	// AuthStatusResponse, so one closure covers the broadcast every one of
	// them owes evener/auth/updated when its write applied.
	authStatusWrite := func(originClientID string, call func() (appwire.AuthStatusResponse, error)) (appwire.AuthStatusResponse, error) {
		resp, err := call()
		notifyAuthWrite(server, err, resp, originClientID)
		return resp, err
	}
	appserver.HandleTyped(server.Router(), appwire.MethodEvenerAuthApiKeySet, func(ctx context.Context, params appwire.AuthApiKeySetParams) (appwire.AuthStatusResponse, error) {
		return authStatusWrite(params.OriginClientId, func() (appwire.AuthStatusResponse, error) { return authController.ApiKeySet(params) })
	})
	appserver.HandleTyped(server.Router(), appwire.MethodEvenerAuthApiKeyClear, func(ctx context.Context, params appwire.AuthApiKeyClearParams) (appwire.AuthStatusResponse, error) {
		return authStatusWrite(params.OriginClientId, func() (appwire.AuthStatusResponse, error) { return authController.ApiKeyClear(params) })
	})
	appserver.HandleTyped(server.Router(), appwire.MethodEvenerAuthApiKeyConditionalSet, func(ctx context.Context, params appwire.ApiKeyConditionalSetParams) (appwire.ApiKeyConditionalSetResponse, error) {
		resp, err := authController.ApiKeyConditionalSet(params)
		// A skipped classification wrote nothing, so it owes no broadcast; a
		// landed write - and a write whose post-write status read failed -
		// broadcasts like every other credential write (notifyAuthWrite folds
		// the applied-but-unread case).
		if err != nil || resp.Action != appwire.ApiKeyConditionalSetActionSkipped {
			notifyAuthWrite(server, err, resp.Status, params.OriginClientId)
		}
		return resp, err
	})
	appserver.HandleTyped(server.Router(), appwire.MethodEvenerAuthCredentialJsonSet, func(ctx context.Context, params appwire.AuthCredentialJsonSetParams) (appwire.AuthStatusResponse, error) {
		return authStatusWrite(params.OriginClientId, func() (appwire.AuthStatusResponse, error) { return authController.CredentialJsonSet(params) })
	})
	appserver.HandleTyped(server.Router(), appwire.MethodEvenerAuthDeviceStart, func(ctx context.Context, params appwire.AuthDeviceStartParams) (appwire.AuthDeviceStartResponse, error) {
		return authController.DeviceStart(ctx, params)
	})
	appserver.HandleTyped(server.Router(), appwire.MethodEvenerAuthDevicePoll, func(ctx context.Context, params appwire.AuthDevicePollParams) (appwire.AuthDevicePollResponse, error) {
		resp, err := authDevicePoll(authController, ctx, params)
		// A pending poll wrote nothing, which the state says; an authorized one
		// wrote the record, whether or not the status read after it failed.
		if resp.State == "authorized" {
			notifyAuthWrite(server, err, *resp.Status, params.OriginClientId)
		}
		return resp, err
	})
}

// instanceRenameError is what the Edit handler returns to the client. A rename
// that stood but could not carry the instance's credentials carries
// ErrorInstanceRenamePersisted, so the client reports the standing rename and
// steers to the new name - the old name is gone and re-issuing the rename can
// only fail on a missing instance - instead of a failed save. Every other
// failure is returned unchanged. The bool says whether the rename stood, which
// is also what the handler broadcasts on.
func instanceRenameError(err error) (bool, error) {
	if _, persisted := errors.AsType[renamePersistedError](err); persisted {
		return true, appwire.InstanceRenamePersisted(err.Error())
	}
	return false, err
}

// instanceRemoveError is what the Remove handler returns to the client. A
// removal whose credential deletion applied before a later step failed carries
// ErrorInstanceRemoveApplied, so the client reconciles the standing removal -
// closing the confirmation, re-reading the listing, and dropping what it
// retained for the name - instead of presenting a failed remove whose retry
// targets an instance that is already gone. Every other failure is returned
// unchanged. The bool says whether the removal stood, which is also what the
// handler broadcasts on.
func instanceRemoveError(err error) (bool, error) {
	if _, applied := errors.AsType[removeAppliedError](err); applied {
		return true, appwire.InstanceRemoveApplied(err.Error())
	}
	return false, err
}

// registerInstanceHandlers registers the evener/instance/* CRUD handlers. When no
// instances controller is configured (providers.toml path unset), no handlers
// are registered — matching the original inline guard. Successful mutations
// broadcast evener/auth/updated (see notifyInstanceUpdated) so every other
// connected client refetches its now-stale instance list.
func registerInstanceHandlers(server *appserver.Server, instancesController *hubInstancesController) {
	if instancesController == nil {
		return
	}
	appserver.HandleTyped(server.Router(), appwire.MethodEvenerInstanceList, func(_ context.Context, _ appwire.EmptyParams) (appwire.InstanceListResponse, error) {
		return instancesController.List(), nil
	})
	// Every instance write answers the same way: a write that applied is
	// announced, whether or not the step after it failed (writeDidApply), and
	// the error still goes back to the client that asked, which is the only
	// one that can act on what was left behind. Only a CLEANLY applied write
	// echoes the caller's own originClientId, so that client recognizes its own
	// change; an applied write that still returned an error broadcasts without
	// one, because the issuing client cannot treat an errored mutation's echo as
	// its own success - the broadcast can beat the failing reply, and consuming
	// the marker then would suppress the invalidation a failed operation owes.
	// apply performs the mutation and returns the listing to answer with, so
	// the notify-and-answer block below is shared by every instance write. An
	// edit passes its lock-scoped capture (see edit); the rest answer with a
	// fresh List() via listAfter.
	instanceWrite := func(originClientId string, apply func() (appwire.InstanceListResponse, error)) (appwire.InstanceListResponse, error) {
		list, err := apply()
		if writeDidApply(err) {
			if err != nil {
				notifyInstanceUpdated(server, "")
			} else {
				notifyInstanceUpdated(server, originClientId)
			}
		}
		if err != nil {
			return appwire.InstanceListResponse{}, err
		}
		return list, nil
	}
	listAfter := func(mutate func() error) func() (appwire.InstanceListResponse, error) {
		return func() (appwire.InstanceListResponse, error) {
			if err := mutate(); err != nil {
				return appwire.InstanceListResponse{}, err
			}
			return instancesController.List(), nil
		}
	}
	appserver.HandleTyped(server.Router(), appwire.MethodEvenerInstanceCreate, func(_ context.Context, params appwire.InstanceCreateParams) (appwire.InstanceListResponse, error) {
		return instanceWrite(params.OriginClientId, listAfter(func() error { return instancesController.Create(params) }))
	})
	appserver.HandleTyped(server.Router(), appwire.MethodEvenerInstanceEdit, func(_ context.Context, params appwire.InstanceEditParams) (appwire.InstanceListResponse, error) {
		return instanceWrite(params.OriginClientId, func() (appwire.InstanceListResponse, error) {
			var list appwire.InstanceListResponse
			err := instancesController.edit(params, &list)
			if err == nil {
				return list, nil
			}
			// A rename that persisted before it failed is a write that stands,
			// so it is announced (writeApplied, which the mutation folded onto
			// its error) and the error goes back carrying
			// ErrorInstanceRenamePersisted, so the client that asked reports the
			// standing rename rather than a failed save.
			persisted, wireErr := instanceRenameError(err)
			if persisted {
				return list, writeApplied(wireErr)
			}
			return list, wireErr
		})
	})
	appserver.HandleTyped(server.Router(), appwire.MethodEvenerInstanceRemove, func(_ context.Context, params appwire.InstanceRemoveParams) (appwire.InstanceListResponse, error) {
		return instanceWrite(params.OriginClientId, func() (appwire.InstanceListResponse, error) {
			// A removal whose credential deletion applied before it failed is a
			// write that stands, so it is announced (writeApplied, which the
			// mutation folded onto its error) and the error goes back carrying
			// ErrorInstanceRemoveApplied, so the client that asked reconciles the
			// standing removal rather than a failed remove it would retry.
			applied, wireErr := instanceRemoveError(instancesController.Remove(params))
			if applied {
				return appwire.InstanceListResponse{}, writeApplied(wireErr)
			}
			if wireErr != nil {
				return appwire.InstanceListResponse{}, wireErr
			}
			return instancesController.List(), nil
		})
	})
	appserver.HandleTyped(server.Router(), appwire.MethodEvenerInstanceSetDefault, func(_ context.Context, params appwire.InstanceSetDefaultParams) (appwire.InstanceListResponse, error) {
		return instanceWrite(params.OriginClientId, listAfter(func() error { return instancesController.SetDefault(params) }))
	})
	appserver.HandleTyped(server.Router(), appwire.MethodEvenerInstanceSetModelDisabled, func(_ context.Context, params appwire.InstanceSetModelDisabledParams) (appwire.InstanceListResponse, error) {
		return instanceWrite(params.OriginClientId, listAfter(func() error { return instancesController.SetModelDisabled(params) }))
	})
	appserver.HandleTyped(server.Router(), appwire.MethodEvenerInstanceRefreshModels, func(ctx context.Context, params appwire.InstanceRefreshModelsParams) (appwire.InstanceListResponse, error) {
		return instanceWrite(params.OriginClientId, listAfter(func() error { return instancesController.RefreshModels(ctx, params) }))
	})
}

// registerLaunchHandlers registers the evener/launch/* RPC handlers, routed to the
// launch controller. Successful layer/trust mutations broadcast evener/launch/updated.
func registerLaunchHandlers(server *appserver.Server, launchController *hubLaunchController) {
	appserver.HandleTyped(server.Router(), appwire.MethodEvenerLaunchResolve, func(ctx context.Context, params appwire.LaunchConfigResolveParams) (appwire.LaunchConfigResolved, error) {
		return launchController.Resolve(ctx, params)
	})
	appserver.HandleTyped(server.Router(), appwire.MethodEvenerLaunchSchema, func(ctx context.Context, params appwire.EmptyParams) (appwire.LaunchOptionSchemaResponse, error) {
		return launchController.Schema(ctx, params)
	})
	appserver.HandleTyped(server.Router(), appwire.MethodEvenerLaunchGetLayer, func(ctx context.Context, params appwire.LaunchConfigGetLayerParams) (appwire.LaunchConfigLayer, error) {
		return launchController.GetLayer(ctx, params)
	})
	appserver.HandleTyped(server.Router(), appwire.MethodEvenerLaunchSetLayer, func(ctx context.Context, params appwire.LaunchConfigSetLayerParams) (appwire.LaunchConfigResolved, error) {
		resp, err := launchController.SetLayer(ctx, params)
		if writeDidApply(err) {
			notifyLaunchUpdated(server, params.CWD, params.Layer)
		}
		return resp, err
	})
	appserver.HandleTyped(server.Router(), appwire.MethodEvenerLaunchTrustRepo, func(ctx context.Context, params appwire.LaunchConfigTrustRepoParams) (appwire.LaunchConfigResolved, error) {
		resp, err := launchTrustRepo(launchController, ctx, params)
		if writeDidApply(err) {
			notifyLaunchUpdated(server, params.CWD, "repo")
		}
		return resp, err
	})
}

// registerPluginHandlers registers the evener/marketplace/* and evener/plugin/*
// RPC handlers, routed to the plugins controller. Every mutation here runs
// through pluginsController.mgr, which newWebServer wires (wirePluginStoreBroadcast)
// to broadcast evener/marketplace/updated and/or evener/plugin/updated for
// whatever its own lockStore session actually wrote — the sole notification
// path for this surface; no handler below calls
// notifyMarketplaceUpdated/notifyPluginUpdated itself.
func registerPluginHandlers(server *appserver.Server, pluginsController *hubPluginsController) {
	appserver.HandleTyped(server.Router(), appwire.MethodEvenerMarketplaceList, func(ctx context.Context, _ appwire.EmptyParams) (appwire.MarketplaceListResponse, error) {
		return pluginsController.ListMarketplaces(ctx)
	})
	appserver.HandleTyped(server.Router(), appwire.MethodEvenerMarketplaceAdd, func(ctx context.Context, params appwire.MarketplaceAddParams) (appwire.MarketplaceListResponse, error) {
		return pluginsController.AddMarketplace(ctx, params)
	})
	appserver.HandleTyped(server.Router(), appwire.MethodEvenerMarketplaceRemove, func(ctx context.Context, params appwire.MarketplaceNameParams) (appwire.MarketplaceListResponse, error) {
		return pluginsController.RemoveMarketplace(ctx, params)
	})
	appserver.HandleTyped(server.Router(), appwire.MethodEvenerMarketplaceRefresh, func(ctx context.Context, params appwire.MarketplaceNameParams) (appwire.MarketplaceListResponse, error) {
		return pluginsController.RefreshMarketplace(ctx, params)
	})
	appserver.HandleTyped(server.Router(), appwire.MethodEvenerMarketplaceEdit, func(ctx context.Context, params appwire.MarketplaceEditParams) (appwire.MarketplaceListResponse, error) {
		return pluginsController.EditMarketplace(ctx, params)
	})
	appserver.HandleTyped(server.Router(), appwire.MethodEvenerMarketplaceBrowse, func(ctx context.Context, params appwire.MarketplaceBrowseParams) (appwire.MarketplaceBrowseResponse, error) {
		return pluginsController.Browse(ctx, params)
	})
	appserver.HandleTyped(server.Router(), appwire.MethodEvenerPluginList, func(ctx context.Context, _ appwire.EmptyParams) (appwire.PluginListResponse, error) {
		return pluginsController.ListPlugins(ctx)
	})
	appserver.HandleTyped(server.Router(), appwire.MethodEvenerPluginPreview, func(ctx context.Context, params appwire.PluginPreviewParams) (appwire.PluginPreviewResponse, error) {
		return pluginsController.Preview(ctx, params)
	})
	appserver.HandleTyped(server.Router(), appwire.MethodEvenerPluginInstall, func(ctx context.Context, params appwire.PluginRefParams) (appwire.PluginListResponse, error) {
		return pluginsController.Install(ctx, params)
	})
	appserver.HandleTyped(server.Router(), appwire.MethodEvenerPluginUpgrade, func(ctx context.Context, params appwire.PluginRefParams) (appwire.PluginListResponse, error) {
		return pluginsController.Upgrade(ctx, params)
	})
	appserver.HandleTyped(server.Router(), appwire.MethodEvenerPluginRemove, func(ctx context.Context, params appwire.PluginRefParams) (appwire.PluginListResponse, error) {
		return pluginsController.Remove(ctx, params)
	})
	appserver.HandleTyped(server.Router(), appwire.MethodEvenerPluginEnable, func(ctx context.Context, params appwire.PluginRefParams) (appwire.PluginListResponse, error) {
		return pluginsController.Enable(ctx, params)
	})
	appserver.HandleTyped(server.Router(), appwire.MethodEvenerPluginDisable, func(ctx context.Context, params appwire.PluginRefParams) (appwire.PluginListResponse, error) {
		return pluginsController.Disable(ctx, params)
	})
	appserver.HandleTyped(server.Router(), appwire.MethodEvenerPluginSetAutoUpgrade, func(ctx context.Context, params appwire.PluginSetAutoUpgradeParams) (appwire.PluginListResponse, error) {
		return pluginsController.SetAutoUpgrade(ctx, params)
	})
}

// notifyMarketplaceUpdated broadcasts a evener/marketplace/updated notification
// to all connected clients.
func notifyMarketplaceUpdated(server hostNotificationBroadcaster) {
	server.BroadcastAll(appwire.NotifyEvenerMarketplaceUpdated, map[string]string{})
}

// notifyPluginUpdated broadcasts a evener/plugin/updated notification to all
// connected clients.
func notifyPluginUpdated(server hostNotificationBroadcaster) {
	server.BroadcastAll(appwire.NotifyEvenerPluginUpdated, map[string]string{})
}

// wirePluginStoreBroadcast installs an OnStoreChanged callback (issue #1634)
// on mgr that broadcasts evener/marketplace/updated and/or
// evener/plugin/updated for whatever a lockStore session actually wrote —
// the sole path that broadcasts a plugin-store write reaching every Manager
// this package constructs: the RPC handlers above, the auto-upgrade daemon
// and its checkNow handler, hubSeedDefaults, hubPluginGC, and the resolver
// path a launch reaches through cfg.PluginManager, without any of them
// needing to call notify*/know this happened.
//
// server takes hostNotificationBroadcaster (app_host_admin.go), the same
// *appserver.Server-shaped seam the host-admin fan-out tests drive with a
// recorder, rather than *appserver.Server itself, so a test can assert on
// the real broadcasts this sends without standing up a connection.
func wirePluginStoreBroadcast(mgr *plugins.Manager, server hostNotificationBroadcaster) {
	mgr.OnStoreChanged(func(changed plugins.StoreChanged) {
		if changed.Marketplaces {
			notifyMarketplaceUpdated(server)
		}
		if changed.Plugins {
			notifyPluginUpdated(server)
		}
	})
}

// recentProjectDirsLimit is the session creation flows' path-dropdown option
// count (issue #35): the 15 most recently used projects.
const recentProjectDirsLimit = 15

// registerMiscHandlers registers hub RPC handlers that are not owned by a
// focused controller registration.
func registerMiscHandlers(server *appserver.Server, cfg hubcore.WebConfig, sources *appsource.Registry) {
	appserver.HandleTyped(server.Router(), appwire.MethodEvenerUpgrade, hubUpgrade)
	appserver.HandleTyped(server.Router(), appwire.MethodEvenerUpdateCheck, hubUpdateCheck)
	appserver.HandleTyped(server.Router(), appwire.MethodEvenerUpdateApply, hubUpdateApply)
	appserver.HandleTyped(server.Router(), appwire.MethodEvenerSearch, func(_ context.Context, params appwire.SearchParams) (appwire.SearchResponse, error) {
		return hubSearch(cfg, params), nil
	})
	appserver.HandleTyped(server.Router(), appwire.MethodModelList, func(ctx context.Context, params appwire.ModelListParams) (appwire.ModelListResponse, error) {
		return hubModelList(ctx, cfg, sources, params)
	})
	appserver.HandleTyped(server.Router(), appwire.MethodEvenerTasksList, func(ctx context.Context, params appwire.TaskListParams) (appwire.TaskListResponse, error) {
		return hubTasksList(ctx, cfg, sources, params)
	})
	appserver.HandleTyped(server.Router(), appwire.MethodEvenerJobsList, func(ctx context.Context, params appwire.JobsListParams) (appwire.JobsListResponse, error) {
		return hubJobsList(ctx, cfg, sources, params)
	})
	appserver.HandleTyped(server.Router(), appwire.MethodEvenerJobsOutput, func(ctx context.Context, params appwire.JobsOutputParams) (appwire.JobsOutputResponse, error) {
		return hubJobsOutput(ctx, cfg, sources, params)
	})
	appserver.HandleTyped(server.Router(), appwire.MethodEvenerThreadTranscriptsList, func(ctx context.Context, params appwire.ThreadTranscriptListParams) (appwire.ThreadTranscriptListResponse, error) {
		return hubThreadTranscriptList(ctx, cfg, sources, params)
	})
	appserver.HandleTyped(server.Router(), appwire.MethodEvenerPathsComplete, func(_ context.Context, params appwire.PathsCompleteParams) (appwire.PathsCompleteResponse, error) {
		return fspaths.CompletePaths(params)
	})
	appserver.HandleTyped(server.Router(), appwire.MethodEvenerDirsCreate, func(_ context.Context, params appwire.DirsCreateParams) (appwire.DirsCreateResponse, error) {
		return hubDirsCreate(cfg, params)
	})
	appserver.HandleTyped(server.Router(), appwire.MethodEvenerProjectsRecent, func(_ context.Context, params appwire.ProjectsRecentParams) (appwire.ProjectsRecentResponse, error) {
		limit := params.Limit
		if limit <= 0 {
			limit = recentProjectDirsLimit
		}
		// Non-nil even when there is nothing to report: a nil slice marshals as
		// JSON null, which contradicts the wire type's own non-nullable
		// `data: string[]` and crashes any client that trusts it.
		dirs := []string{}
		if cfg.Past != nil {
			dirs = append(dirs, cfg.Past.RecentProjectDirs(limit)...)
		}
		return appwire.ProjectsRecentResponse{Data: dirs}, nil
	})
	appserver.HandleTyped(server.Router(), appwire.MethodEvenerPathValidate, func(_ context.Context, params appwire.PathValidateParams) (appwire.PathValidateResponse, error) {
		return fspaths.ValidateLaunchPath(params), nil
	})
	appserver.HandleTyped(server.Router(), appwire.MethodEvenerGitHead, func(ctx context.Context, params appwire.GitHeadParams) (appwire.GitHeadResponse, error) {
		return hubGitHead(ctx, cfg, params), nil
	})
	appserver.HandleTyped(server.Router(), appwire.MethodEvenerHarnessesList, func(context.Context, appwire.HarnessListParams) (appwire.HarnessListResponse, error) {
		return appwire.HarnessListResponse{Data: launchHarnessDescriptors()}, nil
	})
	appserver.HandleTyped(server.Router(), appwire.MethodEvenerCommandList, func(ctx context.Context, _ appwire.EmptyParams) (appwire.CommandListResponse, error) {
		return hubCommandList(ctx, cfg)
	})
	appserver.HandleTyped(server.Router(), appwire.MethodEvenerSpawnSlashCatalog, func(ctx context.Context, params appwire.SpawnSlashCatalogParams) (appwire.SpawnSlashCatalogResponse, error) {
		return hubSpawnSlashCatalog(ctx, cfg, params)
	})
	appserver.HandleTyped(server.Router(), appwire.MethodEvenerSettingsOverview, func(ctx context.Context, _ appwire.EmptyParams) (appwire.SettingsOverviewResponse, error) {
		return hubSettingsOverview(ctx, cfg)
	})
}

// hubCommandList answers evener/command/list by loading every plugin a real
// session would load — internal/plugins.Manager.ResolveForLaunch (explicit
// --plugin-dir-equivalent PluginDirs first, then every installed+enabled
// registry entry) — and flattening their discovered slash commands into a
// catalog. This used to mirror discoverPluginsForSettings's display-only scan
// (web_settings.go, pluginDirsFromConfig: an immediate-subdirectory glob of
// the plugin store) instead, which could never see a plugin installed via the
// marketplace/registry system (living at cache/<marketplace>/<plugin>/<sha>,
// not a direct child of the plugins root) — so a registry-installed plugin's
// commands never appeared here even though a spawned session loaded them.
// The hub catalog combines enabled plugin commands with evener-wide commands.
// Evener-wide discovery receives a nil environment because the hub is
// multi-project: project commands are per-session and must never appear here.
// Loading is fail-soft (plugin.LoadAllFailSoft), so one broken or mid-edit
// plugin dir cannot blank out the whole command catalog.
func hubCommandList(ctx context.Context, cfg hubcore.WebConfig) (appwire.CommandListResponse, error) {
	resolution, err := hubResolvePlugins(ctx, cfg.PluginRoot, cfg.PluginDirs, nil, cfg.PluginManager)
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "warning: listing plugins: %v\n", err)
	}
	loaded, _ := plugin.LoadAllFailSoft(resolution.SelectedDirs)
	evenerwide, _ := plugin.DiscoverEvenerWideCommands(nil)
	merged := plugin.MergeCommands(loaded, evenerwide)
	var commands []appwire.CommandDescriptor
	for _, cmd := range merged {
		commands = append(commands, appwire.CommandDescriptor{
			Name:         cmd.Name,
			PluginName:   cmd.PluginName,
			Description:  cmd.Description,
			ArgumentHint: cmd.ArgumentHint,
			Source:       cmd.Source,
		})
	}
	sortCommandDescriptors(commands)
	return appwire.CommandListResponse{Commands: commands}, nil
}

// sortCommandDescriptors orders command rows by (Name, PluginName, Source).
// It is stable so rows with equal keys keep their discovery order instead of
// shuffling nondeterministically under sort.Slice's unstable pdqsort.
func sortCommandDescriptors(commands []appwire.CommandDescriptor) {
	sort.SliceStable(commands, func(i, j int) bool {
		if commands[i].Name != commands[j].Name {
			return commands[i].Name < commands[j].Name
		}
		if commands[i].PluginName != commands[j].PluginName {
			return commands[i].PluginName < commands[j].PluginName
		}
		return commands[i].Source < commands[j].Source
	})
}

// notifyAuthUpdated broadcasts a evener/auth/updated notification to all connected clients.
// originClientId is the originating client's own id, echoed back from the
// mutation that produced this broadcast; empty when the caller sent none.
func notifyAuthUpdated(server *appserver.Server, provider, activeSource, originClientId string) {
	// Still map[string]string, not appwire.EvenerAuthUpdatedParams (kcb5):
	// provider/activeSource (from AuthStatus) are legitimately empty when no
	// provider is active, but this map always emits both keys anyway; both
	// fields are tagged `omitempty` on the struct, so a typed literal would
	// drop them whenever blank. Not provably byte-identical; left as a map.
	payload := map[string]string{
		"provider":     provider,
		"activeSource": activeSource,
	}
	// The originator id rides along only when the caller sent one. An empty
	// value (the TUI, an older web build) must leave the payload exactly as it
	// was before the field existed: consumers with no id on the notification
	// keep their provider-plus-timing fallback, and one that would see an
	// empty-string id would attribute nothing.
	if originClientId != "" {
		payload["originClientId"] = originClientId
	}
	server.BroadcastAll(appwire.NotifyEvenerAuthUpdated, payload)
}

// notifyAuthWrite broadcasts what a credential or OAuth write actually
// knows. A clean read (err == nil) has the real provider and active source,
// so it broadcasts those. A write that applied but whose status read failed
// (writeApplied's shape) has neither: status is the read's own zero-value
// fallback (AuthStatusResponse{Provider: name}, ActiveSource == ""), and
// broadcasting that would announce "nothing active" as fact when the truth
// was never read. That case broadcasts the no-data form notifyInstanceUpdated
// uses instead, so clients refetch rather than adopt a fabricated
// activeSource. A write that never applied broadcasts nothing.
func notifyAuthWrite(server *appserver.Server, err error, status appwire.AuthStatusResponse, originClientID string) {
	switch {
	case err == nil:
		notifyAuthUpdated(server, status.Provider, status.ActiveSource, originClientID)
	case writeDidApply(err):
		// The no-data form, deliberately WITHOUT the origin. The originating
		// credential mutation failed, so it cannot attribute this broadcast as
		// its own success anyway - and echoing the origin would make this
		// provider-less broadcast structurally identical to a provider-instance
		// echo, letting it consume an instance mutation's marker and turn that
		// mutation's own echo foreign.
		notifyInstanceUpdated(server, "")
	}
}

// notifyInstanceUpdated broadcasts a evener/auth/updated notification to all
// connected clients after a provider-instance CRUD mutation (create, edit,
// remove, setDefault, setModelDisabled, refreshModels), and it is also the
// no-data form notifyAuthWrite uses for a credential write that applied but
// whose status read failed. It deliberately reuses the auth/updated channel
// rather than minting a new notification type: the client-side handler
// (notifications.js) already treats evener/auth/updated as payload-agnostic —
// "credentials or instances changed, refetch" — reloading both the instances
// panel and the providers settings tab on receipt.
//
// Provider and activeSource stay empty: there is no single provider/activeSource
// pair that honestly summarizes "the instance list changed." originClientId is
// the originating client's own id, echoed back from the mutation that produced
// this broadcast so that client can recognize its own echo by id instead of
// refetching as if another client had changed the list; empty when the caller
// sent none (an older build, the TUI), which leaves the payload exactly as it
// was before the field existed. The credential no-data form notifyAuthWrite
// uses passes no origin even when the caller sent one: that caller's own marker
// was retired by the failure, so the broadcast is unattributable, and carrying
// an id would make it look like a provider-instance echo to the SDK.
func notifyInstanceUpdated(server *appserver.Server, originClientId string) {
	server.BroadcastAll(appwire.NotifyEvenerAuthUpdated, appwire.EvenerAuthUpdatedParams{OriginClientId: originClientId})
}

// notifyLaunchUpdated broadcasts a evener/launch/updated notification to all connected clients.
func notifyLaunchUpdated(server *appserver.Server, cwd, layer string) {
	server.BroadcastAll(appwire.NotifyEvenerLaunchUpdated, appwire.EvenerLaunchUpdatedParams{
		CWD:   cwd,
		Layer: layer,
	})
}
