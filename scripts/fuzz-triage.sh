#!/usr/bin/env bash
# fuzz-triage.sh — the local, on-demand fuzz campaign + auto-triage tool
# (roadmap 8.7). A developer runs it at a time of their choosing; it searches
# each surface on a budget, and — when it finds a DETERMINISTIC crash — opens
# exactly one human-reviewable PR (via the developer's own `gh` auth) carrying
# the crasher, a red-until-fixed regression test, and a reproducer, while a
# triage ledger records what has been found / fixed / quarantined. Nothing
# auto-merges; the only standing CI change is the fast `make fuzz` seed replay.
#
# Usage:
#   scripts/fuzz-triage.sh [--time DUR] [--dry-run] [--no-pr] [--no-corpus] [target ...]
#     --time DUR     per-target search budget, passed through to run-fuzz.sh
#                    (default inherits run-fuzz.sh's 60s; e.g. --time 5m).
#     --dry-run      discover, flake-guard, and dedup, but write nothing and open
#                    no PR — every decision is printed. Used by the self-test.
#     --no-pr        discover and commit artifacts to a local branch, but stop
#                    before pushing / `gh pr create` (inspect-first).
#     --no-corpus    skip promotion of the coverage-expanding corpus.
#     target ...     restrict to one or more "module:FuzzName" entries from
#                    run-fuzz.sh's TARGETS; default is every target.
#
# The flake-guard / dedup / ledger / PR logic is exercised deterministically by
# scripts/fuzz-triage-selftest.sh (synthetic failures, stubbed go/gh — no real
# crash and no real PR). The following env vars exist for that self-test and for
# advanced use; defaults are the production values:
#   SERF_FUZZ_REPO_ROOT  repo root (default: the parent of this script's dir)
#   SERF_FUZZ_RUNNER     the search engine    (default: scripts/run-fuzz.sh)
#   SERF_FUZZ_GH         the gh binary        (default: gh)
#   SERF_FUZZ_K          flake-guard replays  (default: 5)
#   SERF_FUZZ_MAX_SEEDS  corpus diversity cap (default: 8 per target per run)
set -uo pipefail

repo_root="${SERF_FUZZ_REPO_ROOT:-$(cd "$(dirname "$0")/.." && pwd)}"
state_dir="$repo_root/fuzz/state"
ledger="$state_dir/ledger.json"
buckets="$state_dir/buckets.json"
runner="${SERF_FUZZ_RUNNER:-$repo_root/scripts/run-fuzz.sh}"
gh="${SERF_FUZZ_GH:-gh}"
K="${SERF_FUZZ_K:-5}"            # flake-guard: a crasher must fail all K replays
max_seeds="${SERF_FUZZ_MAX_SEEDS:-8}"           # promoted-corpus diversity cap
max_seed_bytes="${SERF_FUZZ_MAX_SEED_BYTES:-32768}" # drop promoted seeds larger than this

duration=""
dry_run=false
no_pr=false
no_corpus=false
declare -a targets=()

while [ $# -gt 0 ]; do
	case "$1" in
		--time) duration="$2"; shift 2 ;;
		--time=*) duration="${1#*=}"; shift ;;
		--dry-run) dry_run=true; shift ;;
		--no-pr) no_pr=true; shift ;;
		--no-corpus) no_corpus=true; shift ;;
		-h|--help) sed -n '2,30p' "$0"; exit 0 ;;
		--*) echo "fuzz-triage: unknown flag $1" >&2; exit 2 ;;
		*) targets+=("$1"); shift ;;
	esac
done

log()  { printf '%s\n' "$*"; }
note() { printf '  %s\n' "$*"; }
now()  { date -u +%Y-%m-%dT%H:%M:%SZ; }

git_repo() { git -C "$repo_root" "$@"; }

# --- ledger helpers (jq; the ledger is a signature -> record map) -------------

ledger_write() { # filter, then jq args... — apply to $ledger atomically
	local filter="$1"; shift
	local tmp; tmp="$(mktemp)"
	if jq "$@" "$filter" "$ledger" >"$tmp"; then
		mv "$tmp" "$ledger"
	else
		rm -f "$tmp"
		echo "fuzz-triage: ledger update failed" >&2
	fi
}

ledger_get() { # key, field -> value ("" when absent)
	jq -r --arg k "$1" --arg f "$2" '.[$k][$f] // ""' "$ledger"
}

