package appwire

import (
	"net/url"
	pathpkg "path"
	"strings"
)

const PrivateBrokerPath = "/internal/artifacts/broker"

func IsPrivateBrokerMethod(method string) bool {
	return strings.HasPrefix(method, "evener/artifacts/broker/")
}

func IsExactPrivateBrokerPath(path, rawPath string) bool {
	return path == PrivateBrokerPath && (rawPath == "" || rawPath == PrivateBrokerPath)
}

// IsPrivateBrokerPath recognizes the private route even when a public proxy
// receives a cleaned or encoded spelling. Callers still require the exact form
// to serve it; this broader predicate exists only to reject forwarding.
func IsPrivateBrokerPath(path, rawPath string) bool {
	if IsExactPrivateBrokerPath(path, rawPath) {
		return true
	}
	for _, candidate := range []string{path, rawPath} {
		for range 3 {
			if candidate == "" {
				break
			}
			if pathpkg.Clean("/"+strings.TrimPrefix(candidate, "/")) == PrivateBrokerPath {
				return true
			}
			decoded, err := url.PathUnescape(candidate)
			if err != nil || decoded == candidate {
				break
			}
			candidate = decoded
		}
	}
	return false
}

const (
	MethodBrokerLaunchHello       = "evener/artifacts/broker/launchHello"
	MethodBrokerLaunchInstall     = "evener/artifacts/broker/launchInstall"
	MethodBrokerFinalizeOwnership = "evener/artifacts/broker/finalizeOwnership"
	MethodBrokerInstall           = "evener/artifacts/broker/install"
	MethodBrokerAuthenticate      = "evener/artifacts/broker/authenticate"
)

type BrokerAssociation struct {
	RealmID       string `json:"realmId"`
	HumanOwnerID  string `json:"humanOwnerId"`
	RootSessionID string `json:"rootSessionId"`
	PrincipalID   string `json:"principalId"`
	NamespaceID   string `json:"namespaceId"`
	ProjectID     string `json:"projectId"`
}

// BrokerDaemonIdentity is the complete public rendezvous ownership identity.
// Authentication always additionally requires the current nonempty Hub token
// or the owned inherited launch channel; these public fields are not a bearer.
type BrokerDaemonIdentity struct {
	PID          int    `json:"pid"`
	Address      string `json:"address"`
	Endpoint     string `json:"endpoint"`
	Protocol     string `json:"protocol"`
	SourceID     string `json:"sourceId"`
	ThreadID     string `json:"threadId"`
	SessionID    string `json:"sessionId"`
	InstanceID   string `json:"instanceId"`
	WorkspaceRef string `json:"workspaceRef"`
	WorkingDir   string `json:"workingDir"`
	StateDir     string `json:"stateDir"`
	StartedAt    string `json:"startedAt"`
}

type BrokerLaunchHelloParams struct {
	LaunchID            string `json:"launchId"`
	ActualRootSessionID string `json:"actualRootSessionId"`
	RuntimeGeneration   uint64 `json:"runtimeGeneration"`
}

type BrokerLaunchHelloResponse struct{}

type BrokerLaunchInstallParams struct {
	LaunchID    string            `json:"launchId"`
	HubEpoch    string            `json:"hubEpoch"`
	DaemonEpoch string            `json:"daemonEpoch"`
	Capability  string            `json:"capability"`
	Association BrokerAssociation `json:"association"`
}

type BrokerLaunchInstallResponse struct {
	LaunchID          string `json:"launchId"`
	RootSessionID     string `json:"rootSessionId"`
	AssociationDigest string `json:"associationDigest"`
	DaemonEpoch       string `json:"daemonEpoch"`
}

type BrokerFinalizeOwnershipParams struct {
	LaunchID             string               `json:"launchId"`
	ActualDaemonIdentity BrokerDaemonIdentity `json:"actualDaemonIdentity"`
}

type BrokerFinalizeOwnershipResponse struct {
	DaemonEpoch string `json:"daemonEpoch"`
}

type BrokerInstallParams struct {
	HubEpoch               string               `json:"hubEpoch"`
	DaemonEpoch            string               `json:"daemonEpoch"`
	Capability             string               `json:"capability"`
	Association            BrokerAssociation    `json:"association"`
	ExpectedDaemonIdentity BrokerDaemonIdentity `json:"expectedDaemonIdentity"`
}

type BrokerInstallResponse struct {
	ActualDaemonIdentity BrokerDaemonIdentity `json:"actualDaemonIdentity"`
	RootSessionID        string               `json:"rootSessionId"`
	AssociationDigest    string               `json:"associationDigest"`
	DaemonEpoch          string               `json:"daemonEpoch"`
}

type BrokerAuthenticateParams struct {
	DaemonEpoch string `json:"daemonEpoch"`
	Capability  string `json:"capability"`
}

type BrokerAuthenticateResponse struct {
	DaemonEpoch string `json:"daemonEpoch"`
}
