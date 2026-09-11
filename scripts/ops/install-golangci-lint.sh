#!/usr/bin/env bash
# install-golangci-lint.sh — install the .tool-versions-pinned golangci-lint
# into $(go env GOPATH)/bin, retrying a download that failed in transit.
#
# The upstream installer has no retry of its own. Its http_download_curl
# returns 1 for any non-200 and the installer's `set -e` aborts there, and its
# only options are -b (bindir), -d (debug), -h/-? (usage), -x (xtrace) and the
# release tag — nothing to ask it for another attempt. So one 500 from the
# release CDN failed the whole lint-golangci job, and with it the aggregate
# `static` job (GitHub run 34644540860). The retry has to wrap the invocation.
#
# The retry is bounded and deliberately does not classify the failure. The
# installer collapses every cause to exit 1, so telling a 5xx from a bad pin
# would mean matching upstream's log wording — a coupling that breaks silently
# the next time it is reworded. A genuinely broken pin therefore costs the
# attempts below plus their backoff before it fails, and it still fails with
# the installer's own diagnostics on stderr.
#
# The pinned version is the one in .tool-versions and is never chosen here.
# There is no Go-toolchain fallback: docs/developing-evener/linting.md makes
# `make tools` the install path and a missing golangci-lint a hard gate
# failure, and this script does not invent a second source for it.
#
# Usage:
#   scripts/ops/install-golangci-lint.sh
#     EVENER_GOLANGCI_INSTALL_ATTEMPTS=1 scripts/ops/install-golangci-lint.sh
#
# EVENER_GOLANGCI_INSTALL_ATTEMPTS is the total number of attempts (default 3,
# a positive integer). Backoff between them is 5s, then 10s, and so on.
set -euo pipefail

installer_url='https://raw.githubusercontent.com/golangci/golangci-lint/HEAD/install.sh'

script_dir="$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)"
repo_root="$(CDPATH='' cd -- "$script_dir/../.." && pwd)"

attempts=${EVENER_GOLANGCI_INSTALL_ATTEMPTS:-3}
if [[ ! "$attempts" =~ ^[1-9][0-9]*$ ]]; then
	printf 'install-golangci-lint.sh: EVENER_GOLANGCI_INSTALL_ATTEMPTS must be a positive integer (got %q)\n' "$attempts" >&2
	exit 2
fi

version="$(awk '$1=="golangci-lint" {print $2}' "$repo_root/.tool-versions")"
if [ -z "$version" ]; then
	printf 'install-golangci-lint.sh: no golangci-lint row in %s\n' "$repo_root/.tool-versions" >&2
	exit 1
fi

bindir="$(go env GOPATH)/bin"

# pipefail is what makes the fetch of install.sh part of the attempt: without
# it a failed curl hands `sh` an empty script, which exits 0 and reports a
# successful install of nothing.
attempt=1
while :; do
	if curl -sSfL "$installer_url" | sh -s -- -b "$bindir" "v$version"; then
		break
	fi
	if [ "$attempt" -ge "$attempts" ]; then
		printf 'install-golangci-lint.sh: golangci-lint v%s did not install in %s attempt(s); the installer diagnostics are above.\n' \
			"$version" "$attempts" >&2
		exit 1
	fi
	delay=$((attempt * 5))
	printf 'install-golangci-lint.sh: install attempt %s of %s failed; retrying in %ss.\n' \
		"$attempt" "$attempts" "$delay" >&2
	sleep "$delay"
	attempt=$((attempt + 1))
done

# Prove the pin actually landed rather than trusting the exit status: an
# installer that wrote a different release, or a bindir already holding an
# older binary, would otherwise reach the lint gate as a version mismatch
# nobody attributed to this step.
if ! installed="$("$bindir/golangci-lint" version 2>&1)"; then
	printf 'install-golangci-lint.sh: %s/golangci-lint did not run after installation: %s\n' \
		"$bindir" "$installed" >&2
	exit 1
fi
case "$installed" in
*" version $version "*) ;;
*)
	printf 'install-golangci-lint.sh: %s/golangci-lint reports "%s", not the pinned v%s from %s\n' \
		"$bindir" "$installed" "$version" "$repo_root/.tool-versions" >&2
	exit 1
	;;
esac
