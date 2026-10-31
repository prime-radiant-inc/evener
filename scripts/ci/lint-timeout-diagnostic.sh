#!/usr/bin/env bash
# Controlled timeout experiment, not a replacement for the nine-module required gate.
# Only orchestration: the existing Go module runner schedules the real linter.
set -euo pipefail

# PATH shim calls this mode with literal paths, not new environment knobs.
if [[ ${1:-} == instrument ]]; then
  real=$2 phase=$3 source=$4
  shift 4
  module=${PWD##*/}
  [[ $PWD != "$source" ]] || module=root
  out="$phase/$module"
  mkdir "$out"
  printf '%s\n' "$PWD" >"$out/cwd"
  printf '%s\n' "$@" >"$out/argv"
  date -u +%FT%TZ >"$out/start"
  rc=0
  /usr/bin/time -v -o "$out/resources" "$real" "$@" --verbose --show-stats \
    >"$out/linter.log" 2>&1 || rc=$?
  printf '%s\n' "$rc" >"$out/exit"
  date -u +%FT%TZ >"$out/end"
  cat "$out/linter.log"
  exit "$rc"
fi

head=41b8702f5e34954664910b01e846cb089c3f8d7d
base=656b05baed15d02d2288f17bfb07d93643b21f3e
script=$(realpath "$0")
work="${RUNNER_TEMP:?hosted runner required}/lint-timeout-diagnostic"
# Refuse reuse rather than silently warm an allegedly cold run.
mkdir "$work"
mkdir "$work/evidence" "$work/bin"
evidence="$work/evidence"
exec > >(tee "$evidence/setup.log") 2>&1
printf 'incomplete\n' >"$evidence/verdict"
export GOTOOLCHAIN=local GOCACHE="$work/setup-go" GOMODCACHE="$work/modules"
{
  date -u +%FT%TZ
  uname -a
  printf 'ImageOS=%s ImageVersion=%s\n' "${ImageOS:-unknown}" "${ImageVersion:-unknown}"
  nproc
  cat /proc/meminfo
  for file in /sys/fs/cgroup/cpu.max /sys/fs/cgroup/memory.max; do
    if [[ -r $file ]]; then printf '%s\n' "$file"; cat "$file"; fi
  done
  go version
} >"$evidence/runner"
[[ $(go env GOVERSION) == go1.27.0 ]]
for label in head base; do
  ref=$head
  [[ $label != base ]] || ref=$base
  git worktree add --detach "$work/$label" "$ref"
  (
    cd "$work/$label"
    git rev-parse HEAD 'HEAD^{tree}'
    sha256sum .golangci.yml .golangci-appwire.yml .tool-versions go.work go.mod go.sum
    cat .golangci.yml
  ) >"$evidence/$label-source"
done
# These must remain comparable; do not silently test different tool/rule policies.
git diff --exit-code "$base" "$head" -- .golangci.yml .golangci-appwire.yml \
  .tool-versions go.work go.work.sum ':(glob)**/go.mod' ':(glob)**/go.sum' \
  cmd/evener-dev/modulelint.go >"$evidence/policy-diff"
git diff --stat "$base" "$head" -- '*.go' >"$evidence/go-source-diff"
(
  cd "$work/head"
  [[ $(awk '$1=="golangci-lint" {print $2}' .tool-versions) == 2.13.1 ]]
  make tools-golangci
  go build -o "$work/bin/evener-dev" ./cmd/evener-dev/bin/
)
real="$(go env GOPATH)/bin/golangci-lint"
"$real" version | tee "$evidence/linter-version"
grep -Eq 'version 2\.13\.1([[:space:]]|$)' "$evidence/linter-version"
"$real" run --help >"$evidence/linter-help"
/usr/bin/time --help >"$evidence/time-help"
# Exclude dependency download variability, but never prime an arm's Go cache.
for source in "$work/head" "$work/base"; do
  (cd "$source"; go mod download)
done

# Regenerate a tiny shim for each phase. It always invokes the pinned real binary.
shim() {
  printf '#!/usr/bin/env bash\nexec bash %q instrument %q %q %q "$@"\n' \
    "$script" "$real" "$1" "$2" >"$work/bin/golangci-lint"
  chmod +x "$work/bin/golangci-lint"
}
# Actual invalid invocation proves status preservation, without a fake toolchain.
mkdir "$evidence/exit-probe"
shim "$evidence/exit-probe" "$work/head"
direct=0
"$real" run --timeout=not-a-duration >"$evidence/exit-probe/direct.log" 2>&1 || direct=$?
wrapped=0
(cd "$work/head"; "$work/bin/golangci-lint" run --timeout=not-a-duration) \
  >"$evidence/exit-probe/wrapped.log" 2>&1 || wrapped=$?
printf 'direct=%s wrapped=%s\n' "$direct" "$wrapped" >"$evidence/exit-probe/status"
[[ $direct -ne 0 && $direct -eq $wrapped ]]
[[ $(cat "$evidence/exit-probe/root/exit") == "$direct" ]]

# vmstat is observation only; its first row is since boot, subsequent rows interval data.
vmstat -t 5 >"$evidence/vmstat" &
sampler=$!
trap 'kill "$sampler" 2>/dev/null || :; wait "$sampler" 2>/dev/null || :' EXIT
export PATH="$work/bin:$PATH"
failed=0
modules=(. agent llm auth envvars invariant identifier execsupport fuzz)
phase() {
  name=$1 source=$2 arm=$3 os=$4 parallel=$5
  out="$evidence/$name"
  mkdir "$out"
  shim "$out" "$source"
  printf 'source=%s\nGOCACHE=%s\nGOLANGCI_LINT_CACHE=%s\nGOOS=%s\nLINT_PARALLEL=%s\nMODULES=%s\n' \
    "$source" "$work/$arm/go" "$work/$arm/lint" "$os" "$parallel" "${modules[*]}" >"$out/inputs"
  date -u +%FT%TZ >"$out/start"
  rc=0
  (cd "$source"; GOCACHE="$work/$arm/go" GOLANGCI_LINT_CACHE="$work/$arm/lint" \
    GOOS="$os" MODULES="${modules[*]}" LINT_PARALLEL="$parallel" \
    "$work/bin/evener-dev" dev module-lint) >"$out/runner.log" 2>&1 || rc=$?
  printf '%s\n' "$rc" >"$out/exit"
  date -u +%FT%TZ >"$out/end"
  cat "$out/runner.log"
  [[ $rc -eq 0 ]] || failed=1
  # Missing/truncated evidence is an infrastructure failure, never a clean arm.
  for module in "${modules[@]}"; do
    [[ $module != . ]] || module=root
    for file in cwd argv start end exit resources linter.log; do
      test -s "$out/$module/$file"
    done
    [[ $(cat "$out/$module/exit") == 0 ]] || failed=1
  done
  [[ $(find "$out" -mindepth 1 -maxdepth 1 -type d | wc -l) -eq ${#modules[@]} ]]
  printf '%s\t%s\n' "$name" "$rc" >>"$evidence/phases.tsv"
}
# mkdir (without -p) asserts fresh empty cache roots; all are left for runner disposal.
for arm in A C; do
  mkdir "$work/$arm" "$work/$arm/go" "$work/$arm/lint"
  test -z "$(find "$work/$arm/go" "$work/$arm/lint" -mindepth 1 -print -quit)"
  printf '%s cold: empty Go and analysis caches\n' "$arm" >>"$evidence/cold-assertions"
done
phase A-linux "$work/head" A linux 4
phase A-darwin "$work/head" A darwin 4
phase C-linux "$work/head" C linux 4
phase C-darwin-parallel2 "$work/head" C darwin 2
[[ $(wc -l <"$evidence/phases.tsv") -eq 4 ]]
kill -0 "$sampler"
test -s "$evidence/vmstat"
printf '%s\n' "$failed" >"$evidence/verdict"
exit "$failed"
