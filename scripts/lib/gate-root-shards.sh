# gate-root-shards.sh — the gate's decisions about the root module's sharded
# packages. Sourced by run-module-tests.sh (and by gaterootshards_test.go);
# sourcing only defines the table and functions.
#
# Root-module packages run as cost-balanced shards beside the root go test,
# one "label PREFIX package-dir" entry each: evener dev <label>-shards runs
# them, and <PREFIX>_SHARD_* configure them. The prefix is spelled out rather
# than derived with ${label^^}, which macOS's stock bash 3.2 cannot parse.
#
# <PREFIX>_SHARDS (HUB_SHARDS, CLI_SHARDS) picks where each package runs:
#   1 (default)  as shards beside this run's root go test
#   0            inside the root go test itself
#   elsewhere    not in this run at all: another job runs it (the CI race
#                lanes run the hub on one runner and the rest of the root
#                module on another)
# ROOT_REST=0 skips the root go test itself, so a run can be only the shards.
# Any other value is refused: a mistyped toggle must not quietly drop a
# package or run it twice.
ROOT_SHARDED=("hub HUB cmd/evener-hub" "cli CLI cmd/evener")

# root_shard_mode PREFIX — print shard, inline or elsewhere for that package.
root_shard_mode() {
	local toggle="${1}_SHARDS"
	case "${!toggle:-1}" in
	1) printf 'shard\n' ;;
	0) printf 'inline\n' ;;
	elsewhere) printf 'elsewhere\n' ;;
	*)
		printf 'run-module-tests.sh: %s must be 1, 0 or elsewhere (got %s)\n' "$toggle" "${!toggle}" >&2
		return 2
		;;
	esac
}

# root_shard_excluded_packages — print the import path of every sharded package
# the root go test must leave out: those sharded beside it and those another
# job runs.
root_shard_excluded_packages() {
	local entry label prefix dir mode
	for entry in "${ROOT_SHARDED[@]}"; do
		read -r label prefix dir <<<"$entry"
		mode=$(root_shard_mode "$prefix") || return $?
		[ "$mode" = inline ] || printf 'primeradiant.com/evener/%s\n' "$dir"
	done
}

# root_shard_runners — print "label PREFIX" for every package this run shards.
root_shard_runners() {
	local entry label prefix dir mode
	for entry in "${ROOT_SHARDED[@]}"; do
		read -r label prefix dir <<<"$entry"
		mode=$(root_shard_mode "$prefix") || return $?
		[ "$mode" != shard ] || printf '%s %s\n' "$label" "$prefix"
	done
}

# root_rest_enabled — whether this run tests the root module's other packages.
root_rest_enabled() {
	case "${ROOT_REST:-1}" in
	1) return 0 ;;
	0) return 1 ;;
	*)
		printf 'run-module-tests.sh: ROOT_REST must be 1 or 0 (got %s)\n' "$ROOT_REST" >&2
		return 2
		;;
	esac
}
