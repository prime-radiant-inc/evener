package hub

// The deploy pipeline's live end-to-end check against a disposable container
// host (the accepted D-7 plan): an alpine+sshd container stands in for a fresh
// host, and the pipeline's own wire path — evener/host/add → evener/host/attach
// (which provisions the bare host) → evener/host/plan → evener/host/deploy →
// evener/host/restart → evener/host/operations — runs against it over the
// product's real ssh invocations, with no product change.
//
// It is the operation-model half of the deploy slice, on top of the sibling
// TestHostDeployNoEvenerE2E (which pins the deploy path itself: a bare host is
// provisioned and attaches). This check pins the pipeline kept after comp08
// passes 1–3: the confirmation token's single use, the durable operation
// records' pending→running→terminal lifecycle and identity pair, the pipeline's
// one-operation-at-a-time serialization (each listed operation reached its
// terminal write before the next was created; the typed host-busy refusals a
// genuinely concurrent attempt would meet are covered by the handler and gate
// unit suites, which can drive them deterministically — this check does not
// race for them), and the restart that follows its own operation. It also pins
// the simplification: the container carries no fencing helper, no claim
// primitive, and no fencing state, and the happy path emits no
// fencing-flavoured refusal or prose.
//
// Gates: like the sibling deploy check this is an explicitly opted-in WRITE, so
// it needs EVENER_SSH_E2E=1 and EVENER_SSH_E2E_DEPLOY=1 plus a host source.
// EVENER_SSH_E2E_CONTAINER=1 brings up its own disposable alpine+sshd container
// (Docker unavailable ⇒ a clean skip; the image is cached at most once, under
// hostPipelineImage). EVENER_SSH_E2E_HOST=<ssh destination> instead targets an
// existing disposable host — the paradise-park override path — and skips the
// container entirely. Default `go test ./...` and `make test` touch neither
// docker nor ssh.
//
// The container: a per-run ed25519 key authorized in it, the container's sshd
// published on a per-run loopback port, and a per-run ssh_config naming the
// alias. The product's ssh is redirected to the container by a PATH shim
// (installHostPipelineSSHShim) rather than by a config in the controller's
// isolated HOME: OpenSSH resolves its user config from the passwd-database
// home, NOT $HOME (verified — `HOME=<dir> ssh -G` ignores <dir>/.ssh/config), so
// an isolated-HOME config is never consulted. The shim hands the per-run config
// to exactly the alias's invocations and passes every other invocation through
// unchanged, so both the product's ssh and this harness's ssh use one
// mechanism without touching sshconn's argv contract.
//
// What it writes on the host: the same run-target discipline as the sibling
// check — a per-run directory <HOME>/evener-pipeline-e2e-<runid> with
// bin/evener and state/, a private hub.toml, and a private host-hub loopback
// port — removed when the case finishes; the host's own install
// (~/.local/bin/evener) is hashed before and after and must come out unchanged.
// The container itself is force-removed in cleanup.

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/sshconn"
	"primeradiant.com/evener/execsupport/shellquote"
	"primeradiant.com/evener/hubapi"
	"primeradiant.com/evener/internal/e2ecap"
	"primeradiant.com/evener/test/e2e/fakellm"
)

const (
	// hostPipelineDirPrefix names the test-owned directory under the host's HOME.
	// It carries a per-run token (hostDeployRunID) and is removed when the case
	// finishes, so it is always one this run created rather than one already
	// there.
	hostPipelineDirPrefix = "evener-pipeline-e2e"

	// hostPipelineImage is the one cached image this check builds: alpine plus
	// the host tools the product's ssh paths need. Remove it with
	// `docker rmi evener-e2e-ssh:local`.
	hostPipelineImage = "evener-e2e-ssh:local"

	// hostPipelineReadyTimeout bounds the wait for the container's sshd to
	// accept the per-run key.
	hostPipelineReadyTimeout = 90 * time.Second

	// hostPipelineOperationTimeout bounds one operation's pending→terminal
	// life. It is a tripwire, not the mechanism: the poll returns as soon as
	// the record is terminal. The deploy pushes an artifact and the restart
	// replaces the host's hub process, so the bound is generous.
	hostPipelineOperationTimeout = 8 * time.Minute

	// hostPipelinePollWait is how often the record is re-read while an
	// operation runs.
	hostPipelinePollWait = 200 * time.Millisecond
)

// hostPipelineContainerGate returns the t.Skip reason when the live
// deploy-pipeline check may not run, and "" when it may. The write gate is
// checked before the master gate, matching the sibling checks' order
// convention, so an un-opted-in run's skip line names the contract plainly:
// this check WRITES to the host, and it needs a host source as well as the two
// gates.
func hostPipelineContainerGate(short bool, deploy, master, container, dest string) string {
	if short {
		return "live deploy-pipeline test: plans, deploys, restarts, and reads operation records on a disposable host (its own alpine+sshd container or EVENER_SSH_E2E_HOST)"
	}
	if deploy != "1" {
		return "set EVENER_SSH_E2E_DEPLOY=1 (with EVENER_SSH_E2E=1 and either EVENER_SSH_E2E_CONTAINER=1 or EVENER_SSH_E2E_HOST) to run the live deploy-pipeline test; this check WRITES to the host — it creates its own run-target directory there and starts a hub from it"
	}
	if master != "1" {
		return "set EVENER_SSH_E2E=1 and either EVENER_SSH_E2E_CONTAINER=1 or EVENER_SSH_E2E_HOST to run the live deploy-pipeline test"
	}
	if dest == "" && container != "1" {
		return "set EVENER_SSH_E2E_CONTAINER=1 to run the live deploy-pipeline test against a disposable alpine+sshd container, or set EVENER_SSH_E2E_HOST to target an existing disposable host instead"
	}
	return ""
}

