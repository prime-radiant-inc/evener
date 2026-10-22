package sshconn

import (
	"context"
	"net"
	"slices"
	"strconv"
	"strings"

	"primeradiant.com/evener/cmd/evener-hub/internal/hostreg"
	"primeradiant.com/evener/internal/hubendpoint"
)

// A missing health report cannot invent a bound listener. Borrow that evidence
// only from the same registered process and exact previously verified rewrite.
// The caller still revalidates cwd, supervisor definition and config/root.
func (m *Manager) supervisorArgvMatches(ctx context.Context, host hostreg.Host, pid string, live, definition []string, report hubendpoint.Report, env []string, darwin bool) bool {
	if report.Running != nil || slices.Equal(live, definition) {
		return supervisorArgvMatches(live, definition, report, env)
	}
	m.mu.Lock()
	prior, ok := m.ownerWitnesses[host.Name]
	prior = prior.clone()
	m.mu.Unlock()
	if !ok || prior.report.Running == nil || prior.supervisor.kind == supervisorNone || prior.pid != pid || strconv.Itoa(prior.report.Running.PID) != pid || !host.Equal(prior.host) || host.Generation != prior.host.Generation || host.IncarnationID != prior.host.IncarnationID || host.PresenceEpoch != prior.host.PresenceEpoch || report.ConfigPath != prior.report.ConfigPath || report.HubStateRoot != prior.report.HubStateRoot || !slices.Equal(live, prior.argv) || !slices.Equal(definition, prior.launchArguments()) || !slices.Equal(env, prior.env) {
		return false
	}
	var generation string
	var err error
	if darwin {
		generation, err = m.darwinGeneration(ctx, host, pid)
	} else {
		generation, err = m.ownerGeneration(ctx, host, pid)
	}
	return err == nil && generation == prior.generation && supervisorArgvMatches(live, definition, prior.report, env)
}

// supervisorArgvMatches admits only hubRestartArgs' port-zero rewrite. It does
// not change either snapshot: live argv and the supervisor's next launch differ.
func supervisorArgvMatches(live, definition []string, report hubendpoint.Report, env []string) bool {
	if slices.Equal(live, definition) {
		return true
	}
	if report.Running == nil {
		return false
	}
	bound := report.Running.BoundAddr
	if _, err := hubendpoint.LocalOrigin(bound); err != nil {
		return false
	}
	ephemeral := func(addr string) bool {
		_, port, err := net.SplitHostPort(addr)
		return err == nil && (port == "" || port == "0")
	}
	expected := slices.Clone(definition)
	index, equals := -1, false
	for i := 2; i < len(definition); i++ {
		arg := definition[i]
		flag, value, inline := strings.Cut(arg, "=")
		if flag != "-addr" && flag != "--addr" {
			continue
		}
		if index != -1 {
			return false
		}
		index, equals = i, inline
		if !inline {
			i++
			if i == len(definition) {
				return false
			}
			value = definition[i]
		}
		if !ephemeral(value) {
			return false
		}
	}
	if index == -1 {
		configured := report.ListenSource == "config" && ephemeral(report.ConfiguredListenAddr)
		published := report.Running.ListenSource == "config" && ephemeral(report.Running.RequestedListenAddr)
		fromEnv := false
		for _, value := range env {
			addr, ok := strings.CutPrefix(value, "EVENER_HUB_LISTEN_ADDR=")
			if ok && ephemeral(addr) {
				// Captured process/service environment proves the original launch.
				// SSH inspection may lack it, and the rewritten record names a flag.
				fromEnv = true
			}
		}
		if !configured && !published && !fromEnv {
			return false
		}
		expected = append(expected, "-addr", bound)
	} else if equals {
		flag, _, _ := strings.Cut(definition[index], "=")
		expected[index] = flag + "=" + bound
	} else {
		expected[index+1] = bound
	}
	return slices.Equal(live, expected)
}