ledger_status() { jq -r --arg k "$1" '.[$k].status // ""' "$ledger"; }

# --- gh helpers --------------------------------------------------------------

gh_available() {
	command -v "$gh" >/dev/null 2>&1 || return 1
	"$gh" auth status >/dev/null 2>&1
}

pr_exists() { # branch -> 0 if a PR (any state) already targets it
	gh_available || return 1
	local out
	out="$("$gh" pr list --head "$1" --state all --json number 2>/dev/null || echo "")"
	[ -n "$out" ] && [ "$out" != "[]" ]
}

# --- step 0: reconcile the ledger -------------------------------------------
# Every `found` entry is replayed on the current tree; any that now PASSES means
# the bug was fixed (its PR merged) -> flip to `fixed`. This is the cheap way to
# get a fixed-count without any webhook or scheduler.

reconcile_ledger() {
	[ -f "$ledger" ] || return 0
	local key pkg run
	while IFS= read -r key; do
		[ -n "$key" ] || continue
		pkg="$(ledger_get "$key" pkg)"
		run="$(ledger_get "$key" run)"
		[ -n "$pkg" ] && [ -n "$run" ] || continue
		if ( cd "$repo_root/$pkg" && go test -run "$run" -count=1 . ) >/dev/null 2>&1; then
			if $dry_run; then
				log "reconcile $key -> fixed (dry-run)"
			else
				ledger_write '.[$k] += {status:"fixed", fixed_seen:$now}' --arg k "$key" --arg now "$(now)"
				log "reconcile $key -> fixed"
			fi
		fi
	done < <(jq -r 'to_entries[] | select(.value.status=="found") | .key' "$ledger")
}

# --- step 2: the search ------------------------------------------------------

