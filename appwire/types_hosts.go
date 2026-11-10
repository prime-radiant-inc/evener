package appwire

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
)

// HostRequestParams is the evener/host/request payload (component 07a): one
// hub-scoped admin RPC addressed to a named remote host. The controller's UI
// sends this instead of calling the admin method directly when the selected
// target is a remote host, so there is exactly one place that resolves the
// host, decides whether the method may be forwarded at all, and refuses an
// offline host rather than falling back to local execution.
//
// Host is a component-03 source ID. "local" is reserved for the controller's
// own hub and is never a valid remote target.
//
// Method must name a method in the proxy's exact allow-list; an unlisted
// method — including any catalog method added after that list was written —
// is refused, and the proxy is never a generic hub-to-hub tunnel.
//
// Params is the forwarded method's own params, passed through unchanged. The
// result of this call is likewise that method's own result, verbatim; the
// proxy wraps it in no envelope of its own.
//
// A forwarded method that mutates the remote host — the non-idempotent half of
// the proxy's allow-list — is answered with an explicit outcome-unknown error
// when the response is lost after the request was sent: the change may or may
// not have been applied. That error carries
// evenerErrorInfo "mutationOutcomeUnknown" and retryDisposition "blocked"
// instead of the SessionUnavailable a read-only forward reports, so a caller
// knows not to retry a mutation with no idempotency key. A refusal the remote
// itself sends keeps its own code and message on both paths.
type HostRequestParams struct {
	Host   string          `json:"host"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params,omitempty"`
}

// HostForwardedResult names evener/host/request's result in the protocol
// catalog. The result is the forwarded method's own result, re-serialized
// verbatim: there is no single Go shape to declare, so the catalog declares an
// opaque object rather than fields the proxy does not add. The handler itself
// returns json.RawMessage; this marker exists so the catalog and the generated
// protocol reference (docs/appwire-protocol.md, types.gen.ts) name the
// passthrough honestly instead of borrowing an unrelated type.
//
// A typed TypeScript client therefore does NOT get the forwarded method's own
// result type out of MethodTypes: internal/appwirets maps a named catalog type
// to an interface (and a struct with no fields to an empty one), so
// MethodTypes["evener/host/request"]["result"] is `{}` — "the passthrough is an
// object, the catalog cannot say more". `unknown` is not expressible here: that
// mapping belongs to a field typed json.RawMessage, and a top-level catalog
// entry always names an interface. The declared contract is deliberately
// weaker than the payload, so a caller has to widen the result and cast it to
// the method it forwarded. Nothing does that yet: the host-scoped request seam
// that will is component 07b (branch multi-host-pr07b-host-routing), which has
// not landed here. Read this marker as "an opaque JSON object the proxy passes
// through", never as an empty result.
type HostForwardedResult struct{}

// HostPushCredentialsParams is the evener/host/pushCredentials payload
// (component 07c): the component-03 source ID of one configured remote host to
// copy the controller's local provider-instance keys to. The push reads the
// controller's local store and writes only the host's; the unit of the push is
// the local credentials-store entry, whose key is the instance name.
type HostPushCredentialsParams struct {
	Host string `json:"host"`
}

// HostPushCredentialsResponse is the per-instance report of one push: exactly
// one HostCredentialPushResult for every entry the local credentials store
// listed, in the store's sorted name order, plus the host the push targeted. An
// entry whose value was cleared before it could be read is still reported (a
// skip), so the row count always matches the store's entry count — a report
// that silently omitted an entry would read as "nothing was there to push".
type HostPushCredentialsResponse struct {
	Host    string                     `json:"host"`
	Results []HostCredentialPushResult `json:"results"`
}

// HostCredentialPushResult is one local entry's outcome. Action is one of the
// HostCredentialPush* values; Reason explains a skip or names the failure and
// is empty for a write that landed.
type HostCredentialPushResult struct {
	Instance string `json:"instance"`
	// Action is "added" | "updated" | "skipped" | "failed". "added" and
	// "updated" are the host's own conditional-set actions; "skipped" is the
	// host's classification (a source a pushed key must not shadow, or a scheme
	// that reads no key), or a controller-side skip whose Reason names why (no
	// matching instance on the host, or a local value that is not an API key);
	// "failed" is a per-instance failure (chiefly a refused or stale-revision
	// conditional set) that does not abort the remaining entries.
	Action string `json:"action"`
	Reason string `json:"reason,omitempty"`
}

// The Action values of HostCredentialPushResult.
const (
	HostCredentialPushAdded   = "added"
	HostCredentialPushUpdated = "updated"
	HostCredentialPushSkipped = "skipped"
	HostCredentialPushFailed  = "failed"
)

// HostAttachParams is the evener/host/attach payload (component 06's Connect
// action): the component-03 source ID of one configured remote host to attach.
//
// Host is the host's configured name (component 06 §"Go changes" item 5:
// "Params: HostAttachParams{Host string} — a component-03 source ID"). The
// handler resolves it through the controller's host registry, so an unknown
// name is InvalidParams, and calls the Ensure-backed attach seam. Attaching an
// already-attached host is idempotent and returns its current state.
type HostAttachParams struct {
	Host string `json:"host"`
}

// HostAttachResponse is evener/host/attach's result: the post-attach state of
// one remote host. Attached is true once a live channel exists (always true on
// a successful response). Host is the attached host's configured identity;
// ServerName/ServerVersion are the attach handshake's ServerInfo, so the
// controller can render the row as online without a second probe. The remaining
// fields are the host's post-attach facts (preflight-owned): they are omitted
// when the host's facts seam is not wired.
//
// ServerVersion is the handshake's ServerInfo.Version — the hub's protocol
// server version constant ("0.1.0"), NOT the host's build identity. The host's
// running build is HubVersion, which the preflight facts carry.
type HostAttachResponse struct {
	Attached        bool        `json:"attached"`
	Host            string      `json:"host,omitempty"`
	ServerName      string      `json:"serverName,omitempty"`
	ServerVersion   string      `json:"serverVersion,omitempty"`
	ProtocolVersion string      `json:"protocolVersion,omitempty"`
	HubVersion      string      `json:"hubVersion,omitempty"`
	OS              string      `json:"os,omitempty"`
	Arch            string      `json:"arch,omitempty"`
	Features        *FeatureSet `json:"features,omitempty"`
}

