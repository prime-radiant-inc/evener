package sshconn

import (
	"context"
	"fmt"
	"path"
	"strconv"
	"strings"

	"primeradiant.com/evener/cmd/evener-hub/internal/hostreg"
	"primeradiant.com/evener/execsupport/shellquote"
)

// Account spellings are selectors, not distinct users. Equal selectors already
// identify the same account, while differing names/UIDs need owning-host lookup.
func (m *Manager) sameUser(ctx context.Context, host hostreg.Host, a, b string) (bool, error) {
	if a != "" && a == b {
		return true, nil
	}
	resolve := func(value string) (uint64, error) {
		if !isNumericUID(value) {
			out, err := m.ownerRead(ctx, host, "id -u "+shellquote.RemoteWord(value))
			if err != nil {
				return 0, err
			}
			value = strings.TrimSpace(string(out))
		}
		uid, err := strconv.ParseUint(value, 10, 32)
		if err != nil {
			return 0, fmt.Errorf("%w: user UID unavailable", errOwnerEvidence)
		}
		return uid, nil
	}
	aUID, err := resolve(a)
	if err != nil {
		return false, err
	}
	bUID, err := resolve(b)
	if err != nil {
		return false, err
	}
	return aUID == bUID, nil
}

// Resolve on the owning host, before any lexical cleaning. Keep the configured
// cwd separately as launch intent, including directory symlinks and symlink/...
func (m *Manager) directoryIdentity(ctx context.Context, host hostreg.Host, cwd string) (string, error) {
	if !path.IsAbs(cwd) {
		return "", fmt.Errorf("%w: working directory unavailable", errOwnerEvidence)
	}
	out, err := m.ownerRead(ctx, host, "CDPATH='' cd -P -- "+shellquote.RemoteWord(cwd)+" && pwd -P")
	if err != nil {
		return "", err
	}
	return ownerPathOutput(out)
}

// Unix pathname commands add one LF, not whitespace padding. Every other byte
// belongs to the pathname, including spaces, tabs and CR. Multi-line and NUL
// output cannot unambiguously identify one pathname and must not be normalized.
func ownerPathOutput(out []byte) (string, error) {
	value, err := commandPathOutput(out)
	if err != nil || !path.IsAbs(value) {
		return "", fmt.Errorf("%w: single absolute pathname unavailable", errOwnerEvidence)
	}
	return value, nil
}

func commandPathOutput(out []byte) (string, error) {
	value, framed := strings.CutSuffix(string(out), "\n")
	if !framed || value == "" || strings.ContainsAny(value, "\n\x00") {
		return "", fmt.Errorf("%w: single pathname unavailable", errOwnerEvidence)
	}
	return value, nil
}

func (m *Manager) sameDirectory(ctx context.Context, host hostreg.Host, a, b string) (bool, error) {
	if a == b && path.IsAbs(a) {
		return true, nil
	}
	aPhysical, err := m.directoryIdentity(ctx, host, a)
	if err != nil {
		return false, err
	}
	bPhysical, err := m.directoryIdentity(ctx, host, b)
	if err != nil {
		return false, err
	}
	return aPhysical == bPhysical, nil
}
