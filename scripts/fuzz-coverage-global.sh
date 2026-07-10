#!/usr/bin/env bash
# fuzz-coverage-global.sh replays the registry-audited deterministic fuzz corpus
# into one canonical self-coverage profile per production package, then delegates
# strict whole-module accounting to cmd/serf-fuzzcov.
#
# This deliberately does not run `go test ./... -coverpkg=./...`. Every local
# fuzz surface runs in its owning package only, so a package's profile has the
# Go toolchain's canonical block boundaries. Target profiles are unioned only
# within that declared module/package before global accounting sees them.
#
# The default invocation covers every go.work module:
#   . agent auth envvars fuzz invariant llm
#
# The target plan is obtained only from fuzz-registry-check.sh. A registry drift
# failure, a package without a local registered fuzz surface, a replay failure,
# or a missing/malformed profile is fatal; no result is fabricated or omitted.
#
# Usage:
#   scripts/fuzz-coverage-global.sh
#   scripts/fuzz-coverage-global.sh --check
#   scripts/fuzz-coverage-global.sh --bless
#   scripts/fuzz-coverage-global.sh --modules agent
#   scripts/fuzz-coverage-global.sh --format json
#
# Test seams:
#   SERF_FUZZ_GO              go executable (default: go)
#   SERF_FUZZ_CAPPED          command wrapper (default: scripts/run-capped.sh)
#   SERF_FUZZ_REGISTRY_CHECK  executable registry checker (default:
#                             scripts/fuzz-registry-check.sh)
set -euo pipefail

repo_root="$(cd "$(dirname "$0")/.." && pwd)"
go_work="$repo_root/go.work"
go_bin="${SERF_FUZZ_GO:-go}"
capped="${SERF_FUZZ_CAPPED:-$repo_root/scripts/run-capped.sh}"
registry_check="${SERF_FUZZ_REGISTRY_CHECK:-$repo_root/scripts/fuzz-registry-check.sh}"
exclusions_file="$repo_root/scripts/fuzzcov-global-exclusions.txt"
floors_file="$repo_root/scripts/fuzzcov-global-floors.txt"

# Keep these literals in go.work order. An explicit --modules selection is useful
# while a module is being raised, but the default contract is the full workspace.
workspace_modules=(. agent auth envvars fuzz invariant llm)
selected_modules=("${workspace_modules[@]}")
rapid_seeds=(1 2 3 5 8)
rapid_checks=100
check=false
bless=false
format=text

die() {
	echo "fuzz-coverage-global: $*" >&2
	exit 1
}

usage() {
	sed -n '2,31p' "$0" | sed 's/^# \{0,1\}//'
}

is_workspace_module() {
	local candidate="$1" module
	for module in "${workspace_modules[@]}"; do
		[ "$candidate" = "$module" ] && return 0
	done
	return 1
}

parse_modules() {
	local words="$1" module
	read -r -a selected_modules <<<"$words"
	[ "${#selected_modules[@]}" -gt 0 ] || die "--modules must name at least one go.work module"
	declare -A seen=()
	for module in "${selected_modules[@]}"; do
		is_workspace_module "$module" || die "unknown go.work module: $module"
		[ -z "${seen[$module]+x}" ] || die "duplicate module: $module"
		seen[$module]=1
	done
}

while [ "$#" -gt 0 ]; do
	case "$1" in
		--check) check=true; shift ;;
		--bless) bless=true; shift ;;
		--modules)
			[ "$#" -ge 2 ] || die "--modules requires a space-separated module list"
			parse_modules "$2"
			shift 2
			;;
		--modules=*) parse_modules "${1#*=}"; shift ;;
		--format)
			[ "$#" -ge 2 ] || die "--format requires text or json"
			format="$2"
			shift 2
			;;
		--format=*) format="${1#*=}"; shift ;;
		-h|--help) usage; exit 0 ;;
		*) die "unknown argument: $1" ;;
	esac
done

case "$format" in
	text|json) ;;
	*) die "--format must be text or json" ;;
esac

[ -f "$go_work" ] || die "missing go.work: $go_work"
[ -f "$exclusions_file" ] || die "missing exclusions manifest: $exclusions_file"
[ -f "$floors_file" ] || die "missing floors manifest: $floors_file"

# Every Go command, including the registry checker that internally invokes Go,
# is pinned to this workspace rather than ambient developer GOWORK state.
export GOWORK="$go_work"

