#!/usr/bin/env bash
# go-module-consumer-check.sh — prove each workspace module's go.mod is a
# complete statement of what its own packages need, from a consumer outside the
# workspace.
#
# The check builds a scratch consumer whose go.mod requires just the module
# under test, plus a directory replace for every workspace module, and then
# runs the documented readonly probe:
#
#   GOWORK=off GOFLAGS= GOPROXY=off go list -mod=readonly -deps <packages>
#
# Two things make that probe answer the right question:
#
#   - The packages are the module's own, enumerated with `go list -e ./...`
#     from the module directory. `primeradiant.com/evener/...` is not the root
#     module's package set: every sibling module's import path has that prefix,
#     so the wildcard enumerates the siblings too and fails on requirements the
#     root module does not own. `./...` prunes nested modules, and -e keeps a
#     package listed even when its imports cannot resolve yet.
#
#   - The consumer's go.sum is the union of the workspace modules' go.sum files
#     (and go.work.sum when there is one). A consumer with no sums dies on
#     "missing go.sum entry" before it can report a missing requirement, which
#     is a consumer setup problem, not a module defect. Preparing the sums does
#     not add a requirement or run -mod=mod on anything.
#
# Failure classes, so a missing requirement is never blamed on the consumer:
#
#   missing module requirement (exit 1): the readonly probe says "no required
#   module provides package", "cannot find module providing package", or a
#   replaced module "is replaced but not required". Add the indirect to the
#   module's own go.mod at the version the workspace selects (`go list -m
#   <module>` from the workspace root).
#
#   consumer setup (exit 2): the probe cannot get far enough to judge the
#   module. A "missing go.sum entry" means the sum union did not cover a module
#   the consumer graph selected; "module lookup disabled by GOPROXY=off" means
#   the selected version is not in the module cache (often a pin that is stale
#   against the workspace's selection); a replace, go.mod read or package
#   enumeration failure means the scratch consumer was built wrong. Setup is
#   reported ahead of a requirement gap because a cache or sum miss also makes
#   the packages that need the missing module look absent.
#
# Usage:
#   scripts/gate/go-module-consumer-check.sh [module-dir...]
#
# With no arguments every module in the workspace's go.work is checked. Named
# directories are the only modules checked, but every workspace module is still
# replaced in their consumers. Run it inside the workspace checkout: package
# enumeration needs the workspace, while the consumer probe itself does not.
#
# Exit status: 0 every module passed, 1 a module is missing a requirement,
# 2 a consumer setup problem or usage error (also when both occur).
set -uo pipefail

script_dir="$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)"
repo_root="$(CDPATH='' cd -- "$script_dir/../.." && pwd)"
# The scratch directory is minted and reclaimed by the repository's one
# helper, never by a recursive delete that takes a caller's variable.
. "$repo_root/scripts/lib/scratch-lib.sh"

die() {
	printf 'go-module-consumer-check: %s\n' "$*" >&2
	exit 2
}

workfile="$(cd "$repo_root" && go env GOWORK 2>/dev/null)" || workfile=""
if [ ! -f "$workfile" ]; then
	die "no go.work; run this from the workspace checkout (or set GOWORK)"
fi
workdir="$(dirname -- "$workfile")"
workspace_dirs=()
while IFS= read -r disk; do
	case "$disk" in
	/*) workspace_dirs+=("$disk") ;;
	*) workspace_dirs+=("$workdir/$disk") ;;
	esac
done < <(cd "$repo_root" && go work edit -json | sed -n 's/.*"DiskPath": "\([^"]*\)".*/\1/p')

if [ "$#" -gt 0 ]; then
	target_dirs=("$@")
else
	[ "${#workspace_dirs[@]}" -gt 0 ] || die "no go.work to enumerate modules from; pass module directories explicitly"
	target_dirs=("${workspace_dirs[@]}")
fi

trap scratch_rm EXIT
scratch_dir tmp evener-consumer-check

# Resolve every target directory, and every workspace module that its consumer
# must replace, once. Directories are keyed by their absolute path.
declare -A dir_seen=()
dirs=()
resolved=""
resolve_dir() {
	local dir
	dir="$(CDPATH='' cd -- "$1" && pwd)" || die "module directory does not exist: $1"
	if [ -z "${dir_seen[$dir]:-}" ]; then
		dir_seen[$dir]=1
		dirs+=("$dir")
	fi
	resolved="$dir"
}

resolved_targets=()
for dir in "${target_dirs[@]}"; do
	resolve_dir "$dir"
	resolved_targets+=("$resolved")
done
for dir in "${workspace_dirs[@]}"; do
	resolve_dir "$dir"
done

