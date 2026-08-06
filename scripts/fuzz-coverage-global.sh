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
# The default invocation discovers and covers every repo-local go.work module.
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

repo_root="$(cd "$(dirname "$0")/.." && pwd -P)"
go_work="$repo_root/go.work"
go_bin="${SERF_FUZZ_GO:-go}"
capped="${SERF_FUZZ_CAPPED:-$repo_root/scripts/run-capped.sh}"
registry_check="${SERF_FUZZ_REGISTRY_CHECK:-$repo_root/scripts/fuzz-registry-check.sh}"
exclusions_file="$repo_root/scripts/fuzzcov-global-exclusions.txt"
floors_file="$repo_root/scripts/fuzzcov-global-floors.txt"

# Workspace modules are discovered from go.work after argument parsing. An
# explicit --modules selection is useful while a module is being raised, but the
# default contract is the full workspace as it exists at execution time.
workspace_modules=()
# bash 3.2 — the stock macOS bash — has no associative arrays. Sets in this
# script are newline-delimited lists behind in_list; the two module maps are
# "key<TAB>value" line stores behind map_get. Set entries may themselves
# contain tabs (module<TAB>pkg keys); map keys are module names and never do.
tab=$'\t'
in_list() { case "$2" in *$'\n'"$1"$'\n'*) return 0 ;; *) return 1 ;; esac; }
map_get() {
	local key="$1" line
	while IFS= read -r line; do
		case "$line" in "$key"$'\t'*) printf '%s' "${line#*$'\t'}"; return 0 ;; esac
	done <<<"$2"
	return 1
}
workspace_module_paths=""
workspace_module_dirs=""
selected_modules=()
modules_argument=""
modules_argument_set=false
rapid_seeds=(1 2 3 5 8)
rapid_checks=100
rapid_steps=30
rapid_nofailfile=true
rapid_log=false
rapid_verbose=false
rapid_debug=false
rapid_debugvis=false
rapid_shrinktime=30s
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
	[ "$modules_argument_set" = false ] || die "--modules may be specified only once"
	modules_argument="$1"
	modules_argument_set=true
}

select_modules() {
	local words="$1" module
	read -r -a selected_modules <<<"$words"
	[ "${#selected_modules[@]}" -gt 0 ] || die "--modules must name at least one go.work module"
	local seen=$'\n'
	for module in "${selected_modules[@]}"; do
		is_workspace_module "$module" || die "unknown go.work module: $module"
		if in_list "$module" "$seen"; then die "duplicate module: $module"; fi
		seen="$seen$module"$'\n'
	done
}