// HostEntry is one host's effective configuration as a mutation carries it
// (component 08 slice 2): the six mutable HostConfig fields under slice 1's
// wire spellings — Address is the schema's `ssh`, EvenerPath the schema's
// `evener_path`, ConfigPath the schema's `config_path` — plus KeyPath, the SSH
// identity file the dial uses. The machine-managed hub.toml stores KeyPath as
// `key_path` (registry spec 08 §6), so a host with one round-trips through a
// rewrite.
//
// Name is carried by the ADD entry, which has no other place to put the new
// host's name. The update entry omits it: a rename is not a thing this wire
// offers, and the update's target is HostUpdateParams.Name. Both paths accept
// the same field set, so the dialog's inputs and a refusal's field name are the
// same strings on both sides.
type HostEntry struct {
	Name       string   `json:"name,omitempty"`
	Address    string   `json:"address"`
	User       string   `json:"user,omitempty"`
	KeyPath    string   `json:"keyPath,omitempty"`
	EvenerPath string   `json:"evenerPath,omitempty"`
	ConfigPath string   `json:"configPath,omitempty"`
	Addr       string   `json:"addr,omitempty"`
	Roots      []string `json:"roots,omitempty"`
}

// HostAddParams is the evener/host/add payload (component 08 slice 1, reshaped
// by slice 2): one host entry. The nested entry is the design record's
// own shape, so the guard fields the pipeline slice adds land on a wire this
// slice already matches instead of reshaping it a second time. The handler
// validates exactly like hub.toml loading, writes the machine-managed hub.toml,
// and refuses a name the live set already holds.
//
// MutationID, when set, is the client's idempotency key (registry spec 08 §1,
// §4): opaque, non-empty, at most 128 bytes. A keyless add skips dedup and is
// non-retryable as a continuation (§5); a keyed replay returns the recorded
// receipt instead of adding a second time.
type HostAddParams struct {
	Entry      HostEntry `json:"entry"`
	MutationID string    `json:"mutationId,omitempty"`
}

// HostUpdateParams is the evener/host/update payload (component 08 slice 2): the
// name of the live entry to edit, and the entry that replaces it. Name
// is the immutable target — it identifies which host to edit, and nothing in the
// request can rename one. Every live host is editable; unknown names are
// InvalidParams.
//
// MutationID, ExpectedGeneration, and ExpectedIncarnationID are the guarded
// mutation's idempotency key and (generation, incarnation id) guard (registry
// spec 08 §4, §11): all three are required together, and a request missing any
// is a validation refusal before the dedup lookup. The pair is checked against
// the target's current pair under the mutation lock; a mismatch on either is
// the typed `stale-entry` refusal committing nothing. A keyed replay returns
// the recorded receipt without re-applying.
type HostUpdateParams struct {
	Name                  string    `json:"name"`
	Entry                 HostEntry `json:"entry"`
	MutationID            string    `json:"mutationId"`
	ExpectedGeneration    uint64    `json:"expectedGeneration"`
	ExpectedIncarnationID string    `json:"expectedIncarnationId"`
}

// HostUpdateResponse is evener/host/update's result: the updated row, which the
// dialog's caller re-reads like every other mutation's result. An edit never
// dials, so the row's live state is whatever the teardown left it.
type HostUpdateResponse struct {
	Host HostRow `json:"host"`
}

// HostRow is one host as evener/host/list and evener/host/status render it
// (component 08 slice 2): the effective entry plus live state. A dialog prefills
// from this row, and list/status remain the one source of truth for what a host
// currently is. Origin names the entry's source and is `hub.toml` on every
// row: the storage decision (registry spec 08 §6/§19) made that file the
// machine-managed store every host lives in, so there is no second origin to
// distinguish (the field is retained for wire compatibility). Attached reports
// a live channel right now; offline rows carry the last-known facts below when
// any were recorded.
// Optional entry fields and facts stay absent — never null — when unknown.
type HostRow struct {
	Name       string   `json:"name"`
	Address    string   `json:"address,omitempty"`
	User       string   `json:"user,omitempty"`
	KeyPath    string   `json:"keyPath,omitempty"`
	EvenerPath string   `json:"evenerPath,omitempty"`
	ConfigPath string   `json:"configPath,omitempty"`
	Addr       string   `json:"addr,omitempty"`
	Roots      []string `json:"roots,omitempty"`
	Origin     string   `json:"origin"`
	// Generation and IncarnationID are the live entry's current
	// (generation, incarnation id) pair — the guarded-mutation identity
	// `update`/`remove` require back as expectedGeneration /
	// expectedIncarnationId (registry spec 08 §1, §11). The UI echoes both
	// values from the list/status row it holds. Tombstone rows carry the
	// removed entry's pair (S11).
	Generation    uint64 `json:"generation"`
	IncarnationID string `json:"incarnationId"`
	Attached      bool   `json:"attached"`
	ServerName    string `json:"serverName,omitempty"`
	ServerVersion string `json:"serverVersion,omitempty"`
	HubVersion    string `json:"hubVersion,omitempty"`
	OS            string `json:"os,omitempty"`
	Arch          string `json:"arch,omitempty"`
	LastAttachErr string `json:"lastAttachError,omitempty"`
	MidAttach     bool   `json:"midAttach"`
	Removed       bool   `json:"removed"`
	// RetainedRows and RowsTruncated are the tombstone row's retained-projection
	// fields (registry spec 08 §11, §15): RetainedRows is present on tombstone
	// rows only — the count of rows the tombstone's bounded projection kept —
	// and RowsTruncated is present as true exactly on tombstone rows whose
	// projection was truncated at the persistence bound (absent everywhere
	// else, per the absent-when-unknown rule). A nil RetainedRows renders as an
	// absent key, so a live row never claims a retained count and a tombstone
	// with a zero-row projection still renders `retainedRows: 0`.
	RetainedRows  *int `json:"retainedRows,omitempty"`
	RowsTruncated bool `json:"rowsTruncated,omitempty"`
	// OpenRemnantID is present exactly on rows — live or tombstone — whose name
	// holds an open remnant: the blocking remnant's id the remnant fence's
	// `remnant-open` refusal also names (registry spec 08 §11).
	OpenRemnantID string `json:"openRemnantId,omitempty"`
	// EscalationAgeSec is present only on rows — live or tombstone — whose name
	// holds an open remnant past the escalation bound: the escalation age the
	// expiry-escalation rule promises, in whole seconds. Absent everywhere else
	// per the absent-when-unknown rule.
	EscalationAgeSec *int64 `json:"escalationAgeSec,omitempty"`
}