func TestHostDeployPipelineContainerE2E(t *testing.T) {
	if reason := hostPipelineContainerGate(testing.Short(), os.Getenv("EVENER_SSH_E2E_DEPLOY"), os.Getenv("EVENER_SSH_E2E"), os.Getenv("EVENER_SSH_E2E_CONTAINER"), os.Getenv("EVENER_SSH_E2E_HOST")); reason != "" {
		t.Skip(reason)
	}
	e2ecap.RequireLoopbackBind(t)
	e2ecap.RequireProcessInspect(t)

	repoRoot, err := filepath.Abs("../..")
	if err != nil {
		t.Fatalf("abs repo root: %v", err)
	}

	// EVENER_SSH_E2E_HOST wins over EVENER_SSH_E2E_CONTAINER: the override path
	// is how a real disposable host is targeted instead of the container.
	dest := os.Getenv("EVENER_SSH_E2E_HOST")
	user := os.Getenv("EVENER_SSH_E2E_USER")
	if dest == "" {
		container := startHostPipelineContainer(t)
		installHostPipelineSSHShim(t, filepath.Join(container.dir, "shim"), container.sshConfigPath, container.alias)
		dest = container.alias
		user = "" // the per-run ssh_config names the container's root user
		t.Logf("container host: name=%s id=%s alias=%s loopback port=%d", container.name, container.id, container.alias, container.port)
	}

	host := newHostSSH(t, dest, user)
	home := host.output(`printf '%s' "$HOME"`)
	if !strings.HasPrefix(home, "/") {
		t.Fatalf("host %s HOME = %q, want an absolute path (the deploy target has to be an absolute path the host hub can be launched from)", host.target, home)
	}
	goos, goarch := hostTarget(t, host)

	hubBin, version := deployE2EHubBinary(t, repoRoot)
	staged := stageDeployArtifact(t, repoRoot, goos, goarch)
	t.Logf("controller build: %s (version %q); host target %s/%s", hubBin, version, goos, goarch)

	provider, err := fakellm.New()
	if err != nil {
		t.Fatalf("start fake provider: %v", err)
	}
	t.Cleanup(provider.Close)

	runID := hostDeployRunID()
	hostName := "e2e-pipeline-" + runID
	dirName := hostPipelineDirPrefix + "-" + runID
	hostDir := home + "/" + dirName
	runTarget := hostDir + "/bin/evener"
	// The host hub's private loopback address is allocated on the host itself
	// (bind 127.0.0.1:0 and release, or the probe-verified fallback), before the
	// private hub.toml and the entry carry it, so a stale or concurrent host
	// hub can never be addressed by this run.
	addr := hostPipelineHostAddr(t, host)

	// The same precondition discipline as the sibling deploy check: the test
	// creates the host directory itself and refuses an existing path before
	// anything is created. The cleanup is registered immediately after that
	// check and BEFORE anything is created, so a failure in the creation steps
	// or the run-target check cannot leave the test-owned directory — or a hub
	// started from it — behind on the host.
	if _, err := host.run("test -e " + shellquote.RemoteWord(hostDir)); err == nil {
		t.Fatalf("host %s already has %s; the pipeline check creates and removes this directory itself, so it must not adopt an existing one", host.target, hostDir)
	}
	installPath := home + "/.local/bin/evener"
	installHash := host.sha256IfFile(installPath)
	t.Cleanup(func() {
		stopHostListener(t, host, addr, hostDir+"/"+hostDeployToml)
		if err := host.tryRun("rm -rf " + shellquote.RemoteWord(hostDir)); err != nil {
			t.Errorf("remove the test-owned directory %s on host %s: %v", hostDir, host.target, err)
		}
		if installHash == "" {
			if got := host.sha256IfFile(installPath); got != "" {
				t.Errorf("the pipeline created %s on host %s (sha256 %s); it must write only to the test-owned %s", installPath, host.target, got, runTarget)
			}
			return
		}
		if got := host.sha256IfFile(installPath); got != installHash {
			t.Errorf("the host's own install %s changed during the pipeline case (sha256 %s -> %s); it must write only to the test-owned %s", installPath, installHash, got, runTarget)
		}
	})

	host.mustRun("mkdir -p " + shellquote.RemoteWord(hostDir+"/bin") + " " + shellquote.RemoteWord(hostDir+"/state"))
	if _, err := host.run("test -e " + shellquote.RemoteWord(runTarget)); err == nil {
		t.Fatalf("host %s already has %s at the start of the case; the pipeline case needs a run target with no evener", host.target, runTarget)
	}
	host.writeFile(hostDir+"/"+hostDeployToml, []byte(fmt.Sprintf("addr = %q\nhub_state_root = %q\nplugin_auto_upgrade = false\n", addr, hostDir+"/state")))

	stack := startDeployStack(t, provider, hubBin, "-deploy-binary", staged)
	ctx, cancel := context.WithTimeout(context.Background(), hostDeployAttachTimeout+2*hostPipelineOperationTimeout)
	defer cancel()
	client := stack.dialRPC(ctx, t)

	// evener/host/add answers the mutation-result union; a clean keyless add is
	// the committed arm carrying the registered row.
	addResult, err := clientRequest[appwire.HostMutationResult](ctx, client, appwire.MethodEvenerHostAdd, appwire.HostAddParams{
		Entry: appwire.HostEntry{
			Name:       hostName,
			Address:    dest,
			User:       user,
			EvenerPath: runTarget,
			ConfigPath: hostDir + "/" + hostDeployToml,
			Addr:       addr,
		},
	})
	if err != nil {
		t.Fatalf("step evener/host/add (ssh destination %q, run target %q): %v", dest, runTarget, err)
	}
	if addResult.HostMutationCommitted == nil {
		t.Fatalf("step evener/host/add: the mutation-result union carried no committed arm: %+v", addResult)
	}
	row := addResult.HostMutationCommitted.Host
	if row.Name != hostName || row.Origin != "hub.toml" {
		t.Fatalf("step evener/host/add: committed row name=%q origin=%q, want %q with origin %q (every host lives in the machine-managed hub.toml)", row.Name, row.Origin, hostName, "hub.toml")
	}

	// Step 1: the attach that provisions the bare host (the deploy path) and
	// leaves it attached. This is the sibling check's provisioning path; every
	// later step runs against the attached channel.
	attached := awaitHostAttachedWithin(ctx, t, client, hostName, hostDeployAttachTimeout)
	t.Logf("attached %s: os=%s arch=%s hubVersion=%s evenerPath=%s", hostName, attached.OS, attached.Arch, attached.HubVersion, attached.EvenerPath)
	if attached.OS != goos || attached.Arch != goarch {
		t.Fatalf("attached row for %q reports os/arch %s/%s, want %s/%s", hostName, attached.OS, attached.Arch, goos, goarch)
	}
	if attached.HubVersion != version {
		t.Fatalf("attached row for %q reports hubVersion %q, want the controller's %q", hostName, attached.HubVersion, version)
	}
	if p := attached.EvenerPath; filepath.Base(p) != "evener" || !strings.Contains(p, dirName) {
		t.Fatalf("attached row for %q reports evenerPath %q, want the test's run target under %q (a path named evener)", hostName, p, dirName)
	}

	// A host-side proof read before the pipeline runs: the container's own
	// loopback serves the host hub's health with the stamped build and a process
	// start time, which the restart below must replace.
	beforeHealth := hostPipelineHostHealth(t, host, addr)
	if beforeHealth.Version != version {
		t.Fatalf("the host hub at %s reports health version %q, want the stamped %q", addr, beforeHealth.Version, version)
	}
	if beforeHealth.StartedAt.IsZero() {
		t.Fatalf("the host hub at %s reports no started_at; the restart's process-replacement proof reads it", addr)
	}

	// Step 2: evener/host/plan mints the confirmation token deploy consumes.
	planned := hostPipelinePlan(ctx, t, client, hostName, runTarget, version, attached)

	// Step 3: evener/host/deploy consumes the token and starts the operation.
	deployClientID := "e2e-deploy-" + runID
	deploy, err := clientRequest[appwire.HostDeployResponse](ctx, client, appwire.MethodEvenerHostDeploy, appwire.HostDeployParams{
		Name:        hostName,
		Token:       planned.Token,
		OperationID: deployClientID,
	})
	if err != nil {
		t.Fatalf("step evener/host/deploy: %v", err)
	}
	if deploy.State != appwire.OperationStatePending {
		t.Fatalf("evener/host/deploy returned state %q, want %q: the fresh create that consumed the token answers pending, and the worker owns every later transition", deploy.State, appwire.OperationStatePending)
	}
	if deploy.ID == "" || deploy.ClientOperationID != deployClientID {
		t.Fatalf("evener/host/deploy answered id=%q clientOperationId=%q, want a controller id and the caller's %q", deploy.ID, deploy.ClientOperationID, deployClientID)
	}
	deployRecord, deployStates := awaitHostPipelineTerminal(ctx, t, client, hostName, deploy.ID)
	assertHostPipelineLifecycle(t, deployRecord, deployStates)
	assertHostPipelineRecord(t, deployRecord, "deploy", hostName, deployClientID, attached.Generation, attached.IncarnationID)
	assertHostPipelineProgress(t, deployRecord, "operation for host", "started", "pushing the controller's build", "the controller's build is installed and verified")
	t.Logf("deploy operation %s: states %v, terminal %s result=%+v", deployRecord.ID, deployStates, deployRecord.State, deployRecord.Result)

	// Step 4: the confirmation token is single-use. Presenting the consumed
	// token again with a fresh client operation ID (so dedup cannot answer it)
	// must refuse the typed token-missing arm.
	tokenRefusal := assertHostPipelineTokenConsumed(ctx, t, client, hostName, planned.Token, "e2e-replay-"+runID)

	// Step 5: evener/host/restart replaces the host's process on the identity
	// pair the deploy record carries.
	restartClientID := "e2e-restart-" + runID
	restart, err := clientRequest[appwire.HostRestartResponse](ctx, client, appwire.MethodEvenerHostRestart, appwire.HostRestartParams{
		Name:          hostName,
		OperationID:   restartClientID,
		Generation:    deployRecord.Generation,
		IncarnationID: deployRecord.IncarnationID,
	})
	if err != nil {
		t.Fatalf("step evener/host/restart: %v", err)
	}
	if restart.State != appwire.OperationStatePending {
		t.Fatalf("evener/host/restart returned state %q, want %q (the fresh create answers pending)", restart.State, appwire.OperationStatePending)
	}
	if restart.ID == "" || restart.ID == deploy.ID || restart.ClientOperationID != restartClientID {
		t.Fatalf("evener/host/restart answered id=%q clientOperationId=%q, want a fresh controller id and the caller's %q", restart.ID, restart.ClientOperationID, restartClientID)
	}
	restartRecord, restartStates := awaitHostPipelineTerminal(ctx, t, client, hostName, restart.ID)
	assertHostPipelineLifecycle(t, restartRecord, restartStates)
	assertHostPipelineRecord(t, restartRecord, "restart", hostName, restartClientID, attached.Generation, attached.IncarnationID)
	assertHostPipelineProgress(t, restartRecord, "operation for host", "started", "restarting the host", "restart verified healthy", "reattaching the host under the held host gate", "post-operation refresh verified")
	t.Logf("restart operation %s: states %v, terminal %s result=%+v", restartRecord.ID, restartStates, restartRecord.State, restartRecord.Result)

	// The host-side proof that the restart took: the same endpoint serves the
	// same stamped build from a provably different process.
	afterHealth := hostPipelineHostHealth(t, host, addr)
	if afterHealth.Version != version {
		t.Fatalf("after the restart the host hub at %s reports version %q, want the stamped %q", addr, afterHealth.Version, version)
	}
	if !afterHealth.StartedAt.After(beforeHealth.StartedAt) {
		t.Fatalf("the host hub at %s reports process start time %s after the restart, want a later one than %s (the restart did not replace the process)",
			addr, afterHealth.StartedAt.Format(time.RFC3339Nano), beforeHealth.StartedAt.Format(time.RFC3339Nano))
	}
	lc := host.launchCheck(t, runTarget)
	if lc.Version != version || lc.Protocol != appwire.ProtocolVersion {
		t.Fatalf("the deployed binary at %s reports launch-check version %q protocol %q, want %q and %q", runTarget, lc.Version, lc.Protocol, version, appwire.ProtocolVersion)
	}
	rowBack, err := clientRequest[appwire.HostStatusResponse](ctx, client, appwire.MethodEvenerHostStatus, appwire.HostStatusParams{Name: hostName})
	if err != nil {
		t.Fatalf("step evener/host/status after the restart: %v", err)
	}
	if !rowBack.Host.Attached || rowBack.Host.HubVersion != version {
		t.Fatalf("after the restart the row reports attached=%t hubVersion=%q, want attached with %q", rowBack.Host.Attached, rowBack.Host.HubVersion, version)
	}
	t.Logf("restart verified from the host side: health version=%s startedAt=%s -> %s", version,
		beforeHealth.StartedAt.Format(time.RFC3339Nano), afterHealth.StartedAt.Format(time.RFC3339Nano))

	// Step 6: evener/host/operations lists the terminal records for the host,
	// with the host-pinned identity pair.
	page, err := clientRequest[appwire.HostOperationsResponse](ctx, client, appwire.MethodEvenerHostOperations, appwire.HostOperationsParams{Name: hostName, Limit: 50})
	if err != nil {
		t.Fatalf("step evener/host/operations: %v", err)
	}
	if page.Generation != attached.Generation || page.IncarnationID != attached.IncarnationID {
		t.Fatalf("the host-pinned page identity pair = (%d, %q), want the row's (%d, %q)", page.Generation, page.IncarnationID, attached.Generation, attached.IncarnationID)
	}
	if len(page.HostBoundaries) != 0 {
		t.Fatalf("a host-pinned page carries no hostBoundaries map, got %v", page.HostBoundaries)
	}
	if len(page.Operations) == 0 {
		t.Fatalf("the operations page listed no records; at least the deploy and restart records were expected")
	}
	var prose []string
	listed := map[string]appwire.OperationRecord{}
	for i, record := range page.Operations {
		if !hostPipelineStateTerminal(record.State) {
			t.Fatalf("record %s (%s) is %s, want every listed record terminal after both operations completed", record.ID, record.Kind, record.State)
		}
		if record.Generation != attached.Generation || record.IncarnationID != attached.IncarnationID {
			t.Fatalf("record %s carries pair (%d, %q), want the host's (%d, %q)", record.ID, record.Generation, record.IncarnationID, attached.Generation, attached.IncarnationID)
		}
		if i > 0 && page.Operations[i-1].ID >= record.ID {
			t.Fatalf("records are not ascending by controller id: %s then %s", page.Operations[i-1].ID, record.ID)
		}
		listed[record.ID] = record
		prose = append(prose, hostPipelineRecordProse(record))
		t.Logf("operations page: id=%s kind=%s clientOperationId=%s state=%s result=%+v", record.ID, record.Kind, record.ClientOperationID, record.State, record.Result)
	}
	for _, want := range []struct {
		id   string
		kind string
	}{{deploy.ID, "deploy"}, {restart.ID, "restart"}} {
		record, ok := listed[want.id]
		if !ok {
			t.Fatalf("the operations page does not carry the %s record %s", want.kind, want.id)
		}
		if record.Kind != want.kind || record.State != appwire.OperationStateComplete {
			t.Fatalf("listed record %s = kind %q state %q, want the complete %s operation", want.id, record.Kind, record.State, want.kind)
		}
	}

	// The store mints a continuation whenever a page listed at least one record
	// (hostops/cursor.go's OperationsPage.NextCursor contract: "present exactly
	// when the page listed at least one record"), so a cursor here is expected
	// even though the page exhausted this host's records — the first live run
	// confirmed one is returned. The assertion deliberately does not hinge on
	// that: it follows the chain when a cursor is present and requires the
	// continuation to be the empty tail, which is what proves the first page
	// listed every record this run created.
	if page.NextCursor != "" {
		tail, err := clientRequest[appwire.HostOperationsResponse](ctx, client, appwire.MethodEvenerHostOperations, appwire.HostOperationsParams{Name: hostName, Limit: 50, Cursor: page.NextCursor})
		if err != nil {
			t.Fatalf("step evener/host/operations (continuation): %v", err)
		}
		if len(tail.Operations) != 0 {
			t.Fatalf("the continuation page listed %v; the first page was expected to hold every record this run created", hostPipelineRecordIDs(tail.Operations))
		}
		if tail.NextCursor != "" {
			t.Fatalf("the empty continuation page carries a further cursor (%q); the mint rule gives no cursor to a page that listed no record", tail.NextCursor)
		}
		t.Log("operations page: followed the returned cursor to an empty tail (no records, no further cursor)")
	} else {
		t.Logf("operations page: no continuation cursor was returned; the page listed %d records", len(page.Operations))
	}

	// Deterministic evidence of the pipeline's one-operation-at-a-time
	// serialization: in controller-id (allocation) order, every record reached
	// its terminal write before the next record was created. Provoking the typed
	// host-busy refusal would need a genuinely concurrent attempt whose timing
	// this check cannot make deterministic, so that refusal is left to the
	// handler and gate unit suites; this ordering is what the live run can prove
	// without racing.
	for i := 1; i < len(page.Operations); i++ {
		previous, current := page.Operations[i-1], page.Operations[i]
		previousUpdated, err := time.Parse(time.RFC3339, previous.UpdatedAt)
		if err != nil {
			t.Fatalf("record %s updatedAt %q does not parse: %v", previous.ID, previous.UpdatedAt, err)
		}
		currentCreated, err := time.Parse(time.RFC3339, current.CreatedAt)
		if err != nil {
			t.Fatalf("record %s createdAt %q does not parse: %v", current.ID, current.CreatedAt, err)
		}
		if currentCreated.Before(previousUpdated) {
			t.Fatalf("operation %s was created at %s, before operation %s reached its terminal write at %s; the pipeline must run one operation at a time",
				current.ID, current.CreatedAt, previous.ID, previous.UpdatedAt)
		}
	}

	// Step 7: the simplification's live proof. The container has no
	// evener-fence helper, no claim primitive, and no fencing state, and nothing
	// on the happy path refused for a fencing reason or carries the withdrawn
	// crash-fencing vocabulary.
	assertHostPipelineNoFencingHelpers(t, host, hostDir)
	assertHostPipelineNoFencingProse(t, append(prose, tokenRefusal)...)
}