# clean_lexical_path resolves . and .. without following symlinks. Workspace
# target identity is intentionally based on this spelling, matching the registry
# checker; physical resolution below is only a containment/integrity check.
clean_lexical_path() {
	local raw="$1" input component result
	local -a components=()
	case "$raw" in
		/*) input="$raw" ;;
		*) input="$repo_root/$raw" ;;
	esac
	IFS=/ read -r -a raw_components <<<"$input"
	for component in "${raw_components[@]}"; do
		case "$component" in
			''|.) ;;
			..)
				[ "${#components[@]}" -gt 0 ] || return 1
				components=("${components[@]:0:${#components[@]} - 1}")
				;;
			*) components+=("$component") ;;
		esac
	done
	result=/
	if [ "${#components[@]}" -gt 0 ]; then
		local IFS=/
		result="/${components[*]}"
	fi
	printf '%s\n' "$result"
}

discover_workspace_modules() {
	local workspace_json="$work/go.work.json"
	local workspace_paths="$work/go.work-paths.txt"
	local disk_path logical_module_dir resolved_module_dir module
	local seen=$'\n' resolved_seen=$'\n'
	workspace_modules=()
	workspace_module_paths=""
	workspace_module_dirs=""

	if ! (cd "$repo_root" && "$go_bin" work edit -json "$go_work") >"$workspace_json"; then
		die "cannot read go.work module list: $go_work"
	fi
	# go work edit owns Go syntax and JSON escaping. This narrow extraction rejects
	# escaped values rather than letting an un-decoded path enter shell logic.
	if ! awk '
		/"DiskPath"/ {
			if ($0 !~ /"DiskPath"[[:space:]]*:[[:space:]]*"[^"\\]*"/) {
				bad = 1
				next
			}
			path = $0
			sub(/^.*"DiskPath"[[:space:]]*:[[:space:]]*"/, "", path)
			sub(/".*$/, "", path)
			if (path ~ /\\/) {
				bad = 1
				next
			}
			print path
			found = 1
		}
		END { exit bad || !found }
	' "$workspace_json" >"$workspace_paths"; then
		die "cannot parse go.work module list: $go_work"
	fi

	while IFS= read -r disk_path; do
		[ -n "$disk_path" ] || die "go.work contains an empty module path"
		if ! logical_module_dir="$(clean_lexical_path "$disk_path")"; then
			die "go.work module has unsafe path: $disk_path"
		fi
		case "$logical_module_dir" in
			"$repo_root") module=. ;;
			"$repo_root"/*) module="${logical_module_dir#"$repo_root/"}" ;;
			*) die "go.work module is outside repository root: $disk_path" ;;
		esac
		case "$module" in
			.) ;;
			*) [[ "$module" =~ ^[A-Za-z0-9][A-Za-z0-9._/-]*$ ]] || die "go.work module has unsafe label: $disk_path" ;;
		esac
		if ! resolved_module_dir="$(cd "$logical_module_dir" 2>/dev/null && pwd -P)"; then
			die "go.work module directory does not exist: $disk_path"
		fi
		case "$resolved_module_dir" in
			"$repo_root"|"$repo_root"/*) ;;
			*) die "go.work module is outside repository root: $disk_path" ;;
		esac
		[ -f "$resolved_module_dir/go.mod" ] || die "go.work module has no go.mod: $disk_path"
		if in_list "$module" "$seen"; then die "go.work lists duplicate module: $module"; fi
		if in_list "$resolved_module_dir" "$resolved_seen"; then die "go.work lists duplicate module directory: $disk_path"; fi
		seen="$seen$module"$'\n'
		resolved_seen="$resolved_seen$resolved_module_dir"$'\n'
		workspace_modules+=("$module")
		workspace_module_paths="$workspace_module_paths$module$tab$logical_module_dir"$'\n'
		workspace_module_dirs="$workspace_module_dirs$module$tab$resolved_module_dir"$'\n'
	done <"$workspace_paths"
	[ "${#workspace_modules[@]}" -gt 0 ] || die "go.work contains no modules"
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
# gets one explicit workspace and ignores persisted or ambient Go configuration.
export GOWORK="$go_work" GOENV=off GOFLAGS=

# Keep zero-row validation aligned with the int width used by the Go accounting
# command. Unknown architectures use the safe 32-bit bound.
if ! go_arch="$("$go_bin" env GOARCH)"; then
	die "cannot determine Go architecture for coverage profile validation"
fi
case "$go_arch" in
	amd64|arm64|loong64|mips64|mips64le|ppc64|ppc64le|riscv64|s390x)
		go_int_max=9223372036854775807
		;;
	*)
		go_int_max=2147483647
		;;
esac

work="$(mktemp -d -t serf-fuzzcov-global.XXXXXX)"
trap 'rm -rf "$work"' EXIT
plan="$work/targets.tsv"
groups="$work/groups.tsv"
global_manifest="$work/global-profiles.tsv"

is_expected_global_gate_failure() {
	local stderr_file="$1"
	grep -Eq '^serf-fuzzcov: (RAW THRESHOLD BREACH:|REGRESSION |refusing to bless:)' "$stderr_file"
}

discover_workspace_modules
if [ "$modules_argument_set" = true ]; then
	select_modules "$modules_argument"
else
	selected_modules=("${workspace_modules[@]}")
fi

echo "fuzz-coverage-global: validating registered native and Rapid targets" >&2
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

surface_list=$'\n'
target_list=$'\n'
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
	if in_list "$identity" "$target_list"; then die "registry plan duplicates target: $kind:$module:$pkg:$name"; fi
	target_list="$target_list$identity"$'\n'
	if ! in_list "$module$tab$pkg" "$surface_list"; then
		surface_list="$surface_list$module$tab$pkg"$'\n'
	fi
done <"$plan"

selected_list=$'\n'
for module in "${selected_modules[@]}"; do
	selected_list="$selected_list$module"$'\n'
done

# Preflight every production package before running one replay. This avoids Go
# 1.25's no-test-binary covdata path and gives a complete actionable list rather
# than discovering missing local surfaces one package at a time mid-measurement.
production_package_list=$'\n'
preflight_failed=false
for module in "${selected_modules[@]}"; do
	logical_module_dir="$(map_get "$module" "$workspace_module_paths")" || logical_module_dir=""
	module_dir="$(map_get "$module" "$workspace_module_dirs")" || module_dir=""
	[ -n "$logical_module_dir" ] && [ -n "$module_dir" ] || die "missing module directory mapping: $module"
	list_file="$work/packages-${module//\//_}.txt"
	if ! (cd "$logical_module_dir" && "$go_bin" list -tags serffuzz -f '{{.Dir}}' ./...) >"$list_file"; then
		die "go list failed for module: $module"
	fi
	while IFS= read -r package_dir; do
		[ -n "$package_dir" ] || continue
		if ! resolved_package_dir="$(cd "$package_dir" 2>/dev/null && pwd -P)"; then
			die "go list returned unreadable package directory for module $module: $package_dir"
		fi
		if [ "$resolved_package_dir" = "$module_dir" ]; then
			pkg=.
		elif [[ "$resolved_package_dir" == "$module_dir/"* ]]; then
			pkg="./${resolved_package_dir#"$module_dir/"}"
		else
			die "go list returned package outside module $module: $package_dir"
		fi
		if ! in_list "$module$tab$pkg" "$production_package_list"; then
			production_package_list="$production_package_list$module$tab$pkg"$'\n'
		fi
		if ! in_list "$module$tab$pkg" "$surface_list"; then
			echo "missing local fuzz surface: $module:$pkg" >&2
			preflight_failed=true
		fi
	done <"$list_file"
done

# A plan row for a non-production package is just as unsafe as a missing row:
# it would otherwise be replayed outside the exact denominator we preflighted.
while IFS= read -r key; do
	[ -n "$key" ] || continue
	module="${key%%$tab*}"
	pkg="${key#*$tab}"
	in_list "$module" "$selected_list" || continue
	if ! in_list "$key" "$production_package_list"; then
		echo "registered local fuzz surface is not a production package: $module:$pkg" >&2
		preflight_failed=true
	fi
done <<<"$surface_list"

if [ "$preflight_failed" = true ]; then
	die "local fuzz surface preflight failed; replay did not begin"
fi

# Select and sort the package groups after preflight. A package can own many
# native/Rapid targets, but it contributes exactly one merged profile below.
: >"$groups"
while IFS=$'\t' read -r kind module pkg name; do
	in_list "$module" "$selected_list" || continue
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
	if ! LC_ALL=C awk -v go_int_max="$go_int_max" '
		# Preserve parser-compatible decimal values without numeric coercion.
		function normalize_decimal(value, normalized) {
			normalized = value
			sub(/^0+/, "", normalized)
			if (normalized == "") {
				normalized = "0"
			}
			return normalized
		}
		# Compare unsigned decimal integers without AWK numeric coercion.
		function decimal_compare(left, right, normalized_left, normalized_right, left_length, right_length) {
			normalized_left = normalize_decimal(left)
			normalized_right = normalize_decimal(right)
			left_length = length(normalized_left)
			right_length = length(normalized_right)
			if (left_length < right_length) {
				return -1
			}
			if (left_length > right_length) {
				return 1
			}
			# A nonnumeric prefix forces an exact lexical comparison at equal length.
			if (("x" normalized_left) < ("x" normalized_right)) {
				return -1
			}
			if (("x" normalized_left) > ("x" normalized_right)) {
				return 1
			}
			return 0
		}
		# go_int_max comes from the same Go executable that runs accounting.
		function fits_go_int(value) {
			return decimal_compare(value, go_int_max) <= 0
		}

		FNR == 1 {
			if ($0 != "mode: set") {
				printf "invalid coverage mode in %s: %s\\n", FILENAME, $0 > "/dev/stderr"
				bad = 1
			}
			next
		}
		{
			invalid = NF != 3 || $2 !~ /^[0-9]+$/ || $3 !~ /^[0-9]+$/
			if (!invalid) {
				statements = normalize_decimal($2)
				count_value = normalize_decimal($3)
				invalid = !fits_go_int(statements) || !fits_go_int(count_value)
			}
			if (!invalid) {
				location = $1
				invalid = location !~ /^.+:[0-9]+\.[0-9]+,[0-9]+\.[0-9]+$/
			}
			if (!invalid) {
				range = location
				sub(/^.*:/, "", range)
				position_count = split(range, position, /[,.]/)
				invalid = position_count != 4 ||
					position[1] ~ /^0+$/ || position[2] ~ /^0+$/ ||
					position[3] ~ /^0+$/ || position[4] ~ /^0+$/
				if (!invalid) {
					for (position_index = 1; position_index <= 4; position_index++) {
						if (!fits_go_int(position[position_index])) {
							invalid = 1
							break
						}
					}
				}
				if (!invalid) {
					line_order = decimal_compare(position[3], position[1])
					invalid = line_order < 0 ||
						(line_order == 0 && decimal_compare(position[4], position[2]) < 0)
				}
			}
			if (invalid) {
				printf "invalid coverage block in %s: %s\\n", FILENAME, $0 > "/dev/stderr"
				bad = 1
				next
			}
			# Go can emit valid metadata blocks with no statements. They cannot affect
			# either side of statement coverage, so leave them out of the union.
			if (statements == "0") {
				next
			}
		}
		{
			key = $1 " " statements
			if (!(key in count) || decimal_compare(count_value, count[key]) > 0) {
				count[key] = count_value
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
	local label="$kind:$module:$pkg:$name" logical_module_dir
	[ -z "$seed" ] || label="$label seed=$seed"
	logical_module_dir="$(map_get "$module" "$workspace_module_paths")" || logical_module_dir=""
	[ -n "$logical_module_dir" ] || die "missing module directory mapping: $module"
	echo "fuzz-coverage-global: replay $label" >&2
	if [ "$kind" = rapid ]; then
		# SERF_FUZZ_TESTS=1: the seqfuzz/schemafuzz rapid family t.Skip()s under a
		# plain `go test` (moved out of `make test` per the fuzz-family ruling);
		# this coverage replay must still drive them, not accept a skip's
		# zero-count profile as coverage.
		if ! (cd "$logical_module_dir" && \
			env -u RAPID_FAILFILE SERF_FUZZ_TESTS=1 RAPID_SEED="$seed" RAPID_CHECKS="$rapid_checks" RAPID_STEPS="$rapid_steps" RAPID_NOFAILFILE="$rapid_nofailfile" RAPID_LOG="$rapid_log" RAPID_V="$rapid_verbose" RAPID_DEBUG="$rapid_debug" RAPID_DEBUGVIS="$rapid_debugvis" RAPID_SHRINKTIME="$rapid_shrinktime" \
			"$capped" "$go_bin" test -tags serffuzz -run "^$name\$" -count=1 -coverprofile="$profile" "$pkg") >&2; then
			die "replay failed: $label"
		fi
	else
		if ! (cd "$logical_module_dir" && \
			"$capped" "$go_bin" test -tags serffuzz -run "^$name\$" -count=1 -coverprofile="$profile" "$pkg") >&2; then
			die "replay failed: $label"
		fi
	fi
	validate_profile "$profile" "$label"
}

# Profile names need only be unique within this run's private $work dir; a
# serial counter does that portably (BSD mktemp cannot substitute embedded Xs,
# so a target.XXXXXX.cov template stays literal and collides on the second use).
profile_serial=0

: >"$global_manifest"
while IFS=$'\t' read -r module pkg; do
	package_profiles=()
	while IFS=$'\t' read -r kind row_module row_pkg name; do
		[ "$row_module" = "$module" ] && [ "$row_pkg" = "$pkg" ] || continue
		if [ "$kind" = native ]; then
			profile_serial=$((profile_serial + 1))
			profile="$work/target.$profile_serial.cov"
			replay_target "$kind" "$module" "$pkg" "$name" "" "$profile"
			package_profiles+=("$profile")
		else
			for seed in "${rapid_seeds[@]}"; do
				profile_serial=$((profile_serial + 1))
				profile="$work/target.$profile_serial.cov"
				replay_target "$kind" "$module" "$pkg" "$name" "$seed" "$profile"
				package_profiles+=("$profile")
			done
		fi
	done <"$plan"
	profile_serial=$((profile_serial + 1))
	package_profile="$work/package.$profile_serial.cov"
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

echo "fuzz-coverage-global: account package-local profiles" >&2
if [ "$format" = json ]; then
	accounting_json="$work/accounting.json"
	accounting_stderr="$work/accounting.stderr"
	if (cd "$repo_root" && "$capped" "$go_bin" "${fuzzcov_args[@]}") >"$accounting_json" 2>"$accounting_stderr"; then
		accounting_status=0
	else
		accounting_status=$?
	fi
	cat "$accounting_stderr" >&2
	if [ "$accounting_status" -ne 0 ]; then
		if [ "$accounting_status" -eq 1 ] && [ -s "$accounting_json" ] && is_expected_global_gate_failure "$accounting_stderr"; then
			cat "$accounting_json"
			exit "$accounting_status"
		fi
		die "global coverage accounting failed"
	fi
	cat "$accounting_json"
else
	if ! (cd "$repo_root" && "$capped" "$go_bin" "${fuzzcov_args[@]}"); then
		die "global coverage accounting failed"
	fi
fi