work="$(mktemp -d -t serf-fuzzcov-global.XXXXXX)"
trap 'rm -rf "$work"' EXIT
plan="$work/targets.tsv"
groups="$work/groups.tsv"
global_manifest="$work/global-profiles.tsv"

echo "fuzz-coverage-global: validating registered native and Rapid targets"
if ! "$registry_check" >"$plan"; then
	die "registry check failed; replay did not begin"
fi

# The registry checker promises this exact headerless schema. Validate it here
# too, because treating a malformed plan as a partial plan would silently drop
# surfaces from the denominator.
if ! awk -F '\t' '
	NF != 4 || $1 == "" || $2 == "" || $3 == "" || $4 == "" {
		printf "fuzz-coverage-global: invalid registry replay row %d: %s\\n", NR, $0 > "/dev/stderr"
		bad = 1
	}
	END { exit bad }
' "$plan"; then
	die "registry checker did not emit exact four-column TSV"
fi
[ -s "$plan" ] || die "registry checker emitted no coverage targets"

declare -A surface=()
declare -A target=()
tab=$'\t'
while IFS=$'\t' read -r kind module pkg name; do
	case "$kind" in
		native|rapid) ;;
		*) die "registry plan has unsupported target kind: $kind" ;;
	esac
	is_workspace_module "$module" || die "registry plan names unknown module: $module"
	case "$pkg" in
		.) ;;
		./*) ;;
		*) die "registry plan has non-relative package path: $module:$pkg" ;;
	esac
	case "$pkg" in
		./|../*|*/../*|*/..) die "registry plan has unsafe package path: $module:$pkg" ;;
	esac
	[[ "$name" =~ ^[A-Za-z_][A-Za-z0-9_]*$ ]] || die "registry plan has invalid target name: $module:$pkg:$name"
	case "$kind:$name" in
		native:Fuzz*) ;;
		rapid:Test*) ;;
		*) die "registry plan has invalid $kind target name: $module:$pkg:$name" ;;
	esac
	identity="$kind$tab$module$tab$pkg$tab$name"
	[ -z "${target[$identity]+x}" ] || die "registry plan duplicates target: $kind:$module:$pkg:$name"
	target[$identity]=1
	surface["$module$tab$pkg"]=1
done <"$plan"

declare -A selected=()
for module in "${selected_modules[@]}"; do
	selected[$module]=1
done

# Preflight every production package before running one replay. This avoids Go
# 1.25's no-test-binary covdata path and gives a complete actionable list rather
# than discovering missing local surfaces one package at a time mid-measurement.
declare -A production_package=()
preflight_failed=false
for module in "${selected_modules[@]}"; do
	module_dir="$repo_root/$module"
	[ -d "$module_dir" ] || die "missing module directory: $module_dir"
	module_dir="$(cd "$module_dir" && pwd)"
	list_file="$work/packages-${module//\//_}.txt"
	if ! (cd "$module_dir" && "$go_bin" list -tags serffuzz -f '{{.Dir}}' ./...) >"$list_file"; then
		die "go list failed for module: $module"
	fi
	while IFS= read -r package_dir; do
		[ -n "$package_dir" ] || continue
		if [ "$package_dir" = "$module_dir" ]; then
			pkg=.
		elif [[ "$package_dir" == "$module_dir/"* ]]; then
			pkg="./${package_dir#"$module_dir/"}"
		else
			die "go list returned package outside module $module: $package_dir"
		fi
		production_package["$module$tab$pkg"]=1
		if [ -z "${surface["$module$tab$pkg"]+x}" ]; then
			echo "missing local fuzz surface: $module:$pkg" >&2
			preflight_failed=true
		fi
	done <"$list_file"
done

# A plan row for a non-production package is just as unsafe as a missing row:
# it would otherwise be replayed outside the exact denominator we preflighted.
for key in "${!surface[@]}"; do
	module="${key%%$tab*}"
	pkg="${key#*$tab}"
	[ -n "${selected[$module]+x}" ] || continue
	if [ -z "${production_package[$key]+x}" ]; then
		echo "registered local fuzz surface is not a production package: $module:$pkg" >&2
		preflight_failed=true
	fi
done

if [ "$preflight_failed" = true ]; then
	die "local fuzz surface preflight failed; replay did not begin"
fi

# Select and sort the package groups after preflight. A package can own many
# native/Rapid targets, but it contributes exactly one merged profile below.
: >"$groups"
while IFS=$'\t' read -r kind module pkg name; do
	[ -n "${selected[$module]+x}" ] || continue
	printf '%s\t%s\n' "$module" "$pkg" >>"$groups"