// hostPipelinePlan drives evener/host/plan and pins the planned arm's shape: the
// token is minted, the plan names the run target and the controller's own
// stamped revision, and — because the attach just provisioned the host with
// that same build and it is healthy — no restart follows the push.
func hostPipelinePlan(ctx context.Context, t *testing.T, client *appwire.Client, hostName, runTarget, version string, attached appwire.HostRow) appwire.HostPlanPlanned {
	t.Helper()
	result, err := clientRequest[appwire.HostPlanResult](ctx, client, appwire.MethodEvenerHostPlan, appwire.HostPlanParams{Name: hostName})
	if err != nil {
		t.Fatalf("step evener/host/plan (host %q): %v", hostName, err)
	}
	if result.HostPlanPlanned == nil {
		if no := result.HostPlanNoToken; no != nil {
			t.Fatalf("step evener/host/plan: refused with the no-token arm: reason=%s terminal=%t attached=%t message=%s",
				no.StaleFacts.Reason, no.Terminal, no.StaleFacts.Attached, no.StaleFacts.Message)
		}
		t.Fatalf("step evener/host/plan: the result carried neither arm")
	}
	planned := *result.HostPlanPlanned
	if planned.Token == "" {
		t.Fatal("step evener/host/plan: the planned arm carries no confirmation token")
	}
	plan := planned.Plan
	if plan.Host != hostName {
		t.Fatalf("plan host = %q, want %q", plan.Host, hostName)
	}
	if plan.TargetPath != runTarget {
		t.Fatalf("plan targetPath = %q, want the run target %q", plan.TargetPath, runTarget)
	}
	if plan.ControllerRevision != version {
		t.Fatalf("plan controllerRevision = %q, want the controller's stamped %q", plan.ControllerRevision, version)
	}
	if plan.Generation != attached.Generation {
		t.Fatalf("plan generation = %d, want the row's %d", plan.Generation, attached.Generation)
	}
	if plan.RunningVersion != version {
		t.Fatalf("plan runningVersion = %q, want the host's attached build %q", plan.RunningVersion, version)
	}
	if !plan.RunningHealthy {
		t.Fatalf("plan runningHealthy = false, want true: the attach just provisioned this host with the controller's build")
	}
	if plan.RestartFollows {
		t.Fatalf("plan restartFollows = true for a host already serving this controller's healthy build %q; a restart must not follow this push", version)
	}
	t.Logf("plan: host=%s generation=%d target=%s controllerRevision=%s restartFollows=%t runningVersion=%s healthy=%t factsAgeSec=%d token=%s", plan.Host, plan.Generation, plan.TargetPath, plan.ControllerRevision, plan.RestartFollows, plan.RunningVersion, plan.RunningHealthy, plan.FactsAgeSec, planned.Token)
	return planned
}

