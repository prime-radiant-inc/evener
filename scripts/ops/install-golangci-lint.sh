#!/usr/bin/env bash
# install-golangci-lint.sh — install the .tool-versions-pinned golangci-lint
# into $(go env GOPATH)/bin, retrying a download that failed in transit.
#
# One HTTP 500 from the release CDN failed the whole lint-golangci job, and with
# it the aggregate `static` job (GitHub run 34644540860), on a tree whose code
# was clean. make/repo.mk piped the upstream installer straight into `sh`, and
# neither end had a retry.
#
# There are two downloads and they retry differently. Ours -- the fetch of
# install.sh -- is curl's own --retry, which is bounded, backs off, and needs no
# loop here. The installer's own download of the release asset is where that 500
# happened, and its http_download_curl returns 1 for any non-200 with nothing to
# ask it for another attempt (its options are -b, -d, -h/-?, -x and the tag), so
# retrying it means running the installer again. Hence the loop below.
#
# The retry deliberately does not classify the failure: the installer collapses
# every cause to exit 1, and telling a 5xx from a bad pin would mean matching its
# log wording. A genuinely broken pin costs the attempts plus their backoff and
# then fails with the installer's own diagnostics on stderr.
#
# Neither download may hang. Ours carries curl's --connect-timeout 15 (a CDN
# edge that accepts nothing is the failure this whole script is about, and 15s
# is long past a healthy TLS handshake) and --max-time 300 (the installer
# script is a few tens of KB; five minutes is a stall, not slowness). The
# installer's own download has no such option, so the whole invocation runs
# under `evener-dev dev bounded-list` when that binary is present, which bounds
# it and stops its process group rather than leaving a wedged curl behind.
#
# Residual: `make tools-golangci` on a fresh clone runs before anything is
# built, so the binary is usually absent there and the installer's download is
# unbounded on that path. It says so on stderr when it takes it.
#
# And a second, temporary one: `bounded-list` is not on main yet. It arrives
# with #1263, so until that lands every run takes the unbounded path, including
# CI's -- the probe below asks the binary to run a trivial command under the
# subcommand and believes the answer, so a binary without it is simply a binary
# this script does not use.
#
# The pinned version is the one in .tool-versions and is never chosen here.
# There is no Go-toolchain fallback: docs/developing-evener/linting.md makes
# `make tools` the install path and a missing golangci-lint a hard gate failure.
#
# Usage:
#   scripts/ops/install-golangci-lint.sh
#   EVENER_GOLANGCI_INSTALL_ATTEMPTS=1 scripts/ops/install-golangci-lint.sh
#
# EVENER_GOLANGCI_INSTALL_ATTEMPTS is the total number of attempts (default 3, a
# positive integer). Backoff between them is EVENER_GOLANGCI_INSTALL_BACKOFF
# seconds times the attempt number (default 5, so 5s then 10s), and each
# attempt's own fetch retries EVENER_GOLANGCI_CURL_RETRIES times (default 3),
# EVENER_GOLANGCI_CURL_RETRY_DELAY seconds apart (default 2).
#
# EVENER_GOLANGCI_INSTALLER_URL is where install.sh is fetched from (default the
# upstream raw URL), and EVENER_GOLANGCI_DEV_BIN is the evener-dev binary this
# script looks for (default the one at the repo root). Both are here so the
# paths below can be tested with real curl and real bash against a loopback
# server, with no network and no stubbed binaries; the tests point
# EVENER_GOLANGCI_DEV_BIN at a path that does not exist, so which path they
# exercise does not depend on what happens to be built in the worktree. The
# three numeric knobs exist for the same reason -- a test that waited out the
# default retries and backoff would be a slow test proving nothing extra -- and
# a slow link is welcome to raise them.
#
# The bounded path has no test yet, because bounded-list is not on main. When
# #1263 lands, the test that belongs here builds the real binary and runs the
# install under it.
set -euo pipefail

installer_url="${EVENER_GOLANGCI_INSTALLER_URL:-https://raw.githubusercontent.com/golangci/golangci-lint/HEAD/install.sh}"