declare -A path_of=()
declare -A path_dir=()
for dir in "${dirs[@]}"; do
	path="$(cd "$dir" && GOWORK=off GOFLAGS= go list -m -f '{{.Path}}' 2>/dev/null)" || die "$dir: cannot read the module path from go.mod"
	[ -n "$path" ] || die "$dir: go.mod names no module path"
	if [ -n "${path_dir[$path]:-}" ]; then
		die "duplicate module path $path in ${path_dir[$path]} and $dir"
	fi
	path_of[$dir]="$path"
	path_dir[$path]="$dir"
done

# The consumer's sum must cover every external module its graph can select. The
# per-module sums are incomplete alone (a workspace fills the gaps from its
# siblings), so use the union, plus go.work.sum when a workspace file is known.
sums="$tmp/go.sum"
sums_raw="$tmp/go.sum.raw"
: >"$sums_raw"
for dir in "${dirs[@]}"; do
	if [ -f "$dir/go.sum" ]; then
		cat "$dir/go.sum" >>"$sums_raw" || die "$dir/go.sum is unreadable"
	fi
done
if [ -f "$workfile.sum" ]; then
	cat "$workfile.sum" >>"$sums_raw" || die "$workfile.sum is unreadable"
fi
sort -u "$sums_raw" >"$sums" || die "cannot sort the consumer go.sum"

gap_modules=0
setup_modules=0
for i in "${!resolved_targets[@]}"; do
	dir="${resolved_targets[$i]}"
	path="${path_of[$dir]}"
	consumer="$tmp/consumer-$i"
	mkdir -p "$consumer" || die "cannot create $consumer"

	go_version="$(awk '$1 == "go" { print $2; exit }' "$dir/go.mod")"
	if [ -z "$go_version" ]; then
		go_version="$(go env GOVERSION | sed 's/^go//')"
	fi
	{
		printf 'module evener-consumer-check\n\n'
		printf 'go %s\n\n' "$go_version"
		printf 'require %s v0.0.0\n\n' "$path"
		for replace_dir in "${dirs[@]}"; do
			printf 'replace %s v0.0.0 => %s\n' "${path_of[$replace_dir]}" "$replace_dir"
		done
	} >"$consumer/go.mod"
	cp "$sums" "$consumer/go.sum"

	mapfile -t packages < <(cd "$dir" && GOFLAGS= go list -e -f '{{.ImportPath}}' ./... 2>"$consumer/enum.err" | sed '/^$/d')
	# With -e, a pattern that matches no package lists the pattern itself
	# (./...), which a directory outside the workspace does; only the module's
	# own import paths may reach the probe.
	enumerated=1
	for pkg in "${packages[@]}"; do
		case "$pkg" in
		"$path" | "$path"/*) ;;
		*) enumerated=0 ;;
		esac
	done
	if [ "${#packages[@]}" -eq 0 ] || [ "$enumerated" -eq 0 ]; then
		printf 'FAIL  %s (consumer setup: cannot enumerate the module packages)\n' "$path" >&2
		sed 's/^/      /' "$consumer/enum.err" >&2
		setup_modules=$((setup_modules + 1))
		continue
	fi

	(
		cd "$consumer" || exit 2
		GOWORK=off GOFLAGS= GOPROXY=off go list -mod=readonly -deps "${packages[@]}"
	) >"$consumer/out" 2>"$consumer/err"
	status=$?
	if [ "$status" -eq 0 ]; then
		printf 'PASS  %s\n' "$path"
		continue
	fi

	gap_re='no required module provides package|is replaced but not required|cannot find module providing package'
	if grep -Eq 'missing go.sum entry|updates to go.mod needed|module lookup disabled|replacement directory|outside module root' "$consumer/err"; then
		printf 'FAIL  %s (consumer setup, not a module requirement)\n' "$path" >&2
		sed 's/^/      /' "$consumer/err" >&2
		if grep -Eq "$gap_re" "$consumer/err"; then
			printf '      (some packages also looked absent; rerun after the setup error is cleared to judge requirements)\n' >&2
		fi
		setup_modules=$((setup_modules + 1))
		continue
	fi
	if grep -Eq "$gap_re" "$consumer/err"; then
		printf 'FAIL  %s (missing module requirement)\n' "$path" >&2
		sed 's/^/      /' "$consumer/err" >&2
		gap_modules=$((gap_modules + 1))
		continue
	fi
	printf 'FAIL  %s (consumer setup, not a module requirement: unrecognised probe error)\n' "$path" >&2
	sed 's/^/      /' "$consumer/err" >&2
	setup_modules=$((setup_modules + 1))
done

if [ "$gap_modules" -ne 0 ] || [ "$setup_modules" -ne 0 ]; then
	printf 'go-module-consumer-check: %d module(s) with a missing requirement, %d module(s) with consumer setup\n' "$gap_modules" "$setup_modules" >&2
fi
if [ "$setup_modules" -ne 0 ]; then
	exit 2
fi
if [ "$gap_modules" -ne 0 ]; then
	exit 1
fi
exit 0
