#!/usr/bin/env bash
# scenario-cite-migrate-selftest.sh — deterministic cleanup-boundary tests for
# scripts/scenario-cite-migrate.sh. The fake mktemp returns both a hard failure
# and an existing repository path, so the test never needs to touch the host's
# real temporary directory or any real source file.
set -uo pipefail

script_dir="$(cd "$(dirname "$0")" && pwd -P)"
source_script="$script_dir/scenario-cite-migrate.sh"
work="$(mktemp -d -t serf-cite-migrate-selftest.XXXXXX)"
work="$(cd "$work" && pwd -P)"
trap 'rm -rf "$work"' EXIT

checks=0
fails=0
ok() { checks=$((checks + 1)); printf '  ok: %s\n' "$1"; }
bad() { checks=$((checks + 1)); fails=$((fails + 1)); printf 'FAIL: %s\n' "$1"; }

repo="$work/repo"
bin="$work/bin"
tmp_root="$work/tmp"
mkdir -p "$repo/scripts" "$repo/test/scenarios" "$repo/docs" "$bin" "$tmp_root"
cp "$source_script" "$repo/scripts/scenario-cite-migrate.sh"
chmod +x "$repo/scripts/scenario-cite-migrate.sh"
printf 'module example.test/cite-fixture\n\ngo 1.25.6\n' >"$repo/go.mod"
printf 'package fixture\n\nconst Sentinel = 1\n' >"$repo/sentinel.go"
printf 'package fixture\n\nconst OtherSentinel = 2\n' >"$repo/other.go"
printf '# scenario fixture\n\n`fixture.Sentinel` (`sentinel.go:3`)\n' >"$repo/test/scenarios/card.md"
printf '# documentation fixture\n' >"$repo/docs/agentic-testing.md"
(
	cd "$repo" &&
	git init -q &&
	git config user.email t@t &&
	git config user.name t &&
	git add go.mod sentinel.go other.go test/scenarios/card.md docs/agentic-testing.md &&
	git commit -qm init
) || {
	echo "FAIL: could not set up citation-migration fixture" >&2
	exit 1
}

cat >"$bin/mktemp" <<'FAKE_MKTEMP'
#!/usr/bin/env bash
set -u
case "${FAKE_MKTEMP_MODE:-}" in
failure)
	echo "mktemp: injected failure" >&2
	exit 1
	;;
invalid)
	# A command that claims success but returns the existing repository is an
	# invalid creation result; it is the path that used to arm the broad glob.
	printf '%s\n' "${FAKE_MKTEMP_RESULT:?}"
	exit 0
	;;
esac
echo "mktemp: test mode was not selected" >&2
exit 2
FAKE_MKTEMP
chmod +x "$bin/mktemp"

run_migration() {
	local mode="$1" output_file="$2"
	if output="$(cd "$repo" && FAKE_MKTEMP_MODE="$mode" FAKE_MKTEMP_RESULT="$repo" \
		TMPDIR="$tmp_root" PATH="$bin:$PATH" bash scripts/scenario-cite-migrate.sh 2>&1)"; then
		rc=0
	else
		rc=$?
	fi
	printf '%s\n' "$output" >"$output_file"
	return "$rc"
}

failure_out="$work/mktemp-failure.out"
if run_migration failure "$failure_out"; then
	bad "mktemp failure exits nonzero"
else
	ok "mktemp failure exits nonzero"
fi
if grep -qF "mktemp: injected failure" "$failure_out"; then
	ok "mktemp failure keeps its diagnostic"
else
	bad "mktemp failure loses its diagnostic"
fi
if [ -f "$repo/sentinel.go" ] && [ -f "$repo/other.go" ]; then
	ok "mktemp failure leaves repository Go files untouched"
else
	bad "mktemp failure removed a repository Go file"
fi

invalid_out="$work/invalid-result.out"
if run_migration invalid "$invalid_out"; then
	bad "an invalid mktemp result exits nonzero"
else
	ok "an invalid mktemp result exits nonzero"
fi
if grep -qF "outside its temporary root" "$invalid_out"; then
	ok "an invalid mktemp result names the rejected cleanup scope"
else
	bad "an invalid mktemp result does not explain the rejected cleanup scope"
fi
if [ -f "$repo/sentinel.go" ] && [ -f "$repo/other.go" ]; then
	ok "cleanup never broadens an invalid result into the repository"
else
	bad "cleanup removed repository Go files after an invalid result"
fi
if [ ! -e "$repo/migrate.go" ]; then
	ok "migration source is not written before temp validation"
else
	bad "migration source was written before temp validation"
fi

qualified_out="$work/qualified.out"
if output="$(cd "$repo" && TMPDIR="$tmp_root" PATH="$PATH" bash scripts/scenario-cite-migrate.sh --skips 2>&1)"; then
	rc=0
else
	rc=$?
fi
printf '%s\n' "$output" >"$qualified_out"
if [ "$rc" -eq 0 ] && grep -qF 'REWRITE test/scenarios/card.md:3' "$qualified_out"; then
	ok "qualified declaration anchor is migrated"
else
	bad "qualified declaration anchor is skipped"
fi
if (cd "$repo" && TMPDIR="$tmp_root" PATH="$PATH" bash scripts/scenario-cite-migrate.sh --apply >/dev/null 2>&1) &&
	grep -qF '`sentinel.go#Sentinel`' "$repo/test/scenarios/card.md"; then
	ok "qualified declaration rewrite is applied"
else
	bad "qualified declaration rewrite is not applied"
fi

printf '\nscenario-cite-migrate-selftest: %d checks, %d failed\n' "$checks" "$fails"
exit "$fails"
