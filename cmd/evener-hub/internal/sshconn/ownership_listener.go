package sshconn

import (
	"net"
	"slices"
	"strings"

	"primeradiant.com/evener/internal/hubendpoint"
)

// supervisorArgvMatches admits only hubRestartArgs' port-zero rewrite. It does
// not change either snapshot: live argv and the supervisor's next launch differ.
func supervisorArgvMatches(live, definition []string, report hubendpoint.Report) bool {
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
		if !configured && !published {
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