// awaitHostPipelineTerminal polls one operation record through
// evener/host/operations until it is terminal, logging each observed state
// transition, and returns the terminal record plus the observed state sequence.
func awaitHostPipelineTerminal(ctx context.Context, t *testing.T, client *appwire.Client, hostName, recordID string) (appwire.OperationRecord, []appwire.OperationState) {
	t.Helper()
	deadline := time.Now().Add(hostPipelineOperationTimeout)
	var states []appwire.OperationState
	for {
		page, err := clientRequest[appwire.HostOperationsResponse](ctx, client, appwire.MethodEvenerHostOperations, appwire.HostOperationsParams{Name: hostName, ID: recordID})
		if err != nil {
			t.Fatalf("step evener/host/operations (id %s): %v", recordID, err)
		}
		record, ok := hostPipelineFindRecord(page.Operations, recordID)
		if !ok {
			t.Fatalf("the operations page for host %q does not carry record %q (ids %v); a record is durable from its create write, so the detail filter must find it", hostName, recordID, hostPipelineRecordIDs(page.Operations))
		}
		if len(states) == 0 || states[len(states)-1] != record.State {
			states = append(states, record.State)
		}
		if hostPipelineStateTerminal(record.State) {
			return record, states
		}
		if time.Now().After(deadline) {
			t.Fatalf("operation %s did not reach a terminal state within %s; last state %s result=%+v progress:\n%s",
				recordID, hostPipelineOperationTimeout, record.State, record.Result, hostPipelineProgressText(record))
		}
		select {
		case <-ctx.Done():
			t.Fatalf("the run's context ended while waiting for operation %s: %v", recordID, ctx.Err())
		case <-time.After(hostPipelinePollWait):
		}
	}
}

// assertHostPipelineLifecycle pins the record's lifecycle from what was
// observed: the fresh create answered pending (asserted by the caller), every
// observed step advances pending → running → terminal, and the last is the
// terminal state the caller's record assertions then hold to complete. A poll
// can legitimately miss running, so the running transition is pinned separately
// by the "started" progress line every worker writes on its transition.
func assertHostPipelineLifecycle(t *testing.T, record appwire.OperationRecord, observed []appwire.OperationState) {
	t.Helper()
	states := make([]appwire.OperationState, 0, len(observed)+1)
	for _, state := range append([]appwire.OperationState{appwire.OperationStatePending}, observed...) {
		if len(states) == 0 || states[len(states)-1] != state {
			states = append(states, state)
		}
	}
	for i := 1; i < len(states); i++ {
		if !hostPipelineStateAdvances(states[i-1], states[i]) {
			t.Fatalf("record %s went %s -> %s; the lifecycle is pending -> running -> terminal", record.ID, states[i-1], states[i])
		}
	}
	if last := states[len(states)-1]; last != record.State {
		t.Fatalf("record %s was last observed %s but read terminal as %s", record.ID, last, record.State)
	}
}

// hostPipelineStateAdvances reports whether to is a legal successor of from.
func hostPipelineStateAdvances(from, to appwire.OperationState) bool {
	switch from {
	case appwire.OperationStatePending:
		return to == appwire.OperationStateRunning || hostPipelineStateTerminal(to)
	case appwire.OperationStateRunning:
		return hostPipelineStateTerminal(to)
	}
	return false
}

// hostPipelineStateTerminal reports whether a record state is terminal.
func hostPipelineStateTerminal(state appwire.OperationState) bool {
	switch state {
	case appwire.OperationStateComplete, appwire.OperationStateFailed,
		appwire.OperationStateInterrupted, appwire.OperationStateOrphanUnverified:
		return true
	}
	return false
}

// assertHostPipelineRecord pins one terminal record's identity: kind, host, the
// caller's client operation ID, the durable identity pair, and a complete
// result. A record replaying a dedup tombstone is refused here too — a live
// case must read the retained record.
func assertHostPipelineRecord(t *testing.T, record appwire.OperationRecord, kind, hostName, clientOpID string, generation uint64, incarnation string) {
	t.Helper()
	if record.Kind != kind {
		t.Fatalf("record %s kind = %q, want %q", record.ID, record.Kind, kind)
	}
	if record.Host != hostName {
		t.Fatalf("record %s host = %q, want %q", record.ID, record.Host, hostName)
	}
	if record.ClientOperationID != clientOpID {
		t.Fatalf("record %s clientOperationId = %q, want the caller's %q", record.ID, record.ClientOperationID, clientOpID)
	}
	if record.Generation != generation || record.IncarnationID != incarnation {
		t.Fatalf("record %s pair = (%d, %q), want the host's (%d, %q)", record.ID, record.Generation, record.IncarnationID, generation, incarnation)
	}
	if record.Compacted {
		t.Fatalf("record %s is a compacted dedup-tombstone replay, want the retained record", record.ID)
	}
	if record.HostRemoved {
		t.Fatalf("record %s is marked host-removed; this host was never removed", record.ID)
	}
	if record.State != appwire.OperationStateComplete {
		t.Fatalf("record %s reached %s, want complete; result=%+v progress:\n%s", record.ID, record.State, record.Result, hostPipelineProgressText(record))
	}
	if record.Result == nil || !record.Result.OK {
		t.Fatalf("record %s is complete but carries no ok result: %+v", record.ID, record.Result)
	}
}

// assertHostPipelineProgress fails unless every wanted line appears in the
// record's progress log.
func assertHostPipelineProgress(t *testing.T, record appwire.OperationRecord, want ...string) {
	t.Helper()
	text := hostPipelineProgressText(record)
	for _, line := range want {
		if !strings.Contains(text, line) {
			t.Fatalf("record %s progress does not carry %q:\n%s", record.ID, line, text)
		}
	}
}

// assertHostPipelineTokenConsumed pins the confirmation token's single use: a
// deploy presenting the token this run's plan already consumed — with a fresh
// client operation ID, so dedup cannot answer it — must refuse the typed
// token-missing arm. It returns the refusal's message for the absence scan.
func assertHostPipelineTokenConsumed(ctx context.Context, t *testing.T, client *appwire.Client, hostName, token, replayOpID string) string {
	t.Helper()
	_, err := clientRequest[appwire.HostDeployResponse](ctx, client, appwire.MethodEvenerHostDeploy, appwire.HostDeployParams{Name: hostName, Token: token, OperationID: replayOpID})
	if err == nil {
		t.Fatal("evener/host/deploy accepted the consumed confirmation token again; a single-use token must be gone after the deploy that consumed it")
	}
	var wire appwire.WireError
	if !errors.As(err, &wire) {
		t.Fatalf("the consumed token's refusal is not a WireError: %v (%T)", err, err)
	}
	data, ok := wire.Data.(map[string]any)
	if !ok || data["evenerErrorInfo"] != string(appwire.ErrorTokenMissing) {
		t.Fatalf("the consumed token refused with data %v, want evenerErrorInfo %q (refusal: %v)", wire.Data, appwire.ErrorTokenMissing, err)
	}
	t.Logf("single-use token: the replay refused with the typed %s arm: %s", appwire.ErrorTokenMissing, wire.Message)
	return wire.Message
}

// hostPipelineRecordProse collects every human-readable line a record carries,
// for the fencing-absence scan.
func hostPipelineRecordProse(record appwire.OperationRecord) string {
	prose := hostPipelineProgressText(record)
	if record.Result != nil {
		prose += record.Result.Message + "\n"
	}
	return prose
}

// hostPipelineProgressText renders a record's progress lines as one string.
func hostPipelineProgressText(record appwire.OperationRecord) string {
	var text strings.Builder
	for _, entry := range record.Progress {
		text.WriteString(entry.TS + " " + entry.Message + "\n")
	}
	return text.String()
}

// hostPipelineFindRecord returns the record with id, if the page carries it.
func hostPipelineFindRecord(records []appwire.OperationRecord, id string) (appwire.OperationRecord, bool) {
	for _, record := range records {
		if record.ID == id {
			return record, true
		}
	}
	return appwire.OperationRecord{}, false
}

// hostPipelineRecordIDs lists the page's record ids for a diagnostic.
func hostPipelineRecordIDs(records []appwire.OperationRecord) []string {
	ids := make([]string, 0, len(records))
	for _, record := range records {
		ids = append(ids, record.ID)
	}
	return ids
}