// HostListResponse is evener/host/list's result (component 08 slice 1): every
// known host in name-sorted order — the registry's own order; the origin field
// is `hub.toml` on every row. It never dials: attached
// rows read the live channel, offline rows render last-known state.
type HostListResponse struct {
	Hosts []HostRow `json:"hosts"`
}

// HostStatusParams is the evener/host/status payload (component 08 slice 1):
// the component-03 source ID of one known host. Unknown names are
// InvalidParams. Never dials.
type HostStatusParams struct {
	Name string `json:"name"`
}

// HostStatusResponse is evener/host/status's result (component 08 slice 1):
// the host's list row.
type HostStatusResponse struct {
	Host HostRow `json:"host"`
}

// HostRemoveParams is the evener/host/remove payload (component 08 slice 1):
// the component-03 source ID of one live host, any of which is removable here;
// unknown names are InvalidParams. Removing an
// attached host stops its supervisor and drops its channel.
//
// MutationID, ExpectedGeneration, and ExpectedIncarnationID carry the same
// guarded-mutation contract as HostUpdateParams (registry spec 08 §4, §11):
// all three required together, presence validated before the dedup lookup, the
// pair checked under the mutation lock with the typed `stale-entry` refusal on
// a mismatch, and a keyed replay returning the recorded receipt.
type HostRemoveParams struct {
	Name                  string `json:"name"`
	MutationID            string `json:"mutationId"`
	ExpectedGeneration    uint64 `json:"expectedGeneration"`
	ExpectedIncarnationID string `json:"expectedIncarnationId"`
}

// HostRemoveResponse is evener/host/remove's result (component 08 slice 1):
// the removed row, rendered detached. Removed stays removed: the name is gone
// until re-added.
type HostRemoveResponse struct {
	Host HostRow `json:"host"`
}

// HostPlanParams is the evener/host/plan payload (deploy pipeline 08b §10): the
// component-03 source ID of the host to plan against. plan is a mutation — it
// mints the token deploy consumes — so it admits like the settings mutations
// and refuses a remote origin. Unknown names are InvalidParams.
type HostPlanParams struct {
	Name string `json:"name"`
}

// HostPlan is what the human confirms before a deploy (deploy pipeline 08b
// §10): the host, the registry generation the plan was built from, the resolved
// deploy target, the controller's own revision, whether a restart follows the
// push, the digest and capture time of the refreshed preflight facts, the
// host's own hub.toml entry fingerprint, and the probed running state.
// Timestamps are RFC3339; `factsAgeSec` is how old the facts were when the plan
// was built, in whole seconds. `runningProcessStartTime` is absent — never null
// — when the probe carried none.
type HostPlan struct {
	Host                    string `json:"host"`
	Generation              uint64 `json:"generation"`
	TargetPath              string `json:"targetPath"`
	ControllerRevision      string `json:"controllerRevision"`
	RestartFollows          bool   `json:"restartFollows"`
	FactsRevision           string `json:"factsRevision"`
	HubTOMLFingerprint      string `json:"hubTomlFingerprint"`
	FactsCapturedAt         string `json:"factsCapturedAt"`
	FactsAgeSec             int64  `json:"factsAgeSec"`
	RunningVersion          string `json:"runningVersion"`
	RunningHealthy          bool   `json:"runningHealthy"`
	RunningProcessStartTime string `json:"runningProcessStartTime,omitempty"`
}

// HostPlanStaleFacts is the no-token arm's explanation (deploy pipeline 08b
// §10): the human-readable message, whether the host was attached when the plan
// refused, and the machine-readable reason a client branches on.
type HostPlanStaleFacts struct {
	Message  string `json:"message"`
	Attached bool   `json:"attached"`
	Reason   string `json:"reason"`
}

// HostPlanPlanned is evener/host/plan's planned arm (deploy pipeline 08b §10):
// the plan the human confirms plus the single-use token deploy consumes.
type HostPlanPlanned struct {
	Outcome string   `json:"outcome"`
	Plan    HostPlan `json:"plan"`
	Token   string   `json:"token"`
}

// HostPlanNoToken is evener/host/plan's no-token arm (deploy pipeline 08b §10):
// nothing was minted. `terminal` is true exactly on the arms a retry cannot
// clear on its own (controller-dirty, target-unwritable, target-missing-prereq,
// target-unit-findings) and false on the retryable ones (unattached,
// refresh-failed, probe-failed, handler-absent, remnant-open). `remnantId` is
// present — never null — exactly on the remnant-open arm.
type HostPlanNoToken struct {
	Outcome    string             `json:"outcome"`
	StaleFacts HostPlanStaleFacts `json:"staleFacts"`
	Terminal   bool               `json:"terminal"`
	RemnantID  string             `json:"remnantId,omitempty"`
}

// HostPlanResult is evener/host/plan's result union (deploy pipeline 08b §10):
// exactly one arm is set. The embedded pointers make the union marshal as the
// arm it carries — so `plan` and `token` are absent, never null, on the
// no-token arm — while the catalog still carries one named Go struct per arm
// (Methods' ResultArms).
type HostPlanResult struct {
	*HostPlanPlanned
	*HostPlanNoToken
}

// MarshalJSON renders the one arm the union carries. The explicit marshaller is
// what makes the wire honest: both arms carry an `outcome` field, and
// encoding/json drops a field two same-depth embedded structs both declare — so
// without this the discriminator would silently vanish from the bytes a client
// branches on. Exactly one arm must be set; nothing is a programming error no
// response may hide.
func (u HostPlanResult) MarshalJSON() ([]byte, error) {
	switch {
	case u.HostPlanPlanned != nil && u.HostPlanNoToken != nil:
		return nil, errors.New("appwire: evener/host/plan result carries both arms")
	case u.HostPlanPlanned != nil:
		return json.Marshal(u.HostPlanPlanned)
	case u.HostPlanNoToken != nil:
		return json.Marshal(u.HostPlanNoToken)
	}
	return nil, errors.New("appwire: evener/host/plan result carries no arm")
}