want_target() { # entry "module:FuzzName" -> 0 if selected
	[ ${#targets[@]} -eq 0 ] && return 0
	local o
	for o in "${targets[@]}"; do [ "$o" = "$1" ] && return 0; done
	return 1
}

run_search() {
	log "=== searching (SERF_FUZZ_PERSIST=1${duration:+, --time $duration}) ==="
	local -a runner_args=()
	[ -n "$duration" ] && runner_args+=(--time "$duration")
	[ ${#targets[@]} -gt 0 ] && runner_args+=("${targets[@]}")
	# run-fuzz.sh drives both the native (go test -fuzz) and rapid (go test -run)
	# surfaces from the unified registry; SERF_FUZZ_PERSIST=1 is inherited by the
	# rapid promoter targets so a live-found failure persists durably.
	SERF_FUZZ_PERSIST=1 bash "$runner" "${runner_args[@]}" || true
}

# --- step 3: discover new crashers (snapshot diff over git status) ------------

new_untracked() { # echoes paths that became untracked (??) between the snapshots
	comm -13 <(printf '%s\n' "$1" | sort) <(printf '%s\n' "$2" | sort) \
		| sed -n 's/^?? //p'
}

# --- step 3.1: flake-guard for Go-native crashers ----------------------------
# Go-native crashers never went through promoter.Promote, so give them the same
# discipline: re-run the saved corpus entry K times; deterministic only if it
# FAILS all K. survived_runs is the run index at which it first passed.

flake_survived=0
flake_guard_native() { # pkgdir, fuzzname, hash -> 0 deterministic, 1 flaky
	local pkgdir="$1" fuzz="$2" hash="$3" i
	for ((i = 1; i <= K; i++)); do
		if ( cd "$repo_root/$pkgdir" && go test -run "${fuzz}/${hash}" -count=1 . ) >/dev/null 2>&1; then
			flake_survived=$((i - 1))
			return 1 # passed a replay -> not deterministic
		fi
	done
	return 0 # failed all K -> deterministic
}

# --- step 3.4: open the PR (never auto-merged) --------------------------------

write_pr_body() { # sig12, surface, oracle, detail, kind, pkg, run, reproducer -> path
	local sig12="$1" surface="$2" oracle="$3" detail="$4" kind="$5" pkg="$6" run="$7" repro="$8"
	local body; body="$(mktemp)"
	{
		printf '## Fuzz crash: %s / %s (`%s`)\n\n' "$surface" "$oracle" "$sig12"
		printf -- '- **Surface:** %s\n- **Oracle:** %s\n- **Signature:** `%s`\n- **Detail:** %s\n\n' \
			"$surface" "$oracle" "$sig12" "$detail"
		printf '### Reproduce locally\n\n```\n%s\n```\n\n' "$repro"
		printf '### Determinism\n\n'
		if [ "$kind" = native ]; then
			printf 'Flake-guarded: the saved corpus entry failed all %s replays.\n\n' "$K"
		else
			printf 'Flake-guarded by the promoter (%s same-signature replays inside Promote).\n\n' "$K"
		fi
		printf 'This is a real, deterministic failure. Review and FIX the bug; '
		printf 'do not merge the regression test without a fix — it is red on `main` until then.\n'
	} >"$body"
	printf '%s\n' "$body"
}

open_pr() { # sig12, sigkey, surface, oracle, detail, kind, pkg, run, test_path, artifact, repro
	local sig12="$1" sigkey="$2" surface="$3" oracle="$4" detail="$5" kind="$6"
	local pkg="$7" run="$8" test_path="$9" artifact="${10}" repro="${11}"
	local branch="fuzz/crash-$sig12"
	local ts; ts="$(now)"

	# Record the ledger entry first so it rides into the branch commit.
	local first; first="$(ledger_get "$sigkey" first_seen)"; [ -n "$first" ] || first="$ts"
	ledger_write '.[$k] = {
			surface:$surface, oracle:$oracle, sig:$sig, status:"found",
			first_seen:$first, last_seen:$now, kind:$kind, pkg:$pkg, run:$run,
			test_path:$test_path, detail:$detail, pr:""
		}' \
		--arg k "$sigkey" --arg surface "$surface" --arg oracle "$oracle" --arg sig "$sig12" \
		--arg first "$first" --arg now "$ts" --arg kind "$kind" --arg pkg "$pkg" \
		--arg run "$run" --arg test_path "$test_path" --arg detail "$detail"

	# Each distinct bug gets its own PR branched from the base branch, not from a
	# previously-filed crasher branch.
	git_repo switch "$base_branch" >/dev/null 2>&1 || true
	git_repo switch -c "$branch" || { echo "fuzz-triage: branch $branch exists; skipping" >&2; return 1; }
	git_repo add "$artifact" "$ledger" "$buckets" 2>/dev/null || true
	[ -n "$test_path" ] && git_repo add "$test_path" 2>/dev/null || true
	git_repo commit -q -F - <<EOF
test(fuzz): regression for $surface/$oracle $sig12

Auto-filed by scripts/fuzz-triage.sh. The committed regression test is red on
main until the bug is fixed; $detail.

Claude-Session: https://claude.ai/code/session_0111JibAhU1kVtGpvgYeGJRV
EOF

	if $no_pr; then
		note "committed to local branch $branch (--no-pr: not pushed)"
		return 0
	fi
	if ! gh_available; then
		note "gh unavailable/unauthenticated; left on local branch $branch (no push, no PR)"
		return 0
	fi

	git_repo push -u origin "$branch" || { echo "fuzz-triage: push failed" >&2; return 1; }
	local bodyfile url
	bodyfile="$(write_pr_body "$sig12" "$surface" "$oracle" "$detail" "$kind" "$pkg" "$run" "$repro")"
	url="$("$gh" pr create --base main --head "$branch" --label fuzz-crash \
		--title "Fuzz crash: $surface $oracle ($sig12)" --body-file "$bodyfile" 2>/dev/null || echo "")"
	rm -f "$bodyfile"
	if [ -n "$url" ]; then
		ledger_write '.[$k] += {pr:$pr}' --arg k "$sigkey" --arg pr "$url"
		git_repo commit -qam "chore(fuzz): record PR url for $sig12

Claude-Session: https://claude.ai/code/session_0111JibAhU1kVtGpvgYeGJRV" || true
		git_repo push || true
		note "opened PR $url"
	else
		note "gh pr create failed; left on local branch $branch"
	fi
}

# --- per-crasher handling ----------------------------------------------------

handle_native() { # path under */testdata/fuzz/<FuzzName>/<hash>
	local path="$1"
	local hash fuzz pkgdir
	hash="$(basename "$path")"
	fuzz="$(basename "$(dirname "$path")")"
	pkgdir="${path%%/testdata/fuzz/*}"
	local sig12="${hash:0:12}"
	local sigkey="$fuzz:$hash"
	local run="${fuzz}/${hash}"
	local detail="go-native fuzz crasher in $fuzz"
	local repro="cd $pkgdir && go test -run '$run' ."
	log "crasher (native) $path  sig=$sig12"

	# Dedup layer 1: ledger.
	case "$(ledger_status "$sigkey")" in
		found)
			note "dedup (ledger): already filed"
			$dry_run || ledger_write '.[$k] += {last_seen:$now}' --arg k "$sigkey" --arg now "$(now)"
			return ;;
	esac
	# Dedup layer 3: an open/closed PR already covers it.
	if pr_exists "fuzz/crash-$sig12"; then
		note "dedup (pr-exists): fuzz/crash-$sig12"
		return
	fi
	# Flake-guard.
	if ! flake_guard_native "$pkgdir" "$fuzz" "$hash"; then
		note "quarantine: survived a replay (run $flake_survived of $K) — not deterministic"
		if ! $dry_run; then
			git_repo checkout -- "$path" 2>/dev/null || rm -f "$repo_root/$path"
			ledger_write '.[$k] = ((.[$k] // {first_seen:$now}) + {
					surface:$surface, oracle:"crash", sig:$sig, status:"quarantined",
					last_seen:$now, survived_runs:($surv|tonumber), kind:"native",
					pkg:$pkg, run:$run, detail:$detail
				})' \
				--arg k "$sigkey" --arg surface "$fuzz" --arg sig "$sig12" --arg now "$(now)" \
				--arg surv "$flake_survived" --arg pkg "$pkgdir" --arg run "$run" --arg detail "$detail"
		fi
		return
	fi
	# Deterministic + novel: open the PR (default).
	if $dry_run; then
		note "promote -> would open PR fuzz/crash-$sig12 (dry-run)"
		return
	fi
	open_pr "$sig12" "$sigkey" "$fuzz" "crash" "$detail" native "$pkgdir" "$run" "" "$path" "$repro"
}