// assertHostPipelineNoFencingHelpers proves the container host carries no
// fencing machinery: no evener-fence-style command on PATH, no fence/claim
// helper in the usual bin directories, and no fence/claim/orphan state file
// anywhere under the test-owned run directory (which holds the deployed binary
// and the host hub's state root).
func assertHostPipelineNoFencingHelpers(t *testing.T, host *hostSSH, hostDir string) {
	t.Helper()
	script := "if command -v evener-fence >/dev/null 2>&1; then echo \"command evener-fence: $(command -v evener-fence)\"; fi; " +
		"for d in /usr/local/bin /usr/bin /bin /usr/sbin /sbin; do for f in \"$d\"/*fence* \"$d\"/*claim*; do [ -e \"$f\" ] && echo \"$f\"; done; done; " +
		"find " + shellquote.RemoteWord(hostDir) + " \\( -name '*fence*' -o -name '*claim*' -o -name '*orphan*' \\) -print 2>/dev/null; " +
		"echo EVENER_FENCING_SCAN_DONE"
	out := host.output(script)
	found := strings.TrimSpace(strings.TrimSuffix(out, "EVENER_FENCING_SCAN_DONE"))
	if found != "" {
		t.Fatalf("host %s carries fencing-shaped helpers or state, but the crash-fencing stack was removed from main: %s", host.target, found)
	}
	t.Log("fencing absence: the container host has no evener-fence command, no fence/claim helper binary, and no fence/claim/orphan state under the run directory")
}

// hostPipelineFencingProsePattern matches the withdrawn crash-fencing
// vocabulary as whole words: a bare substring scan for "claim" also matched
// benign words like "reclaim" and "disclaim" (roborev's Low finding on the
// first revision), while the stem alternations keep the teeth for the
// inflections a fencing refusal would actually carry.
var hostPipelineFencingProsePattern = regexp.MustCompile(`(?i)\b(?:fenc(?:e|es|ed|ing)|orphan(?:s|ed|ing)?|claim(?:s|ed|ing|ant)?)\b`)

// assertHostPipelineNoFencingProse fails when the happy path carries the
// withdrawn crash-fencing vocabulary in any record prose or refusal message.
func assertHostPipelineNoFencingProse(t *testing.T, texts ...string) {
	t.Helper()
	for _, text := range texts {
		if match := hostPipelineFencingProsePattern.FindString(text); match != "" {
			t.Fatalf("the happy path carries the withdrawn crash-fencing vocabulary %q: %s", match, text)
		}
	}
}

// hostPipelineHostHealth reads the deployed host hub's /api/health from the
// host's own loopback over ssh — the same curl probe the product's restart
// verification uses — and decodes it, so the restart's process replacement is
// proven from the host side rather than inferred from the record.
func hostPipelineHostHealth(t *testing.T, host *hostSSH, addr string) hubapi.HealthResponse {
	t.Helper()
	url := "http://" + addr + "/api/health"
	out := host.output("curl -q --noproxy '*' -fsS --connect-timeout 2 --max-time 5 " + shellquote.RemoteWord(url))
	var resp hubapi.HealthResponse
	if err := json.Unmarshal([]byte(out), &resp); err != nil {
		t.Fatalf("the host hub's %s answer did not decode: %v: %s", url, err, out)
	}
	if resp.Version == "" {
		t.Fatalf("the host hub's %s answer carries no version: %s", url, out)
	}
	// Ownership: the listener on the configured address must be the hub this run
	// configured — the hub echoes the address it bound (main.go overwrites
	// cfg.Addr with hubListener.Addr().String()), so a different process that
	// grabbed the port, or a stale hub on another address, cannot satisfy this.
	if resp.HubAddr != addr {
		t.Fatalf("the listener at %s reports hub_addr %q, want %q (the address's owner must be the hub this run configured)", url, resp.HubAddr, addr)
	}
	return resp
}

// The bounds on the host hub's private loopback address: probe-verified
// candidates are drawn from crypto/rand over this range, and the allocation
// gives up after this many conflicts rather than reading a taken port as free.
const (
	hostPipelineHostPortBase     = 20000
	hostPipelineHostPortSpan     = 20000
	hostPipelineHostPortAttempts = 12
)

// hostPipelinePortAllocUnavailable is the allocation script's answer when the
// host lacks the tools to bind and name a port; the caller then falls back to
// the probe-verified draw.
const hostPipelinePortAllocUnavailable = "EVENER_PORT_ALLOC_UNAVAILABLE"

// hostPipelinePortAllocFailed is the allocation script's answer when the host
// has the tools but the bind could not be named (for example a host whose nc
// does not take busybox's flags); the caller falls back to the probe-verified
// draw here too.
const hostPipelinePortAllocFailed = "EVENER_PORT_ALLOC_FAILED"

// hostPipelinePortAllocPrefix prefixes the allocation script's success answer,
// e.g. "EVENER_PORT_ALLOC 127.0.0.1:39381".
const hostPipelinePortAllocPrefix = "EVENER_PORT_ALLOC "

// hostPipelineHostAddr picks the private loopback address the deployed host hub
// listens on. The port is allocated the way the product allocates its own
// listener: bind 127.0.0.1:0, read back the port the kernel handed out, release
// it, and use it. The controller cannot bind in the HOST's network namespace, so
// the allocation runs on the host itself (hostPipelineBindFreePortScript:
// busybox nc binds, lsof names the bound port). Where the host lacks those tools
// — the EVENER_SSH_E2E_HOST override path is not the container image — the same
// property is reached the other way round: candidates are drawn from crypto/rand
// and each is PROVED free on the host with the product's own listener probe,
// retrying on a conflict and failing closed on an unprobeable host.
//
// Either way the port is not held between the pick and the host hub's bind — the
// same residual the product's own bind-:0 allocation documents (cmd/evener-hub/
// main.go: "sidestepping the TOCTOU race in 'probe a free port, then hope
// nothing else grabs it'") — and the host hub's bind is the arbiter.
// hostPipelineHostHealth then proves ownership: the listener answering on the
// address must report that same address.
func hostPipelineHostAddr(t *testing.T, host *hostSSH) string {
	t.Helper()
	addr, ok, err := hostPipelineBindFreeAddr(host)
	switch {
	case err != nil:
		t.Fatalf("bind and release a free loopback port on host %s: %v", host.target, err)
	case ok:
		t.Logf("host hub address %s: allocated by binding 127.0.0.1:0 on the host and releasing it", addr)
		return addr
	}
	addr, err = hostPipelineProbedFreeAddr(host)
	if err != nil {
		t.Fatalf("find a free loopback port on host %s: %v", host.target, err)
	}
	t.Logf("host hub address %s: verified free with the product's listener probe (this host has no bind-and-release tools)", addr)
	return addr
}

// hostPipelineBindFreePortScript binds 127.0.0.1:0 with busybox nc, names the
// bound port with lsof, prints it, and releases the bind. Every answer is a
// marker, so a host where the tools are missing is never mistaken for a port.
func hostPipelineBindFreePortScript() string {
	return `if ! command -v nc >/dev/null 2>&1 || ! command -v lsof >/dev/null 2>&1; then echo ` + hostPipelinePortAllocUnavailable + `; exit 0; fi
nc -l -s 127.0.0.1 -p 0 >/dev/null 2>&1 &
p=$!
out=""
i=0
while [ $i -lt 20 ] && [ -z "$out" ]; do
  out=$(lsof -p $p -a -iTCP -sTCP:LISTEN -Pn 2>/dev/null | sed -n 's/.*127\.0\.0\.1:\([0-9][0-9]*\).*/\1/p' | head -1)
  i=$((i+1))
  [ -n "$out" ] || sleep 0.05
done
kill $p 2>/dev/null
if [ -z "$out" ]; then echo ` + hostPipelinePortAllocFailed + `; else echo "` + hostPipelinePortAllocPrefix + `127.0.0.1:$out"; fi`
}

// hostPipelineParseBindFreeAddr maps the allocation script's answer: the
// success marker with a loopback address is taken; the unavailable marker — and
// a failed bind, which is what a host whose nc does not take busybox's flags
// answers — returns ok=false so the caller falls back to the probe-verified
// draw. Anything unrecognized is an error rather than a port, so a script bug
// cannot be read as an allocation.
func hostPipelineParseBindFreeAddr(out []byte) (addr string, ok bool, err error) {
	line := strings.TrimSpace(string(out))
	switch {
	case line == hostPipelinePortAllocUnavailable || line == hostPipelinePortAllocFailed:
		return "", false, nil
	case strings.HasPrefix(line, hostPipelinePortAllocPrefix):
		addr = strings.TrimSpace(strings.TrimPrefix(line, hostPipelinePortAllocPrefix))
		if !strings.HasPrefix(addr, "127.0.0.1:") {
			return "", false, fmt.Errorf("the allocation answered %q, want a 127.0.0.1 address", line)
		}
		if port, perr := strconv.Atoi(strings.TrimPrefix(addr, "127.0.0.1:")); perr != nil || port < 1 || port > 65535 {
			return "", false, fmt.Errorf("the allocation answered %q, which is not a usable TCP port (1-65535)", line)
		}
		return addr, true, nil
	default:
		return "", false, fmt.Errorf("the allocation answered %q", line)
	}
}

// hostPipelineBindFreeAddr runs the bind-and-release allocation on the host.
func hostPipelineBindFreeAddr(host *hostSSH) (string, bool, error) {
	out, err := host.runStdout(hostPipelineBindFreePortScript())
	if err != nil {
		return "", false, fmt.Errorf("allocation script: %w: %s", err, out)
	}
	return hostPipelineParseBindFreeAddr(out)
}