script_dir="$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)"
repo_root="$(CDPATH='' cd -- "$script_dir/../.." && pwd)"

# The knobs are bounded, and the bound is in the pattern rather than in an
# arithmetic comparison: a twenty-digit value is not a number bash can compare,
# so a check written as arithmetic would fail as arithmetic rather than as the
# diagnostic the caller needs. Twenty is far past any real use -- three
# attempts is the default and CI's lint lane is one download.
attempts=${EVENER_GOLANGCI_INSTALL_ATTEMPTS:-3}
if [[ ! "$attempts" =~ ^([1-9]|1[0-9]|20)$ ]]; then
	printf 'install-golangci-lint.sh: EVENER_GOLANGCI_INSTALL_ATTEMPTS must be a whole number from 1 to 20, without a leading zero (got %q)\n' "$attempts" >&2
	exit 2
fi

# The leading-zero refusal is not pedantry: bash arithmetic reads 08 as octal
# and fails, so a value that passed a looser check would break the backoff
# itself rather than the validation.
backoff=${EVENER_GOLANGCI_INSTALL_BACKOFF:-5}
if [[ ! "$backoff" =~ ^(0|[1-9]|1[0-9]|20)$ ]]; then
	printf 'install-golangci-lint.sh: EVENER_GOLANGCI_INSTALL_BACKOFF must be a whole number of seconds from 0 to 20, without a leading zero (got %q)\n' "$backoff" >&2
	exit 2
fi

curl_retries=${EVENER_GOLANGCI_CURL_RETRIES:-3}
if [[ ! "$curl_retries" =~ ^(0|[1-9]|1[0-9]|20)$ ]]; then
	printf 'install-golangci-lint.sh: EVENER_GOLANGCI_CURL_RETRIES must be a whole number from 0 to 20, without a leading zero (got %q)\n' "$curl_retries" >&2
	exit 2
fi

curl_retry_delay=${EVENER_GOLANGCI_CURL_RETRY_DELAY:-2}
if [[ ! "$curl_retry_delay" =~ ^(0|[1-9]|1[0-9]|20)$ ]]; then
	printf 'install-golangci-lint.sh: EVENER_GOLANGCI_CURL_RETRY_DELAY must be a whole number of seconds from 0 to 20, without a leading zero (got %q)\n' "$curl_retry_delay" >&2
	exit 2
fi

if ! version="$(awk '$1=="golangci-lint" {print $2}' "$repo_root/.tool-versions")"; then
	printf 'install-golangci-lint.sh: could not read %s\n' "$repo_root/.tool-versions" >&2
	exit 2
fi
if [ -z "$version" ]; then
	printf 'install-golangci-lint.sh: no golangci-lint row in %s\n' "$repo_root/.tool-versions" >&2
	exit 1
fi

# --retry-all-errors is what makes an HTTP status a failure worth retrying, and
# it arrived in curl 7.71. An older curl would accept the flag's absence
# silently in some builds and fail outright in others; either way the retry
# this script is built around would not be the retry it describes. There is no
# fallback: a retry reimplemented here would be the shell supervisor this whole
# change exists to avoid.
if ! command -v curl >/dev/null 2>&1; then
	printf 'install-golangci-lint.sh: curl is not on PATH, and this script has nothing else to download with\n' >&2
	exit 2
fi
if ! curl_version="$(curl --version 2>/dev/null | awk 'NR==1 {print $2}')"; then
	printf 'install-golangci-lint.sh: `curl --version` did not run\n' >&2
	exit 2
fi
curl_major="${curl_version%%.*}"
curl_rest="${curl_version#*.}"
curl_minor="${curl_rest%%.*}"
if [[ ! "$curl_major" =~ ^[0-9]+$ ]] || [[ ! "$curl_minor" =~ ^[0-9]+$ ]]; then
	printf 'install-golangci-lint.sh: could not read a version out of `curl --version` (got %q); curl 7.71 or newer is required for --retry-all-errors\n' \
		"$curl_version" >&2
	exit 2