handle_promoter() { # path to an emitted testregression_*_test.go
	local path="$1"
	local pkgdir; pkgdir="$(dirname "$path")"
	# The emitted file carries its provenance: "// ... Signature: <oracle>:<key>".
	local sigkey surface oracle testfunc
	sigkey="$(sed -n 's#^// .*Signature: \(.*\)$#\1#p' "$repo_root/$path" | head -1)"
	surface="$(sed -n 's#^// Surface: \([^ ]*\).*#\1#p' "$repo_root/$path" | head -1)"
	oracle="$(sed -n 's#^// .*Oracle: \([^ ]*\).*#\1#p' "$repo_root/$path" | head -1)"
	testfunc="$(sed -n 's#^func \(TestRegression_[A-Za-z0-9_]*\)(.*#\1#p' "$repo_root/$path" | head -1)"
	[ -n "$sigkey" ] || sigkey="$(basename "$path")"
	local sig12; sig12="$(basename "$path" | sed -n 's#.*_\([0-9a-f]\{12\}\)_test.go$#\1#p')"
	[ -n "$sig12" ] || sig12="$(printf '%s' "$sigkey" | shasum | cut -c1-12)"
	local run="^${testfunc}\$"
	local detail="promoter regression $sigkey"
	local repro="cd $pkgdir && go test -run '$run' ."
	log "crasher (promoter) $path  sig=$sig12"

	case "$(ledger_status "$sigkey")" in
		found)
			note "dedup (ledger): already filed"
			$dry_run || ledger_write '.[$k] += {last_seen:$now}' --arg k "$sigkey" --arg now "$(now)"
			return ;;
	esac
	if pr_exists "fuzz/crash-$sig12"; then
		note "dedup (pr-exists): fuzz/crash-$sig12"
		return
	fi
	# Promoter crashers already passed Promote's K-replay guard (the emitted file
	# only exists for a deterministic failure), so no second flake-guard here.
	if $dry_run; then
		note "promote -> would open PR fuzz/crash-$sig12 (dry-run)"
		return
	fi
	open_pr "$sig12" "$sigkey" "$surface" "$oracle" "$detail" promoter "$pkgdir" "$run" "$path" "$path" "$repro"
}