// hostPipelineProbedFreeAddr is the fallback for a host without nc/lsof: it
// draws candidate ports and returns the first the product's own listener probe
// proves free. Only NoListenerMarker is read as absence — a named pid and the
// held-but-unnameable marker are conflicts, and a probe that cannot run fails
// closed rather than being read as a free port.
func hostPipelineProbedFreeAddr(host *hostSSH) (string, error) {
	port, err := hostPipelinePickFreePort(hostPipelineCandidatePort, func(port int) ([]byte, error) {
		return host.runStdout(sshconn.ListenerProbeRemote(strconv.Itoa(port)))
	})
	if err != nil {
		return "", err
	}
	return "127.0.0.1:" + strconv.Itoa(port), nil
}

// hostPipelineCandidatePort draws one candidate from crypto/rand over the
// allocation range, so two runs' candidates are never correlated.
func hostPipelineCandidatePort() (int, error) {
	n, err := rand.Int(rand.Reader, big.NewInt(hostPipelineHostPortSpan))
	if err != nil {
		return 0, fmt.Errorf("draw a candidate host port: %w", err)
	}
	return hostPipelineHostPortBase + int(n.Int64()), nil
}

// hostPipelinePickFreePort is the verify-and-retry seam: it draws candidates and
// returns the first its probe proves free, failing closed after the attempt
// bound or on a probe that cannot run. Draw and probe are seams, so the logic is
// testable without a host.
func hostPipelinePickFreePort(draw func() (int, error), probe func(port int) ([]byte, error)) (int, error) {
	var last string
	for attempt := range hostPipelineHostPortAttempts {
		port, err := draw()
		if err != nil {
			return 0, err
		}
		out, err := probe(port)
		if err != nil {
			return 0, fmt.Errorf("probe candidate port %d (attempt %d): %w: %s", port, attempt+1, err, out)
		}
		if strings.TrimSpace(string(out)) == sshconn.NoListenerMarker {
			return port, nil
		}
		last = strings.TrimSpace(string(out))
	}
	return 0, fmt.Errorf("no candidate port proved free in %d attempts (last probe answer %q)", hostPipelineHostPortAttempts, last)
}

// hostPipelineDockerHostIsRemote reports whether a DOCKER_HOST value names a
// daemon this controller cannot reach the container's published loopback port
// on: anything that is not a local unix socket.
func hostPipelineDockerHostIsRemote(dockerHost string) bool {
	host := strings.TrimSpace(dockerHost)
	return host != "" && !strings.HasPrefix(host, "unix://")
}

// hostPipelineRequireLocalDocker skips the check when Docker is configured to
// talk to a daemon on another machine: the container's sshd is published on the
// DAEMON host's loopback, which this controller cannot reach, so the readiness
// wait could only time out. The check can run only against a local daemon (the
// accepted D-7 plan's per-run loopback publish).
func hostPipelineRequireLocalDocker(t *testing.T, docker string) {
	t.Helper()
	if hostPipelineDockerHostIsRemote(os.Getenv("DOCKER_HOST")) {
		t.Skipf("DOCKER_HOST=%s names a non-local docker daemon; this check publishes the container's sshd on this controller's 127.0.0.1 and cannot reach a remote daemon's port", os.Getenv("DOCKER_HOST"))
	}
	out, err := hostPipelineDockerRun(docker, 15*time.Second, "context", "inspect", "--format", "{{.Endpoints.docker.Host}}")
	if err != nil {
		// A client without contexts: DOCKER_HOST is the only remote mechanism.
		return
	}
	if host := strings.TrimSpace(string(out)); hostPipelineDockerHostIsRemote(host) {
		t.Skipf("the active docker context talks to %s, not a local unix socket; this check publishes the container's sshd on this controller's 127.0.0.1 and cannot reach a remote daemon's port", host)
	}
}

// hostPipelineAwaitPublishedPort proves the container's published sshd port is
// reachable from THIS controller before the ssh readiness wait spends its
// budget on it; with a remote daemon the publish lands on the daemon's host.
// The dial is retried briefly because the publish takes a moment after
// `docker run`.
func hostPipelineAwaitPublishedPort(t *testing.T, port int) {
	t.Helper()
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	deadline := time.Now().Add(15 * time.Second)
	var last error
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", addr, time.Second)
		if err == nil {
			_ = conn.Close()
			return
		}
		last = err
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("the container's published sshd port %s is not reachable from this controller (%v); if Docker is configured for a remote daemon, its published port lives on that host", addr, last)
}

// hostPipelineContainer is one disposable alpine+sshd container standing in for
// a fresh host, plus the per-run pieces that reach it.
type hostPipelineContainer struct {
	name          string
	id            string
	alias         string
	port          int
	dir           string
	sshConfigPath string
}

// startHostPipelineContainer brings up the disposable container host: it
// ensures the small alpine+sshd image, generates a per-run ed25519 key,
// publishes the container's sshd on a per-run loopback port, authorizes the
// key, writes the per-run ssh_config, and waits for the sshd to accept it. It
// skips cleanly when Docker is unavailable and removes the container in
// cleanup.
func startHostPipelineContainer(t *testing.T) *hostPipelineContainer {
	t.Helper()
	docker, err := exec.LookPath("docker")
	if err != nil {
		t.Skipf("docker is not on PATH (%v), so the disposable container host cannot be started; set EVENER_SSH_E2E_HOST to run this check against an existing host", err)
	}
	infoCtx, cancelInfo := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancelInfo()
	if out, err := exec.CommandContext(infoCtx, docker, "info", "--format", "{{.ServerVersion}}").CombinedOutput(); err != nil {
		t.Skipf("the docker daemon is unavailable (%v): %s; set EVENER_SSH_E2E_HOST to run this check against an existing host", err, strings.TrimSpace(string(out)))
	}
	hostPipelineRequireLocalDocker(t, docker)
	image := ensureHostPipelineImage(t, docker)

	dir := t.TempDir()
	keyPath, pubPath := hostPipelineKey(t, dir)
	configPath := filepath.Join(dir, "ssh_config")
	runID := hostDeployRunID()
	name := "evener-pipeline-" + runID
	alias := "evener-e2e-" + runID

	// The container's removal is registered before the first attempt: a failed
	// `docker run` can leave a created-but-not-started container under this name,
	// and every exit path from here — including the t.Fatalfs below — must not
	// orphan one. A name no attempt ever created is not an error to report.
	t.Cleanup(func() {
		if out, err := hostPipelineDockerRun(docker, time.Minute, "rm", "-f", name); err != nil && !strings.Contains(string(out), "No such container") {
			t.Errorf("remove the disposable container %s: %v: %s", name, err, out)
		}
	})

	// The kernel picks the loopback port the container's sshd is published on,
	// and `docker run` is retried with a fresh port if that one was taken in the
	// window between the probe and the publish. Each failed attempt's leftovers
	// are removed before the next one reuses the name.
	port := 0
	var runOut []byte
	var runErr error
	for range 3 {
		port = hostPipelineFreePort(t)
		_, _ = hostPipelineDockerRun(docker, time.Minute, "rm", "-f", name)
		runOut, runErr = hostPipelineDockerRun(docker, 2*time.Minute, "run", "-d", "--name", name, "-p", fmt.Sprintf("127.0.0.1:%d:22", port), image)
		if runErr == nil {
			break
		}
	}
	if runErr != nil {
		t.Fatalf("start the disposable container %s (image %s) on a loopback port: %v: %s", name, image, runErr, runOut)
	}
	container := &hostPipelineContainer{
		name:          name,
		id:            strings.TrimSpace(string(runOut)),
		alias:         alias,
		port:          port,
		dir:           dir,
		sshConfigPath: configPath,
	}
	hostPipelineAwaitPublishedPort(t, port)

	if err := os.WriteFile(configPath, []byte(hostPipelineSSHConfig(alias, port, keyPath)), 0o600); err != nil {
		t.Fatalf("write the per-run ssh_config %s: %v", configPath, err)
	}
	for _, step := range [][]string{
		{"exec", name, "mkdir", "-p", "/root/.ssh"},
		{"cp", pubPath, name + ":/root/.ssh/authorized_keys"},
		{"exec", name, "sh", "-c", "chmod 700 /root/.ssh && chmod 600 /root/.ssh/authorized_keys && chown -R root:root /root/.ssh"},
	} {
		if out, err := hostPipelineDockerRun(docker, time.Minute, step...); err != nil {
			t.Fatalf("authorize the per-run key in %s (docker %s): %v: %s", name, strings.Join(step, " "), err, out)
		}
	}
	hostPipelineAwaitSSH(t, docker, name, configPath, alias)
	return container
}