fi
if [ "$curl_major" -lt 7 ] || { [ "$curl_major" -eq 7 ] && [ "$curl_minor" -lt 71 ]; }; then
	printf 'install-golangci-lint.sh: curl %s is too old; --retry-all-errors needs curl 7.71 or newer\n' \
		"$curl_version" >&2
	exit 2
fi

if ! gopath="$(go env GOPATH)" || [ -z "$gopath" ]; then
	printf 'install-golangci-lint.sh: `go env GOPATH` did not answer, so there is nowhere to install to\n' >&2
	exit 2
fi
bindir="$gopath/bin"

# --retry-all-errors is what makes --retry cover a failure that is not a
# connection breaking: an HTTP status, or a reply that ended early.
#
# The installer is downloaded to a file and only then run, and the same command
# serves both paths so they cannot drift apart. Piping curl into sh hands the
# shell whatever arrived: a response cut off halfway is a script the shell has
# already started executing, and curl's retry then appends the second response
# to the first, so the shell runs the truncated half and then the whole thing.
# A file has no halfway state -- curl either exits 0 with the body or it does
# not, and sh sees the file only in the first case. That also retires the
# pipefail form, which was there to notice the download failing on the left of
# a pipe.
# Armed before the file exists: a failure between the two would otherwise leave
# whatever mktemp had managed to create.
trap 'rm -f "${install_script:-}"' EXIT
install_script="$(mktemp "${TMPDIR:-/tmp}/install-golangci-lint.XXXXXX")" || {
	printf 'install-golangci-lint.sh: could not make a temporary file for the installer\n' >&2
	exit 2
}

fetch_and_run='curl -fsSL --connect-timeout 15 --max-time 300 --retry "$4" --retry-delay "$6" --retry-all-errors -o "$5" "$1" && sh "$5" -b "$2" "$3"'

evener_dev_bin="${EVENER_GOLANGCI_DEV_BIN:-$repo_root/evener-dev}"
bounded_list_support=
announced_unbounded=0

# bounded_list_available answers once per run: the binary is there and its
# bounded-list runs a trivial command. Asking is the only honest test -- the
# subcommand arrives with #1263 and this script is on main before it.
bounded_list_available() {
	case "$bounded_list_support" in
	yes) return 0 ;;
	no) return 1 ;;
	esac
	if [ -x "$evener_dev_bin" ] && "$evener_dev_bin" dev bounded-list -timeout 10s -attempts 1 -- true >/dev/null 2>&1; then
		bounded_list_support=yes
		return 0
	fi
	bounded_list_support=no
	return 1
}

run_installer() {
	if bounded_list_available; then
		"$evener_dev_bin" dev bounded-list -timeout 300s -attempts 1 -grace 5s -- \
			sh -c "$fetch_and_run" install-golangci-lint "$installer_url" "$bindir" "v$version" "$curl_retries" "$install_script" "$curl_retry_delay"
		return
	fi
	if [ "$announced_unbounded" -eq 0 ]; then
		printf 'install-golangci-lint.sh: %s cannot bound this download (not built, or without bounded-list, which arrives with #1263), so the installer runs unbounded.\n' \
			"$evener_dev_bin" >&2
		announced_unbounded=1
	fi
	sh -c "$fetch_and_run" install-golangci-lint "$installer_url" "$bindir" "v$version" "$curl_retries" "$install_script" "$curl_retry_delay"
}

attempt=1
while :; do
	if run_installer; then
		break
	fi
	if [ "$attempt" -ge "$attempts" ]; then
		printf 'install-golangci-lint.sh: golangci-lint v%s did not install in %s attempt(s); the installer diagnostics are above.\n' \
			"$version" "$attempts" >&2
		exit 1
	fi
	delay=$((attempt * backoff))
	printf 'install-golangci-lint.sh: install attempt %s of %s failed; retrying in %ss.\n' \
		"$attempt" "$attempts" "$delay" >&2
	sleep "$delay"
	attempt=$((attempt + 1))
done

# Prove the pin actually landed rather than trusting the exit status: an
# installer that wrote a different release, or a bindir already holding an older
# binary, would otherwise reach the lint gate as a version mismatch nobody
# attributed to this step.
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