# --- step 6: promote a MINIMIZED coverage-expanding corpus -------------------
# Fold the NEW inputs Go kept in its fuzz cache into the target's committed
# testdata/fuzz seeds so future runs start richer — but MINIMIZED, so the
# committed corpus stays small and high-signal rather than a raw cache dump:
#   * content-dedup: an input whose bytes already match a committed seed (under
#     ANY filename, not just the same one) is skipped, never re-committed;
#   * size-prefer: candidates are taken smallest-first, so the diversity cap
#     keeps the most-reduced inputs Go discovered (Go already minimizes the
#     crashers it writes; the coverage cache is what needs shrinking here);
#   * size-cap: an input larger than max_seed_bytes is dropped — a giant cache
#     entry is low-signal as a permanent seed and bloats the repo.
# A no-op when the cache is empty/absent. Skipped in --dry-run.

content_hash() { shasum "$1" 2>/dev/null | cut -d' ' -f1; }

promote_corpus() {
	$no_corpus && return 0
	$dry_run && return 0
	local gocache; gocache="$(go env GOCACHE 2>/dev/null || echo "")"
	[ -n "$gocache" ] || return 0
	local tag module pkg name cover focus
	while IFS=: read -r tag module pkg name cover focus; do
		# Only native targets keep a go-fuzz cache to promote seeds from.
		[ "$tag" = native ] || continue
		[ -n "$name" ] || continue
		want_target "$module:$name" || continue
		local pkgdir="$repo_root/$module/${pkg#./}"
		local importpath
		importpath="$(cd "$pkgdir" 2>/dev/null && go list -f '{{.ImportPath}}' . 2>/dev/null || echo "")"
		[ -n "$importpath" ] || continue
		local cachedir="$gocache/fuzz/$importpath/$name"
		[ -d "$cachedir" ] || continue
		local dest="$pkgdir/testdata/fuzz/$name"
		mkdir -p "$dest"

		# Index the committed seeds by content hash so a cache entry Go already has
		# (under a different filename) is not re-committed as a duplicate.
		local -A have=()
		local f h
		for f in "$dest"/*; do
			[ -f "$f" ] || continue
			h="$(content_hash "$f")"
			[ -n "$h" ] && have["$h"]=1
		done

		# Walk candidates smallest-first; dedup by content, drop oversized, cap count.
		local copied=0 base sz path
		while IFS=$'\t' read -r sz path; do
			[ -n "$path" ] || continue
			[ "$copied" -lt "$max_seeds" ] || break
			[ "$((sz))" -le "$max_seed_bytes" ] || continue
			h="$(content_hash "$path")"
			[ -n "$h" ] || continue
			[ -n "${have[$h]:-}" ] && continue
			base="$(basename "$path")"
			[ -e "$dest/$base" ] && continue
			cp "$path" "$dest/$base"
			have["$h"]=1
			copied=$((copied + 1))
		done < <(for f in "$cachedir"/*; do
				[ -f "$f" ] && printf '%s\t%s\n' "$(wc -c <"$f" | tr -d ' ')" "$f"
			done | sort -n)
		[ "$copied" -gt 0 ] && log "corpus: promoted $copied minimized seed(s) into $module/${pkg#./}/testdata/fuzz/$name"
	done < <(bash "$runner" --list)
}

# --- main --------------------------------------------------------------------

[ -f "$ledger" ] || { mkdir -p "$state_dir"; echo '{}' >"$ledger"; }
[ -f "$buckets" ] || { mkdir -p "$state_dir"; echo '{}' >"$buckets"; }

base_branch="$(git_repo rev-parse --abbrev-ref HEAD 2>/dev/null || echo HEAD)"

mode_suffix=""
$dry_run && mode_suffix+=" [dry-run]"
$no_pr && mode_suffix+=" [no-pr]"
log "=== fuzz-triage (K=$K)$mode_suffix ==="
reconcile_ledger

# -uall lists each new file individually, so a brand-new testdata/fuzz/<Fuzz>/
# directory is not collapsed to a single directory entry.
snap_before="$(git_repo status --porcelain --untracked-files=all 2>/dev/null || true)"
run_search
snap_after="$(git_repo status --porcelain --untracked-files=all 2>/dev/null || true)"

mapfile -t discovered < <(new_untracked "$snap_before" "$snap_after")
found_any=false
for path in "${discovered[@]}"; do
	[ -n "$path" ] || continue
	case "$path" in
		*/testdata/fuzz/*/*) handle_native "$path"; found_any=true ;;
		*testregression_*_test.go) handle_promoter "$path"; found_any=true ;;
	esac
done
$found_any || log "no new crashers discovered"

promote_corpus
log "=== done ==="