// hostPipelineAwaitSSH waits until the container's sshd accepts the per-run key
// through the per-run config.
func hostPipelineAwaitSSH(t *testing.T, docker, name, configPath, alias string) {
	t.Helper()
	deadline := time.Now().Add(hostPipelineReadyTimeout)
	var last string
	for time.Now().Before(deadline) {
		out, err := hostPipelineSSHProbe(configPath, alias)
		if err == nil {
			return
		}
		last = strings.TrimSpace(string(out))
		time.Sleep(500 * time.Millisecond)
	}
	logs, _ := hostPipelineDockerRun(docker, time.Minute, "logs", "--tail", "40", name)
	t.Fatalf("the container's sshd (%s) never accepted the per-run key within %s (last ssh answer: %s); docker logs:\n%s",
		alias, hostPipelineReadyTimeout, last, logs)
}

// hostPipelineSSHProbe runs one readiness ssh through the per-run config.
func hostPipelineSSHProbe(configPath, alias string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return exec.CommandContext(ctx, "ssh", "-F", configPath, "-T", "-o", "BatchMode=yes", "-o", "ConnectTimeout=3", "--", alias, "true").CombinedOutput()
}

// installHostPipelineSSHShim writes the per-run `ssh` shim and prepends its
// directory to PATH, so both the product's ssh invocations (children of the
// controller hub, which inherits this process's PATH) and this harness's ssh
// invocations resolve the per-run alias through the per-run config.
//
// A shim is needed because OpenSSH resolves its user config from the
// passwd-database home, NOT $HOME: the controller runs with an isolated HOME
// and the product's plain `ssh` invocation would never consult a config placed
// there. The shim does not change what any invocation is asked to do — it hands
// the config to exactly the alias's invocations, and every other invocation is
// exec'd with its argv untouched — so no product code changes.
func installHostPipelineSSHShim(t *testing.T, dir, configPath, alias string) {
	t.Helper()
	realSSH, err := exec.LookPath("ssh")
	if err != nil {
		t.Fatalf("locate the real ssh binary for the per-run shim: %v", err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("create the shim directory %s: %v", dir, err)
	}
	shimPath := filepath.Join(dir, "ssh")
	if err := os.WriteFile(shimPath, []byte(hostPipelineSSHShimScript(realSSH, configPath, alias)), 0o755); err != nil {
		t.Fatalf("write the per-run ssh shim %s: %v", shimPath, err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// hostPipelineSSHShimScript is the shim's text: it redirects every invocation
// whose arguments name the per-run alias through the per-run config, and
// passes every other invocation through to the real ssh unchanged.
func hostPipelineSSHShimScript(realSSH, configPath, alias string) string {
	quoted := shellquote.RemoteWord(realSSH)
	config := shellquote.RemoteWord(configPath)
	pattern := shellquote.RemoteWord(alias)
	return fmt.Sprintf(`#!/bin/sh
# evener pipeline e2e: the per-run ssh shim. OpenSSH reads its user config from
# the passwd home, not $HOME, so the per-run config is handed to exactly this
# run's alias; every other invocation is untouched.
for a in "$@"; do
  case "$a" in
    %s) exec %s -F %s "$@";;
  esac
done
exec %s "$@"
`, pattern, quoted, config, quoted)
}

// hostPipelineSSHConfig is the per-run ssh_config: the alias reaches the
// container's sshd on the published loopback port as root with the per-run key,
// and host-key checking is off with no known_hosts file, so nothing prompts and
// no operator file is read or written.
func hostPipelineSSHConfig(alias string, port int, keyPath string) string {
	return fmt.Sprintf("Host %s\n"+
		"  HostName 127.0.0.1\n"+
		"  Port %d\n"+
		"  User root\n"+
		"  IdentityFile %s\n"+
		"  IdentitiesOnly yes\n"+
		"  StrictHostKeyChecking no\n"+
		"  UserKnownHostsFile /dev/null\n"+
		"  LogLevel ERROR\n", alias, port, keyPath)
}

// hostPipelineDockerfile is the one small image this check caches: alpine plus
// exactly the host tools the product's ssh paths invoke. `curl` serves the
// health probe; `lsof` names the listener pid the restart lookups need; `procps`
// provides the real `ps` whose `-p` flag busybox's applet does not take.
const hostPipelineDockerfile = `FROM alpine:3.20
RUN apk add --no-cache openssh curl lsof procps \
 && ssh-keygen -A \
 && mkdir -p /run/sshd
CMD ["/usr/sbin/sshd", "-D", "-e"]
`

// ensureHostPipelineImage returns the cached disposable-host image, building it
// once when it is absent. The build context lives in this test's own t.TempDir()
// and is removed with it: the image cache is the tag, so nothing is gained by
// keeping the context around, and a fixed shared path would race (or linger)
// across runs.
func ensureHostPipelineImage(t *testing.T, docker string) string {
	t.Helper()
	if _, err := hostPipelineDockerRun(docker, time.Minute, "image", "inspect", hostPipelineImage); err == nil {
		return hostPipelineImage
	}
	contextDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(contextDir, "Dockerfile"), []byte(hostPipelineDockerfile), 0o600); err != nil {
		t.Fatalf("write the image Dockerfile: %v", err)
	}
	out, err := hostPipelineDockerRun(docker, 10*time.Minute, "build", "-t", hostPipelineImage, contextDir)
	if err != nil {
		t.Fatalf("build the disposable host image %s: %v: %s", hostPipelineImage, err, out)
	}
	return hostPipelineImage
}

// hostPipelineKey generates the per-run ed25519 key pair authorized in the
// container.
func hostPipelineKey(t *testing.T, dir string) (keyPath, pubPath string) {
	t.Helper()
	keyPath = filepath.Join(dir, "id_ed25519")
	if out, err := exec.Command("ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-f", keyPath, "-C", "evener-e2e-pipeline").CombinedOutput(); err != nil {
		t.Fatalf("generate the per-run ssh key %s: %v: %s", keyPath, err, out)
	}
	return keyPath, keyPath + ".pub"
}

// hostPipelineFreePort reserves a free loopback port for the container's sshd
// publish and returns it. The window between close and `docker run` is closed by
// the caller's bounded retry.
func hostPipelineFreePort(t *testing.T) int {
	t.Helper()
	var lc net.ListenConfig
	listener, err := lc.Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve a loopback port for the container's sshd: %v", err)
	}
	tcpAddr, ok := listener.Addr().(*net.TCPAddr)
	if !ok {
		_ = listener.Close()
		t.Fatalf("the reserved loopback listener reports a %T address, want *net.TCPAddr", listener.Addr())
	}
	port := tcpAddr.Port
	if err := listener.Close(); err != nil {
		t.Fatalf("release the reserved loopback port %d: %v", port, err)
	}
	return port
}

// hostPipelineDockerRun runs one bounded docker command.
func hostPipelineDockerRun(docker string, timeout time.Duration, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	return exec.CommandContext(ctx, docker, args...).CombinedOutput()
}

// TestHostPipelineContainerGate pins the live check's gate order and reasons
// without a docker daemon, an ssh, or a host: the write gate first, then the
// master gate, then the requirement that a host source is named, and the
// override's precedence is decided by the caller (dest non-empty ⇒ no
// container).
func TestHostPipelineContainerGate(t *testing.T) {
	cases := []struct {
		name                       string
		short                      bool
		deploy, master, cont, dest string
		wantSkip                   bool
		wantReasonContains         []string
	}{
		{name: "short always skips", short: true, deploy: "1", master: "1", cont: "1", wantSkip: true, wantReasonContains: []string{"live deploy-pipeline test"}},
		{name: "write gate first", deploy: "", master: "1", cont: "1", wantSkip: true, wantReasonContains: []string{"EVENER_SSH_E2E_DEPLOY=1", "WRITES"}},
		{name: "master gate next", deploy: "1", master: "", cont: "1", wantSkip: true, wantReasonContains: []string{"EVENER_SSH_E2E=1"}},
		{name: "host source required", deploy: "1", master: "1", cont: "", dest: "", wantSkip: true, wantReasonContains: []string{"EVENER_SSH_E2E_CONTAINER=1", "EVENER_SSH_E2E_HOST"}},
		{name: "container gate", deploy: "1", master: "1", cont: "1"},
		{name: "host override", deploy: "1", master: "1", dest: "paradise-park"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := hostPipelineContainerGate(tc.short, tc.deploy, tc.master, tc.cont, tc.dest)
			if tc.wantSkip && got == "" {
				t.Fatal("gate returned no skip reason, want one")
			}
			if !tc.wantSkip && got != "" {
				t.Fatalf("gate returned skip reason %q, want none", got)
			}
			for _, want := range tc.wantReasonContains {
				if !strings.Contains(got, want) {
					t.Fatalf("skip reason %q does not name %q", got, want)
				}
			}
		})
	}
}

// TestHostPipelineSSHConfigPinsTheRedirect pins the per-run config's fields
// without a container: the alias must resolve to the published loopback port as
// root with the per-run key, and the host-key checks must stay off (an unknown
// host key with BatchMode would otherwise refuse the first dial).
func TestHostPipelineSSHConfigPinsTheRedirect(t *testing.T) {
	const (
		alias   = "evener-e2e-1234-5678"
		keyPath = "/run/keys/id_ed25519"
	)
	got := hostPipelineSSHConfig(alias, 20345, keyPath)
	for _, want := range []string{
		"Host " + alias,
		"HostName 127.0.0.1",
		"Port 20345",
		"User root",
		"IdentityFile " + keyPath,
		"StrictHostKeyChecking no",
		"UserKnownHostsFile /dev/null",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("per-run ssh_config does not carry %q:\n%s", want, got)
		}
	}
}

