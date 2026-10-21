package hub

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/appsource"
	"primeradiant.com/evener/cmd/evener-hub/internal/hostreg"
	"primeradiant.com/evener/cmd/evener-hub/internal/sshconn"
	"primeradiant.com/evener/execsupport/shellquote"
	"primeradiant.com/evener/test/e2e/fakellm"
)

// This regression permits writes only inside a newly created container. It
// deliberately ignores EVENER_SSH_E2E_HOST, including when both are configured.
func TestHostCompatibleConnectContainerE2E(t *testing.T) {
	if testing.Short() || os.Getenv("EVENER_SSH_E2E") != "1" || os.Getenv("EVENER_SSH_E2E_DEPLOY") != "1" || os.Getenv("EVENER_SSH_E2E_CONTAINER") != "1" {
		t.Skip("requires EVENER_SSH_E2E=1 EVENER_SSH_E2E_DEPLOY=1 EVENER_SSH_E2E_CONTAINER=1")
	}
	container := startHostPipelineContainer(t)
	assertCompatibleConnectContainerIsolation(t, container.id)
	installHostPipelineSSHShim(t, filepath.Join(container.dir, "shim"), container.sshConfigPath, container.alias)
	host := newHostSSH(t, container.alias, "")
	goos, goarch := hostTarget(t, host)
	controller, err := buildHostTargetBinary(runtime.GOOS, runtime.GOARCH, "compatible-controller", deployLdflags("1111111111111111111111111111111111111111", ""))
	if err != nil {
		t.Fatal(err)
	}
	remote, err := buildHostTargetBinary(goos, goarch, "compatible-remote", deployLdflags("2222222222222222222222222222222222222222", ""))
	if err != nil {
		t.Fatal(err)
	}
	provider, err := fakellm.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(provider.Close)
	hostDir := "/root/compatible-connect-" + hostDeployRunID()
	host.prepareDeployDir(hostDir)
	target, config := hostDir+"/bin/evener", hostDir+"/hub.toml"
	addr := hostPipelineHostAddr(t, host)
	// Container cleanup owns the remote filesystem and all remote processes.
	data, err := os.ReadFile(remote)
	if err != nil {
		t.Fatal(err)
	}
	host.writeFile(target, data)
	host.mustRun("chmod 700 " + shellquote.RemoteWord(target))
	host.writeFile(config, []byte(fmt.Sprintf("addr = %q\nhub_state_root = %q\nplugin_auto_upgrade = false\n", addr, hostDir+"/state")))
	entryPath := hostDir + "/remote-only/entry-a"
	host.mustRun("mkdir -p " + shellquote.RemoteWord(entryPath))
	host.mustRun(hostHubLaunchScript(target, config, hostDir, addr))
	awaitHostHubHealth(t, host, addr, hostDir+"/hub.log")
	beforeHash := host.sha256IfFile(target)
	before := hostPipelineHostHealth(t, host, addr)
	if beforeHash == "" || before.StartedAt.IsZero() || before.Version == "" {
		t.Fatalf("missing independent identity: hash=%q health=%+v", beforeHash, before)
	}
	controllerVersion, err := binaryVersion(controller)
	if err != nil || before.Version == controllerVersion {
		t.Fatalf("builds must differ: controller=%q remote=%q err=%v", controllerVersion, before.Version, err)
	}
	// No deploy flags: exercise the controller's normal own-executable source.
	stack := startDeployStack(t, provider, controller)
	ctx, cancel := context.WithTimeout(t.Context(), 2*hostDeployAttachTimeout)
	defer cancel()
	client := stack.dialRPC(ctx, t)
	entry := appwire.HostEntry{Name: "compatible-host", Address: container.alias, EvenerPath: target, ConfigPath: config, Addr: addr}
	add := func() appwire.HostRow {
		t.Helper()
		result, err := clientRequest[appwire.HostMutationResult](ctx, client, appwire.MethodEvenerHostAdd, appwire.HostAddParams{Entry: entry})
		if err != nil || result.HostMutationCommitted == nil {
			t.Fatalf("add: %+v, %v", result, err)
		}
		return result.HostMutationCommitted.Host
	}
	row := add()
	for round := range 3 {
		if round == 2 {
			_, err := clientRequest[appwire.HostMutationResult](ctx, client, appwire.MethodEvenerHostRemove, appwire.HostRemoveParams{
				Name: entry.Name, MutationID: "disconnect-" + hostDeployRunID(), ExpectedGeneration: row.Generation, ExpectedIncarnationID: row.IncarnationID,
			})
			if err != nil {
				t.Fatal(err)
			}
			row = add()
		}
		attached, attachErr := clientRequest[appwire.HostAttachResponse](ctx, client, appwire.MethodEvenerHostAttach, appwire.HostAttachParams{Host: entry.Name})
		// Read preservation even on attach failure, so a failed destructive
		// attempt cannot hide behind its final RPC error.
		afterHash := host.sha256IfFile(target)
		after := hostPipelineHostHealth(t, host, addr)
		if afterHash != beforeHash || !after.StartedAt.Equal(before.StartedAt) {
			t.Fatalf("round %d changed installation: hash %s -> %s, started %s -> %s, attach error %v", round, beforeHash, afterHash, before.StartedAt, after.StartedAt, attachErr)
		}
		if attachErr != nil || !attached.Attached || attached.HubVersion != before.Version {
			t.Fatalf("round %d attach = %+v, err=%v, remote build=%q", round, attached, attachErr, before.Version)
		}
		status, err := clientRequest[appwire.HostStatusResponse](ctx, client, appwire.MethodEvenerHostStatus, appwire.HostStatusParams{Name: entry.Name})
		if err != nil || !status.Host.Attached || status.Host.HubVersion != before.Version {
			t.Fatalf("status = %+v, err=%v", status, err)
		}
		list, err := clientRequest[appwire.HostListResponse](ctx, client, appwire.MethodEvenerHostList, appwire.EmptyParams{})
		if err != nil || len(list.Hosts) != 1 || !list.Hosts[0].Attached || list.Hosts[0].HubVersion != before.Version {
			t.Fatalf("list = %+v, err=%v", list, err)
		}
		params := appwire.PathsCompleteParams{Prefix: hostDir + "/remote-only/"}
		raw, err := forwardHostMethod(ctx, client, entry.Name, appwire.MethodEvenerPathsComplete, mustMarshal(t, params))
		if err != nil {
			t.Fatal(err)
		}
		var remotePaths appwire.PathsCompleteResponse
		if err := json.Unmarshal(raw, &remotePaths); err != nil || !slices.Contains(remotePaths.Data, entryPath) {
			t.Fatalf("remote paths = %+v, err=%v", remotePaths, err)
		}
		localPaths, err := clientRequest[appwire.PathsCompleteResponse](ctx, client, appwire.MethodEvenerPathsComplete, params)
		if err != nil || len(localPaths.Data) != 0 {
			t.Fatalf("controller-local negative control = %+v, err=%v", localPaths, err)
		}
		operations, err := clientRequest[appwire.HostOperationsResponse](ctx, client, appwire.MethodEvenerHostOperations, appwire.HostOperationsParams{Name: entry.Name})
		if err != nil || len(operations.Operations) != 0 {
			t.Fatalf("Connect created operations: %+v, err=%v", operations, err)
		}
	}
	// The public host surface has no capability-probe RPC. Exercise that
	// consumer through a second real SSH channel and the production facts adapter.
	registry, err := hostreg.New([]hostreg.Host{{Name: entry.Name, SSH: entry.Address, EvenerPath: target, ConfigPath: config, Addr: addr}})
	if err != nil {
		t.Fatal(err)
	}
	manager := sshconn.New(registry, sshconn.Options{DeployDisabled: true})
	t.Cleanup(func() { _ = manager.Close() })
	channel, err := manager.Ensure(ctx, entry.Name)
	if err != nil {
		t.Fatal(err)
	}
	source := appsource.NewRemoteHubSource(entry.Name, nil, func(context.Context, string) (*appwire.Client, error) {
		return channel.Client(), nil
	})
	source.SetHostClientIfAttached(manager.ClientIfAttached)
	source.SetHostFacts(func(_ context.Context, name string, client *appwire.Client) (appsource.HostFacts, error) {
		return remoteHostFactsForChannel(channel, name, client)
	})
	source.SetHostHandshake(func(_ string, client *appwire.Client) (appwire.InitializeResponse, bool) {
		return remoteHostHandshakeForChannel(channel, client)
	})
	caps, err := source.HostCapabilities(ctx)
	if err != nil || caps.HubVersion != before.Version || caps.ProtocolVersion != appwire.ProtocolVersion {
		t.Fatalf("capability probe: %+v, err=%v", caps, err)
	}
	if host.sha256IfFile(target) != beforeHash || !hostPipelineHostHealth(t, host, addr).StartedAt.Equal(before.StartedAt) {
		t.Fatal("capability probe changed the installed binary or running process")
	}
}

func assertCompatibleConnectContainerIsolation(t *testing.T, id string) {
	t.Helper()
	out, err := hostPipelineDockerRun("docker", time.Minute, "inspect", id)
	if err != nil {
		t.Fatalf("inspect fixture isolation: %v: %s", err, out)
	}
	var inspected []struct {
		HostConfig struct {
			Privileged                    bool
			PidMode, IpcMode, NetworkMode string
		}
		Mounts []json.RawMessage
	}
	if err := json.Unmarshal(out, &inspected); err != nil || len(inspected) != 1 {
		t.Fatalf("decode fixture isolation: %v", err)
	}
	config := inspected[0].HostConfig
	if config.Privileged || config.PidMode != "" || config.IpcMode == "host" || config.NetworkMode == "host" || len(inspected[0].Mounts) != 0 {
		t.Fatalf("unsafe fixture isolation: %+v", inspected[0])
	}
	t.Logf("verified container %s: private process namespace, unprivileged, no mounts or host sockets", id)
}