// UnmarshalJSON reads the arm the discriminator names, and refuses anything
// else: a result whose `outcome` is neither value is not a plan this protocol
// defines, so a client fails loudly instead of reading a zero-valued arm.
func (u *HostPlanResult) UnmarshalJSON(raw []byte) error {
	var probe struct {
		Outcome string `json:"outcome"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		return err
	}
	switch probe.Outcome {
	case HostPlanOutcomePlanned:
		arm := HostPlanPlanned{}
		if err := json.Unmarshal(raw, &arm); err != nil {
			return err
		}
		u.HostPlanPlanned, u.HostPlanNoToken = &arm, nil
	case HostPlanOutcomeNoToken:
		arm := HostPlanNoToken{}
		if err := json.Unmarshal(raw, &arm); err != nil {
			return err
		}
		u.HostPlanNoToken, u.HostPlanPlanned = &arm, nil
	default:
		return fmt.Errorf("appwire: evener/host/plan result carries outcome %q, want %q or %q",
			probe.Outcome, HostPlanOutcomePlanned, HostPlanOutcomeNoToken)
	}
	return nil
}

// The two outcome values evener/host/plan's arms carry (deploy pipeline 08b
// §10).
const (
	HostPlanOutcomePlanned = "planned"
	HostPlanOutcomeNoToken = "no-token"
)

// The no-token reasons evener/host/plan's no-token arm carries (deploy pipeline
// 08b §10), exactly as the spec spells them.
const (
	HostPlanReasonUnattached          = "unattached"
	HostPlanReasonRefreshFailed       = "refresh-failed"
	HostPlanReasonProbeFailed         = "probe-failed"
	HostPlanReasonHandlerAbsent       = "handler-absent"
	HostPlanReasonRemnantOpen         = "remnant-open"
	HostPlanReasonControllerDirty     = "controller-dirty"
	HostPlanReasonTargetUnwritable    = "target-unwritable"
	HostPlanReasonTargetMissingPrereq = "target-missing-prereq"
	HostPlanReasonTargetUnitFindings  = "target-unit-findings"
)

// FencingEpoch is the epoch wire shape (deploy pipeline 08b §10): the controller
// boot id plus the per-host monotonic op sequence. `evener/host/running` requires
// it — absent or malformed is a typed `probe-failed` refusal, never an unfenced
// write. The name is retained historical wire vocabulary; a `probeEpoch` rename
// is a wire-compat follow-up.
type FencingEpoch struct {
	BootID string `json:"bootId"`
	OpSeq  uint64 `json:"opSeq"`
}

// HostDeployParams is the evener/host/deploy payload (deploy pipeline 08b
// §10): the host name, the single-use confirmation token the plan minted, and
// the client's own operation ID — opaque, non-empty, at most 128 bytes, no
// required structure — that makes the call idempotent.
type HostDeployParams struct {
	Name        string `json:"name"`
	Token       string `json:"token"`
	OperationID string `json:"operationId"`
}

// HostDeployResponse is evener/host/deploy's result (deploy pipeline 08b
// §10): the controller-assigned record id, the caller's operation ID echoed
// back, and the record's state — `pending` on the fresh create that consumed
// the token, the existing record's actual state on a dedup hit.
type HostDeployResponse struct {
	ID                string         `json:"id"`
	ClientOperationID string         `json:"clientOperationId"`
	State             OperationState `json:"state"`
}

// HostRestartParams is the evener/host/restart payload (deploy pipeline 08b
// §10): the host name, the client operation ID, and the intended
// (generation, incarnation id) pair. A lost-response retry repeats the old
// pair and replays the retained record; a reuse of the same operation ID for a
// new incarnation names the new pair and opens fresh; a pair older than the
// registry's current one refuses `stale-entry` (§4).
type HostRestartParams struct {
	Name          string `json:"name"`
	OperationID   string `json:"operationId"`
	Generation    uint64 `json:"generation"`
	IncarnationID string `json:"incarnationId"`
}

// HostRestartResponse is evener/host/restart's result (deploy pipeline 08b
// §10): the fresh create reports `pending`, a dedup hit the existing record's
// state.
type HostRestartResponse struct {
	ID                string         `json:"id"`
	ClientOperationID string         `json:"clientOperationId"`
	State             OperationState `json:"state"`
}

// HostOperationsParams is the evener/host/operations payload (deploy pipeline
// 08b §10): every filter is optional, and empty params list the first
// unfiltered cross-host page. `operationId` matches the client-supplied
// clientOperationId, never the controller-assigned `id`; `generation` selects
// the incarnation after client operation-ID reuse (omitted: the current
// generation) and `incarnationId` narrows that selection to the exact
// incarnation, required alongside `generation` whenever the caller names a
// superseded pair; `id` is the detail filter for the controller-assigned
// record id; `limit` defaults to 50 and caps at 200; `cursor` is the opaque
// continuation the previous page returned.
type HostOperationsParams struct {
	Name          string         `json:"name,omitempty"`
	OperationID   string         `json:"operationId,omitempty"`
	State         OperationState `json:"state,omitempty"`
	Generation    uint64         `json:"generation,omitempty"`
	IncarnationID string         `json:"incarnationId,omitempty"`
	ID            string         `json:"id,omitempty"`
	Limit         int            `json:"limit,omitempty"`
	Cursor        string         `json:"cursor,omitempty"`
}

// HostBoundary is one host's boundary in an operations page's hostBoundaries
// map (deploy pipeline 08b §10): the {generation, incarnationId,
// presenceEpoch} object. The map's value is the union of this object and the
// literal "absent" string (HostBoundaryAbsent), carried as `unknown` in the
// generated client — the generator has no value-union emission, so the
// protocol-shapes test pins both arms' bytes instead.
type HostBoundary struct {
	Generation    uint64 `json:"generation"`
	IncarnationID string `json:"incarnationId"`
	PresenceEpoch uint64 `json:"presenceEpoch"`
}

// HostBoundaryAbsent is the literal string §10's hostBoundaries union encodes
// a host with no records in the query as — never an omission.
const HostBoundaryAbsent = "absent"

// HostOperationsResponse is evener/host/operations' result (deploy pipeline
// 08b §10): the bounded page of records plus the identity the client pages
// under. `generation`/`incarnationId` are present exactly on host-pinned pages
// (the single host named by the request) and absent on unfiltered cross-host
// pages, where `hostBoundaries` is authoritative instead: one boundary per
// every host in the query at cursor creation, the object triple or the literal
// "absent" (HostBoundaryAbsent) for hosts with no records. `nextCursor` is
// present exactly when the page listed at least one record.
type HostOperationsResponse struct {
	Operations     []OperationRecord `json:"operations"`
	Generation     uint64            `json:"generation,omitempty"`
	IncarnationID  string            `json:"incarnationId,omitempty"`
	HostBoundaries map[string]any    `json:"hostBoundaries,omitempty"`
	NextCursor     string            `json:"nextCursor,omitempty"`
}

// OperationState is one durable operation record's lifecycle state (deploy
// pipeline 08b §4, §10). The set is closed; the wire carries the exact string.
type OperationState string

const (
	OperationStatePending          OperationState = "pending"
	OperationStateRunning          OperationState = "running"
	OperationStateComplete         OperationState = "complete"
	OperationStateFailed           OperationState = "failed"
	OperationStateInterrupted      OperationState = "interrupted"
	OperationStateOrphanUnverified OperationState = "orphan-unverified"
)

// OperationProgressEntry is one timestamped progress line on an operation
// record (deploy pipeline 08b §10): RFC3339 UTC plus the line's message,
// bounded per record.
type OperationProgressEntry struct {
	TS      string `json:"ts"`
	Message string `json:"message"`
}

// OperationResult is a record's terminal result (deploy pipeline 08b §10).
type OperationResult struct {
	OK      bool   `json:"ok"`
	Message string `json:"message"`
}

// OperationRecord is the controller-side durable record of one deploy/restart
// (deploy pipeline 08b §4, §10). `incarnationId` is the pinned incarnation the
// record ran against; `result` is present exactly on terminal records;
// `hostRemoved` marks a record whose pinned incarnation a removal tombstoned.
// `compacted: true` is present exactly on a replay the operation store
// answered from a dedup tombstone — the terminal record itself was compacted
// (§4) — and absent on every retained record.
type OperationRecord struct {
	ID                string                   `json:"id"`
	ClientOperationID string                   `json:"clientOperationId"`
	Host              string                   `json:"host"`
	Generation        uint64                   `json:"generation"`
	IncarnationID     string                   `json:"incarnationId"`
	Kind              string                   `json:"kind"`
	State             OperationState           `json:"state"`
	Progress          []OperationProgressEntry `json:"progress,omitempty"`
	Result            *OperationResult         `json:"result,omitempty"`
	CreatedAt         string                   `json:"createdAt"`
	UpdatedAt         string                   `json:"updatedAt"`
	HostRemoved       bool                     `json:"hostRemoved"`
	Compacted         bool                     `json:"compacted,omitempty"`
}

// HostRunningParams is the evener/host/running payload (deploy pipeline 08b
// §10): the calling worker's persisted fencing epoch, which the caller minted
// and persisted before the probe. The field is required on the wire — the
// generated client carries it, so no well-formed call omits it — and an epoch
// absent or malformed is refused with typed `probe-failed` ("no epoch
// presented"), never served as an unfenced write.
type HostRunningParams struct {
	FencingEpoch FencingEpoch `json:"fencingEpoch"`
}

// HostRunningResponse is evener/host/running's result (deploy pipeline 08b
// §10): the serving hub's own build revision (from the same source as the
// controllerBuild plan input), its authoritative health flag, and its process
// start time — present exactly when the serving hub knows it, absent otherwise
// per the absent-when-unknown rule. An unverifiable revision ("dev" or a dirty
// "<sha>-dirty") never proves currency by revision equality.
type HostRunningResponse struct {
	BuildRevision    string `json:"buildRevision"`
	Healthy          bool   `json:"healthy"`
	ProcessStartTime string `json:"processStartTime,omitempty"`
}

// HostNotificationParams is the evener/host/notification payload (component
// 07a): one host-owned config notification re-emitted to the controller's
// browser clients, tagged with the source host. It is the Go-side fan-out
// contract — what the controller emits for a remote host — and nothing more.
//
// Method and Params are the remote hub's own notification, unchanged:
// evener/auth/updated, evener/launch/updated, evener/marketplace/updated,
// evener/plugin/updated, or evener/settings/agentsDoc/changed. Local
// notifications keep their existing, unwrapped methods; the wrapper is
// emitted only for remote hosts, so a store that is not host-scoped never
// sees remote traffic and cannot misapply it.
//
// There is no Go-side consumer of the wrapper: the emitter in this component is
// cmd/evener-hub's remote-admin fan-out, and the client-side unwrapping into
// host-scoped stores is component 07b (branch multi-host-pr07b-host-routing),
// where the wrapped evener/auth/updated refreshes a remote host's own partition
// in cmd/evener-hub/frontend/src/stores/credentials.ts and in the spawn pane's
// catalog invalidation. Until that lands, a wrapped notification reaches browser
// clients that do not yet route it; a local config change is still delivered
// unwrapped, so no existing store changes behavior.
//
// Host is always present and is the only way a store tells two hosts apart.
type HostNotificationParams struct {
	Host   string          `json:"host"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params,omitempty"`
}

// ---------------------------------------------------------------------------
// The mutation-result union (registry spec 08 §11)
// ---------------------------------------------------------------------------

// HostMutationOutcome identifies a host mutation result arm.
type HostMutationOutcome string

// The four discriminator values the mutation-result union carries.
const (
	HostMutationOutcomeCommitted        HostMutationOutcome = "committed"
	HostMutationOutcomeTeardownFailure  HostMutationOutcome = "committed-with-teardown-failure"
	HostMutationOutcomeCollisionDropped HostMutationOutcome = "collision-dropped"
	HostMutationOutcomeAmbiguous        HostMutationOutcome = "ambiguous"
)

// RemovedRow is evener/host/remove's dedicated removed-row arm (registry spec
// 08 §11): `{name, removed: true, retainedRows}` plus the tombstone's retained
// effective HostConfig fields, `attached: false`, `midEnsure: false`, and the
// removed entry's `origin`, `generation`, and `incarnationId` per `list`
// tombstone values. It is NOT a HostRow: the catalog and the regenerated client
// carry it as its own interface, and both it and HostRow carry `incarnationId`,
// so the UI can always construct the guarded-mutation pair.
type RemovedRow struct {
	Name       string   `json:"name"`
	Address    string   `json:"address,omitempty"`
	User       string   `json:"user,omitempty"`
	KeyPath    string   `json:"keyPath,omitempty"`
	EvenerPath string   `json:"evenerPath,omitempty"`
	ConfigPath string   `json:"configPath,omitempty"`
	Addr       string   `json:"addr,omitempty"`
	Roots      []string `json:"roots,omitempty"`
	Origin     string   `json:"origin"`
	// Generation and IncarnationID are the removed entry's pair — what a
	// re-add mints strictly above.
	Generation    uint64 `json:"generation"`
	IncarnationID string `json:"incarnationId"`
	// Removed is always true on this arm; Attached and MidEnsure are always
	// false, per `list`'s tombstone values.
	Removed   bool `json:"removed"`
	Attached  bool `json:"attached"`
	MidEnsure bool `json:"midEnsure"`
	// RetainedRows is the tombstone's retained-projection count; a nil renders
	// as an absent key, exactly as HostRow's tombstone arm does.
	RetainedRows  *int `json:"retainedRows,omitempty"`
	RowsTruncated bool `json:"rowsTruncated,omitempty"`
	// EscalationAgeSec is present only when the row's name holds an escalated
	// open remnant, mirroring HostRow.
	EscalationAgeSec *int64 `json:"escalationAgeSec,omitempty"`
	// OpenRemnantID is present exactly on rows whose name holds an open
	// remnant, mirroring HostRow.
	OpenRemnantID string `json:"openRemnantId,omitempty"`
}

// HostMutationCommitted is the union's clean arm: the mutation landed and every
// planned teardown completed. `host` is a HostRow for add/update and a
// RemovedRow for remove's clean path.
type HostMutationCommitted struct {
	Outcome HostMutationOutcome `json:"outcome"`
	Host    HostRow             `json:"host"`
}

// HostMutationCommittedRemoved is remove's clean arm: the same outcome with the
// dedicated removed-row shape.
type HostMutationCommittedRemoved struct {
	Outcome HostMutationOutcome `json:"outcome"`
	Host    RemovedRow          `json:"host"`
}

// HostMutationTeardownFailure is the union's failure arm: the mutation is
// committed but a post-commit teardown failed. `seam` names the failed rebind
// step and `remnantId` is mandatory — the retry handle
// `evener/host/teardown-retry` resumes. The committed row is always present so
// the UI renders it with a teardown-retry affordance.
type HostMutationTeardownFailure struct {
	Outcome   HostMutationOutcome `json:"outcome"`
	Seam      string              `json:"seam"`
	RemnantID string              `json:"remnantId"`
	Host      HostRow             `json:"host"`
}

// HostMutationTeardownFailureRemoved is remove's teardown-failure arm, carrying
// the removed-row shape for the same reason.
type HostMutationTeardownFailureRemoved struct {
	Outcome   HostMutationOutcome `json:"outcome"`
	Seam      string              `json:"seam"`
	RemnantID string              `json:"remnantId"`
	Host      RemovedRow          `json:"host"`
}

// HostMutationCollisionDropped is the union's dropped arm: the post-rename
// reconcile observed a foreign write that replaced the just-committed staged
// entry, so the file's bytes won. `droppedEntry` is the staged effective config
// "in the same lowerCamel shape as `HostRow`'s config fields — never a literal
// `HostConfig`", and `winningFingerprint` is the winning hub.toml fingerprint.
// `host` is the authoritative row whenever the file still holds the name; when
// the re-read finds the name gone entirely (a hand-edit deletion) the arm
// carries no `host` and sets `removed: true`.
type HostMutationCollisionDropped struct {
	Outcome            HostMutationOutcome `json:"outcome"`
	DroppedEntry       HostRow             `json:"droppedEntry"`
	WinningFingerprint string              `json:"winningFingerprint"`
	Host               *HostRow            `json:"host,omitempty"`
	Removed            bool                `json:"removed,omitempty"`
}

// HostMutationAmbiguous is the union's keyless-ambiguous arm (registry spec 08
// §5): "returned only by a keyless `add` retry that observes its intended row —
// the row may be the caller's committed mutation, a pre-existing identical row,
// or another client's remove/re-add, so the response claims no commit and
// carries no receipt semantics".
type HostMutationAmbiguous struct {
	Outcome     HostMutationOutcome `json:"outcome"`
	ObservedRow HostRow             `json:"observedRow"`
}

// HostMutationResult is the mutation-result union evener/host/add,
// evener/host/update, and evener/host/remove return (registry spec 08 §11):
// exactly one arm is set. The embedded pointers make the union marshal as the
// arm it carries — never as a merged shape with optional-ified fields the
// absent-when-unknown rule cannot distinguish — while the catalog still carries
// one named Go struct per arm (MethodResultArms).
type HostMutationResult struct {
	*HostMutationCommitted
	*HostMutationCommittedRemoved
	*HostMutationTeardownFailure
	*HostMutationTeardownFailureRemoved
	*HostMutationCollisionDropped
	*HostMutationAmbiguous
}

// MarshalJSON renders the one arm the union carries. The arms are disjoint
// shapes with distinct discriminators, but several of them declare `outcome` —
// encoding/json drops a field two same-depth embedded structs both declare, so
// without this marshaller the discriminator would silently vanish from the
// bytes a client branches on. Exactly one arm must be set; nothing is a
// programming error no response may hide.
func (u HostMutationResult) MarshalJSON() ([]byte, error) {
	arms := make([]any, 0, 6)
	for _, arm := range []any{
		u.HostMutationCommitted, u.HostMutationCommittedRemoved,
		u.HostMutationTeardownFailure, u.HostMutationTeardownFailureRemoved,
		u.HostMutationCollisionDropped, u.HostMutationAmbiguous,
	} {
		if arm != nil && !isNilArm(arm) {
			arms = append(arms, arm)
		}
	}
	if len(arms) == 0 {
		return nil, errors.New("appwire: host mutation result carries no arm")
	}
	if len(arms) > 1 {
		return nil, errors.New("appwire: host mutation result carries more than one arm")
	}
	return json.Marshal(arms[0])
}

// isNilArm reports whether a typed arm pointer is nil.
func isNilArm(arm any) bool {
	value := reflect.ValueOf(arm)
	return value.Kind() == reflect.Ptr && value.IsNil()
}

// UnmarshalJSON reads the arm the discriminator names, and refuses anything
// else: a result whose `outcome` is none of the four values is not a mutation
// result this protocol defines, so a client fails loudly instead of reading a
// zero-valued arm.
func (u *HostMutationResult) UnmarshalJSON(raw []byte) error {
	var probe struct {
		Outcome HostMutationOutcome `json:"outcome"`
		Host    json.RawMessage     `json:"host"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		return err
	}
	*u = HostMutationResult{}
	hasHost := len(probe.Host) > 0 && string(probe.Host) != "null"
	// Which row shape the arm carries is read off the host object itself: the
	// dedicated removed row carries `midEnsure` and never `midAttach`, while a
	// live row is the other way round (each is non-optional on its own shape).
	// A remove's arms always carry the removed shape and add/update's never do,
	// so a client that knows its method can narrow on the discriminator alone;
	// this keeps the union decodable on its own too.
	removed := false
	if hasHost {
		var shape map[string]json.RawMessage
		if err := json.Unmarshal(probe.Host, &shape); err == nil {
			_, removed = shape["midEnsure"]
		}
	}
	switch probe.Outcome {
	case HostMutationOutcomeCommitted:
		if removed {
			arm := HostMutationCommittedRemoved{}
			if err := json.Unmarshal(raw, &arm); err != nil {
				return err
			}
			u.HostMutationCommittedRemoved = &arm
			return nil
		}
		arm := HostMutationCommitted{}
		if err := json.Unmarshal(raw, &arm); err != nil {
			return err
		}
		u.HostMutationCommitted = &arm
		return nil
	case HostMutationOutcomeTeardownFailure:
		if removed {
			arm := HostMutationTeardownFailureRemoved{}
			if err := json.Unmarshal(raw, &arm); err != nil {
				return err
			}
			u.HostMutationTeardownFailureRemoved = &arm
			return nil
		}
		arm := HostMutationTeardownFailure{}
		if err := json.Unmarshal(raw, &arm); err != nil {
			return err
		}
		u.HostMutationTeardownFailure = &arm
		return nil
	case HostMutationOutcomeCollisionDropped:
		arm := HostMutationCollisionDropped{}
		if err := json.Unmarshal(raw, &arm); err != nil {
			return err
		}
		u.HostMutationCollisionDropped = &arm
		return nil
	case HostMutationOutcomeAmbiguous:
		arm := HostMutationAmbiguous{}
		if err := json.Unmarshal(raw, &arm); err != nil {
			return err
		}
		u.HostMutationAmbiguous = &arm
		return nil
	default:
		return fmt.Errorf("appwire: host mutation result carries outcome %q, want one of %q, %q, %q, %q",
			probe.Outcome, HostMutationOutcomeCommitted, HostMutationOutcomeTeardownFailure,
			HostMutationOutcomeCollisionDropped, HostMutationOutcomeAmbiguous)
	}
}

// ---------------------------------------------------------------------------
// evener/host/teardown-retry and evener/host/teardown-recover (registry §11)
// ---------------------------------------------------------------------------

// HostTeardownRetryParams is the evener/host/teardown-retry payload (registry
// spec 08 §6/§11): the opaque id of the remnant whose pinned teardown to
// resume. Lookup is by id alone — "lookup never requires a current live entry
// and later mutations cannot strand it".
type HostTeardownRetryParams struct {
	RemnantID string `json:"remnantId"`
}

// The three outcomes the retry's six declared arms cross with the two host
// shapes (registry spec 08 §11: "three outcomes crossed with both host
// shapes").
const (
	HostTeardownOutcomeComplete  = "teardown-complete"
	HostTeardownOutcomeCleared   = "already-cleared"
	HostTeardownOutcomeFailed    = "committed-with-teardown-failure"
	HostTeardownOutcomeRecovered = "recovered-cleared"
)

// The two host shapes the retry and recover arms carry.
const (
	HostKindLive    = "live"
	HostKindRemoved = "removed"
)

// HostTeardownRetryCompleteLive is `teardown-complete` for a remnant whose
// pinned generation is still live: the teardown is done and the row renders.
type HostTeardownRetryCompleteLive struct {
	Outcome          string  `json:"outcome"`
	HostKind         string  `json:"hostKind"`
	Host             HostRow `json:"host"`
	RemnantID        string  `json:"remnantId"`
	EscalationAgeSec *int64  `json:"escalationAgeSec,omitempty"`
}

// HostTeardownRetryCompleteRemoved is `teardown-complete` for a remnant whose
// pinned generation was removed.
type HostTeardownRetryCompleteRemoved struct {
	Outcome          string     `json:"outcome"`
	HostKind         string     `json:"hostKind"`
	Host             RemovedRow `json:"host"`
	RemnantID        string     `json:"remnantId"`
	EscalationAgeSec *int64     `json:"escalationAgeSec,omitempty"`
}

// HostTeardownRetryClearedLive is the `already-cleared` idempotent-replay arm
// for a live generation: "a retry naming an already-cleared remnant returns the
// already-cleared success arm, a receipt-returned no-op".
type HostTeardownRetryClearedLive struct {
	Outcome          string  `json:"outcome"`
	HostKind         string  `json:"hostKind"`
	Host             HostRow `json:"host"`
	RemnantID        string  `json:"remnantId"`
	EscalationAgeSec *int64  `json:"escalationAgeSec,omitempty"`
}

// HostTeardownRetryClearedRemoved is the `already-cleared` replay arm for a
// removed generation.
type HostTeardownRetryClearedRemoved struct {
	Outcome          string     `json:"outcome"`
	HostKind         string     `json:"hostKind"`
	Host             RemovedRow `json:"host"`
	RemnantID        string     `json:"remnantId"`
	EscalationAgeSec *int64     `json:"escalationAgeSec,omitempty"`
}

// HostTeardownRetryFailedLive is the timeout arm for a live generation: "the
// same failure outcome as the mutation-result union, carrying the still-open
// remnant's details for a later retry", with `seam` naming the failed seam.
type HostTeardownRetryFailedLive struct {
	Outcome          string  `json:"outcome"`
	HostKind         string  `json:"hostKind"`
	Host             HostRow `json:"host"`
	RemnantID        string  `json:"remnantId"`
	Seam             string  `json:"seam"`
	EscalationAgeSec *int64  `json:"escalationAgeSec,omitempty"`
}

// HostTeardownRetryFailedRemoved is the timeout arm for a removed generation.
type HostTeardownRetryFailedRemoved struct {
	Outcome          string     `json:"outcome"`
	HostKind         string     `json:"hostKind"`
	Host             RemovedRow `json:"host"`
	RemnantID        string     `json:"remnantId"`
	Seam             string     `json:"seam"`
	EscalationAgeSec *int64     `json:"escalationAgeSec,omitempty"`
}

// HostTeardownRetryResult is evener/host/teardown-retry's six-arm result union
// (registry spec 08 §11): three outcomes crossed with both host shapes, exactly
// one arm set.
type HostTeardownRetryResult struct {
	*HostTeardownRetryCompleteLive
	*HostTeardownRetryCompleteRemoved
	*HostTeardownRetryClearedLive
	*HostTeardownRetryClearedRemoved
	*HostTeardownRetryFailedLive
	*HostTeardownRetryFailedRemoved
}

// MarshalJSON renders the one arm the retry union carries.
func (u HostTeardownRetryResult) MarshalJSON() ([]byte, error) {
	arms := make([]any, 0, 6)
	for _, arm := range []any{
		u.HostTeardownRetryCompleteLive, u.HostTeardownRetryCompleteRemoved,
		u.HostTeardownRetryClearedLive, u.HostTeardownRetryClearedRemoved,
		u.HostTeardownRetryFailedLive, u.HostTeardownRetryFailedRemoved,
	} {
		if arm != nil && !isNilArm(arm) {
			arms = append(arms, arm)
		}
	}
	if len(arms) == 0 {
		return nil, errors.New("appwire: teardown-retry result carries no arm")
	}
	if len(arms) > 1 {
		return nil, errors.New("appwire: teardown-retry result carries more than one arm")
	}
	return json.Marshal(arms[0])
}

// UnmarshalJSON reads the arm the `outcome`/`hostKind` pair names. An unknown
// pair is refused: a client fails loudly rather than reading a zero arm.
func (u *HostTeardownRetryResult) UnmarshalJSON(raw []byte) error {
	var probe struct {
		Outcome  string `json:"outcome"`
		HostKind string `json:"hostKind"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		return err
	}
	*u = HostTeardownRetryResult{}
	decode := func(target any) error { return json.Unmarshal(raw, target) }
	switch probe.Outcome + "/" + probe.HostKind {
	case HostTeardownOutcomeComplete + "/" + HostKindLive:
		arm := HostTeardownRetryCompleteLive{}
		if err := decode(&arm); err != nil {
			return err
		}
		u.HostTeardownRetryCompleteLive = &arm
	case HostTeardownOutcomeComplete + "/" + HostKindRemoved:
		arm := HostTeardownRetryCompleteRemoved{}
		if err := decode(&arm); err != nil {
			return err
		}
		u.HostTeardownRetryCompleteRemoved = &arm
	case HostTeardownOutcomeCleared + "/" + HostKindLive:
		arm := HostTeardownRetryClearedLive{}
		if err := decode(&arm); err != nil {
			return err
		}
		u.HostTeardownRetryClearedLive = &arm
	case HostTeardownOutcomeCleared + "/" + HostKindRemoved:
		arm := HostTeardownRetryClearedRemoved{}
		if err := decode(&arm); err != nil {
			return err
		}
		u.HostTeardownRetryClearedRemoved = &arm
	case HostTeardownOutcomeFailed + "/" + HostKindLive:
		arm := HostTeardownRetryFailedLive{}
		if err := decode(&arm); err != nil {
			return err
		}
		u.HostTeardownRetryFailedLive = &arm
	case HostTeardownOutcomeFailed + "/" + HostKindRemoved:
		arm := HostTeardownRetryFailedRemoved{}
		if err := decode(&arm); err != nil {
			return err
		}
		u.HostTeardownRetryFailedRemoved = &arm
	default:
		return fmt.Errorf("appwire: teardown-retry result carries outcome %q with hostKind %q, want one of the six declared arms",
			probe.Outcome, probe.HostKind)
	}
	return nil
}

// HostTeardownAttestation is the audited recovery attestation (registry spec 08
// §6/§11): the operator's identity, the one statement this build accepts, and
// the instant the absence was observed.
type HostTeardownAttestation struct {
	Operator   string `json:"operator"`
	Statement  string `json:"statement"`
	ObservedAt string `json:"observedAt"`
}

// HostTeardownRecoverParams is the evener/host/teardown-recover payload
// (registry spec 08 §6/§11).
type HostTeardownRecoverParams struct {
	RemnantID   string                  `json:"remnantId"`
	Attestation HostTeardownAttestation `json:"attestation"`
}

// HostTeardownRecoverResult is evener/host/teardown-recover's result (registry
// spec 08 §11): `{outcome: "recovered-cleared", remnantId, clearedName,
// clearedAt, hostKind}` — "the response carries no live row because the recover
// clears a remnant whose teardown never produced one". A retry naming an
// already-recovered id replays the same shape from the persisted record.
type HostTeardownRecoverResult struct {
	Outcome     string `json:"outcome"`
	RemnantID   string `json:"remnantId"`
	ClearedName string `json:"clearedName"`
	ClearedAt   string `json:"clearedAt"`
	HostKind    string `json:"hostKind"`
}