done <"$plan"
LC_ALL=C sort -u "$groups" >"$groups.sorted"
mv "$groups.sorted" "$groups"
[ -s "$groups" ] || die "no registered coverage targets for selected modules"

validate_profile() {
	local profile="$1" label="$2" header
	[ -s "$profile" ] || die "coverage profile missing after replay: $label"
	header="$(sed -n '1p' "$profile")"
	[ "$header" = "mode: set" ] || die "coverage profile has invalid mode after replay: $label"
}

merge_profiles() {
	local out="$1"
	shift
	[ "$#" -gt 0 ] || die "internal error: no profiles to merge for $out"
	local blocks="$out.blocks"
	if ! awk '
		FNR == 1 {
			if ($0 != "mode: set") {
				printf "invalid coverage mode in %s: %s\\n", FILENAME, $0 > "/dev/stderr"
				bad = 1
			}
			next
		}
		NF != 3 || $2 !~ /^[1-9][0-9]*$/ || $3 !~ /^[0-9]+$/ {
			printf "invalid coverage block in %s: %s\\n", FILENAME, $0 > "/dev/stderr"
			bad = 1
			next
		}
		{
			key = $1 " " $2
			if (!(key in count) || $3 > count[key]) {
				count[key] = $3
			}
		}
		END {
			for (key in count) {
				print key " " count[key]
			}
			exit bad
		}
	' "$@" >"$blocks"; then
		rm -f "$blocks"
		die "cannot merge package-local coverage profiles: $out"
	fi
	{
		echo "mode: set"
		LC_ALL=C sort "$blocks"
	} >"$out"
	rm -f "$blocks"
	validate_profile "$out" "merged package profile $out"
}

replay_target() {
	local kind="$1" module="$2" pkg="$3" name="$4" seed="${5:-}" profile="$6"
	local label="$kind:$module:$pkg:$name"
	[ -z "$seed" ] || label="$label seed=$seed"
	echo "fuzz-coverage-global: replay $label"
	if [ "$kind" = rapid ]; then
		if ! (cd "$repo_root/$module" && RAPID_SEED="$seed" RAPID_CHECKS="$rapid_checks" \
			"$capped" "$go_bin" test -tags serffuzz -run "^$name\$" -count=1 -coverprofile="$profile" "$pkg"); then
			die "replay failed: $label"
		fi
	else
		if ! (cd "$repo_root/$module" && \
			"$capped" "$go_bin" test -tags serffuzz -run "^$name\$" -count=1 -coverprofile="$profile" "$pkg"); then
			die "replay failed: $label"
		fi
	fi
	validate_profile "$profile" "$label"
}

: >"$global_manifest"
while IFS=$'\t' read -r module pkg; do
	package_profiles=()
	while IFS=$'\t' read -r kind row_module row_pkg name; do
		[ "$row_module" = "$module" ] && [ "$row_pkg" = "$pkg" ] || continue
		if [ "$kind" = native ]; then
			profile="$(mktemp "$work/target.XXXXXX.cov")"
			rm -f "$profile"
			replay_target "$kind" "$module" "$pkg" "$name" "" "$profile"
			package_profiles+=("$profile")
		else
			for seed in "${rapid_seeds[@]}"; do
				profile="$(mktemp "$work/target.XXXXXX.cov")"
				rm -f "$profile"
				replay_target "$kind" "$module" "$pkg" "$name" "$seed" "$profile"
				package_profiles+=("$profile")
			done
		fi
	done <"$plan"
	package_profile="$(mktemp "$work/package.XXXXXX.cov")"
	rm -f "$package_profile"
	merge_profiles "$package_profile" "${package_profiles[@]}"
	printf '%s\t%s\t%s\n' "$module" "$pkg" "$package_profile" >>"$global_manifest"
done <"$groups"

[ -s "$global_manifest" ] || die "internal error: no package profiles were produced"

fuzzcov_args=(
	run ./cmd/serf-fuzzcov
	-global-manifest "$global_manifest"
	-repo-root "$repo_root"
	-global-exclusions "$exclusions_file"
	-global-floors "$floors_file"
	-global-minimum 95
)
[ "$format" = json ] && fuzzcov_args+=(-global-json)
"$check" && fuzzcov_args+=(-check)
"$bless" && fuzzcov_args+=(-bless)

echo "fuzz-coverage-global: account package-local profiles"
if ! (cd "$repo_root" && "$capped" "$go_bin" "${fuzzcov_args[@]}"); then
	die "global coverage accounting failed"
fi