// TestHostPipelineSSHShimOnlyRedirectsTheAlias pins the shim's precision without
// a container: an invocation naming the alias is handed the per-run config
// (with the caller's argv preserved), and an invocation naming anything else is
// passed through untouched. A shim that leaked -F onto other destinations, or
// dropped it for the alias, would show up here.
func TestHostPipelineSSHShimOnlyRedirectsTheAlias(t *testing.T) {
	dir := t.TempDir()
	stub := filepath.Join(dir, "real-ssh")
	if err := os.WriteFile(stub, []byte("#!/bin/sh\nprintf '%s\\n' \"$@\"\n"), 0o755); err != nil {
		t.Fatalf("write stub ssh: %v", err)
	}
	shim := filepath.Join(dir, "ssh")
	const (
		alias  = "evener-e2e-42"
		config = "/run/evener-e2e-42/ssh_config"
	)
	if err := os.WriteFile(shim, []byte(hostPipelineSSHShimScript(stub, config, alias)), 0o755); err != nil {
		t.Fatalf("write shim: %v", err)
	}

	run := func(args ...string) string {
		t.Helper()
		out, err := exec.Command(shim, args...).CombinedOutput()
		if err != nil {
			t.Fatalf("run shim %v: %v: %s", args, err, out)
		}
		return string(out)
	}
	redirected := run("-T", "-o", "BatchMode=yes", "--", alias, "true")
	if !strings.Contains(redirected, "-F\n"+config+"\n") {
		t.Fatalf("alias invocation was not handed the per-run config:\n%s", redirected)
	}
	if !strings.Contains(redirected, alias) || !strings.Contains(redirected, "BatchMode=yes") {
		t.Fatalf("alias invocation lost the caller's argv:\n%s", redirected)
	}
	untouched := run("-T", "-o", "BatchMode=yes", "--", "other-host", "true")
	if strings.Contains(untouched, "-F") {
		t.Fatalf("a non-alias invocation was redirected:\n%s", untouched)
	}
	if !strings.Contains(untouched, "other-host") {
		t.Fatalf("a non-alias invocation lost its destination:\n%s", untouched)
	}
}

// TestHostPipelineParseBindFreeAddr pins the allocation script's answer handling
// without a host: the unavailable marker falls back, a valid loopback address is
// taken, and a failed bind, a portless address, a non-loopback address, or
// anything unrecognized is an error rather than a port.
func TestHostPipelineParseBindFreeAddr(t *testing.T) {
	for _, tc := range []struct {
		name    string
		out     string
		want    string
		wantOK  bool
		wantErr bool
	}{
		{name: "allocated", out: hostPipelinePortAllocPrefix + "127.0.0.1:39381\n", want: "127.0.0.1:39381", wantOK: true},
		{name: "unavailable", out: hostPipelinePortAllocUnavailable + "\n"},
		{name: "failed bind falls back", out: hostPipelinePortAllocFailed + "\n"},
		{name: "portless", out: hostPipelinePortAllocPrefix + "127.0.0.1:\n", wantErr: true},
		{name: "port zero", out: hostPipelinePortAllocPrefix + "127.0.0.1:0\n", wantErr: true},
		{name: "port out of range", out: hostPipelinePortAllocPrefix + "127.0.0.1:65536\n", wantErr: true},
		{name: "non-loopback", out: hostPipelinePortAllocPrefix + "10.0.0.1:39381\n", wantErr: true},
		{name: "unrecognized output", out: "hello\n", wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, ok, err := hostPipelineParseBindFreeAddr([]byte(tc.out))
			if tc.wantErr {
				if err == nil {
					t.Fatalf("parse(%q) = (%q, %t, nil), want an error", tc.out, got, ok)
				}
				return
			}
			if err != nil {
				t.Fatalf("parse(%q): %v", tc.out, err)
			}
			if ok != tc.wantOK || got != tc.want {
				t.Fatalf("parse(%q) = (%q, %t), want (%q, %t)", tc.out, got, ok, tc.want, tc.wantOK)
			}
		})
	}
}

// TestHostPipelinePickFreePortSkipsConflicts pins the verify-and-retry logic
// without a host: a candidate the probe names as held is skipped, only the
// proved-empty marker is taken, a held-but-unnameable answer is a conflict
// rather than absence, a probe that cannot run fails closed, and a failed draw
// is returned rather than read as a port.
func TestHostPipelinePickFreePortSkipsConflicts(t *testing.T) {
	t.Run("takes the first proved-free candidate", func(t *testing.T) {
		next := 20100
		got, err := hostPipelinePickFreePort(
			func() (int, error) { next++; return next, nil },
			func(port int) ([]byte, error) {
				if port == 20101 {
					return []byte("4242\n"), nil
				}
				return []byte(sshconn.NoListenerMarker + "\n"), nil
			},
		)
		if err != nil {
			t.Fatalf("pick: %v", err)
		}
		if got != 20102 {
			t.Fatalf("picked %d, want 20102 (the taken candidate must be skipped)", got)
		}
	})
	t.Run("a held-but-unnameable answer is a conflict", func(t *testing.T) {
		got, err := hostPipelinePickFreePort(
			func() (int, error) { return 20103, nil },
			func(int) ([]byte, error) { return []byte(sshconn.ListenerPresentMarker + "\n"), nil },
		)
		if err == nil || got != 0 {
			t.Fatalf("pick = (%d, %v), want an exhaustion error: the unnameable marker is not absence", got, err)
		}
	})
	t.Run("a probe that cannot run fails closed", func(t *testing.T) {
		_, err := hostPipelinePickFreePort(
			func() (int, error) { return 20104, nil },
			func(int) ([]byte, error) { return nil, errors.New("no listener probe is available on this host") },
		)
		if err == nil {
			t.Fatal("pick returned no error for an unprobeable host")
		}
	})
	t.Run("a failed draw is returned", func(t *testing.T) {
		_, err := hostPipelinePickFreePort(
			func() (int, error) { return 0, errors.New("no entropy") },
			func(int) ([]byte, error) { return []byte(sshconn.NoListenerMarker), nil },
		)
		if err == nil {
			t.Fatal("pick returned no error for a failed draw")
		}
	})
}

// TestHostPipelineBindFreePortScriptGatesOnItsTools pins that the allocation
// script fails over to the unavailable marker — rather than answering as if it
// allocated a port — when the host lacks nc or lsof, and that its success answer
// carries the prefix the parser reads.
func TestHostPipelineBindFreePortScriptGatesOnItsTools(t *testing.T) {
	script := hostPipelineBindFreePortScript()
	for _, want := range []string{
		"command -v nc",
		"command -v lsof",
		hostPipelinePortAllocUnavailable,
		hostPipelinePortAllocPrefix,
	} {
		if !strings.Contains(script, want) {
			t.Fatalf("the allocation script does not carry %q:\n%s", want, script)
		}
	}
}

// TestHostPipelineDockerHostIsRemote pins the local-daemon predicate the
// container check skips on: a unix socket (or an unset value) is local, and
// every other scheme — tcp, ssh, npipe — names a daemon whose published
// loopback port this controller cannot reach.
func TestHostPipelineDockerHostIsRemote(t *testing.T) {
	for _, tc := range []struct {
		host string
		want bool
	}{
		{host: "", want: false},
		{host: "unix:///var/run/docker.sock", want: false},
		{host: "unix:///run/user/1000/docker.sock", want: false},
		{host: "tcp://10.0.0.5:2375", want: true},
		{host: "ssh://builder.example", want: true},
		{host: "npipe:////./pipe/docker_engine", want: true},
	} {
		if got := hostPipelineDockerHostIsRemote(tc.host); got != tc.want {
			t.Fatalf("hostPipelineDockerHostIsRemote(%q) = %t, want %t", tc.host, got, tc.want)
		}
	}
}

// TestHostPipelineFencingProsePatternMatchesWholeWords pins roborev's Low
// finding: the scan must catch the fencing stems as words (and the inflections
// a fencing refusal would carry) while leaving benign words that merely contain
// them alone.
func TestHostPipelineFencingProsePatternMatchesWholeWords(t *testing.T) {
	for _, text := range []string{"a fencing-helper refusal", "the orphaned record", "a claim primitive"} {
		if !hostPipelineFencingProsePattern.MatchString(text) {
			t.Fatalf("the pattern does not match %q, so the fencing-absence scan lost its teeth", text)
		}
	}
	for _, text := range []string{"the lock was reclaimed", "it disclaimed the writes", "it reclaims the port"} {
		if match := hostPipelineFencingProsePattern.FindString(text); match != "" {
			t.Fatalf("the pattern matched %q in %q; a benign word containing a fencing stem is not fencing prose", match, text)
		}
	}
}
