# Reproduce the paired catalog experiment

Use two isolated checkouts of the product baseline `f18fe2290a5c1037aab21bd3201ce20bf09c9862` and measured candidate `1f1b3dccdac5f4311b0f877902e4834f28d56208`. Supply absolute paths as `BEFORE`, `AFTER`, and `RESEARCH` (this directory in the retained audit checkout). Supply an empty writable `EVENER_SCRATCH_DIR`. Keep other local test/build workloads stopped during measurement. These commands do not launch Evener or contact providers.

The recorded run used audit checkout `a16d1e78a0a49ade517f5466ff376ecb93d36dc0` for `BEFORE`; its product sources matched the baseline. The recorded binary and source hashes are in `build-manifest.json`. Go 1.27.0 is required to match the recorded compiler. A different path can change binary hashes without changing product source; record new hashes for new runs. The 2026-11-13 #4799 rerun used Go 1.27.4; its hashes are the `startup_vmhwm_rerun_2026_11_13` block of `build-manifest.json`.

## Build identical probes and benchmark source

```bash
set -e
export BEFORE AFTER RESEARCH EVENER_SCRATCH_DIR
export PYTHONDONTWRITEBYTECODE=1
BASE=f18fe2290a5c1037aab21bd3201ce20bf09c9862
CANDIDATE=1f1b3dccdac5f4311b0f877902e4834f28d56208
git -C "$BEFORE" diff --exit-code "$BASE" -- llm go.mod go.sum go.work
git -C "$AFTER" diff --exit-code "$CANDIDATE" -- llm go.mod go.sum go.work
git -C "$AFTER" show "$CANDIDATE:llm/registry/layer_rows_test.go" > "$EVENER_SCRATCH_DIR/catalog-layer_rows_test.go"
python3 - <<'PY'
import json, os
from pathlib import Path
scratch = Path(os.environ['EVENER_SCRATCH_DIR'])
for side, root in [('before',os.environ['BEFORE']),('after',os.environ['AFTER'])]:
    (scratch/f'catalog-{side}-overlay.json').write_text(json.dumps({'Replace': {
        str(Path(root)/'llm/registry/layer_rows_test.go'):str(scratch/'catalog-layer_rows_test.go')}}))
PY
for side in before after; do
  root="$BEFORE"; if [ "$side" = after ]; then root="$AFTER"; fi
  (cd "$root" && GOMAXPROCS=2 go build -p=1 -o "$EVENER_SCRATCH_DIR/catalog-startup-$side" "$RESEARCH/catalog-startup.go")
  (cd "$root" && GOMAXPROCS=2 go build -p=1 -o "$EVENER_SCRATCH_DIR/catalog-retention-$side" "$RESEARCH/catalog-retention.go")
  (cd "$root" && GOMAXPROCS=2 go test -p=1 -overlay "$EVENER_SCRATCH_DIR/catalog-$side-overlay.json" -c -o "$EVENER_SCRATCH_DIR/catalog-benchmark-$side" ./llm/registry)
done
```

## Retained heap

The recorded driver body below validates six ordered checkpoints and provider counts per subprocess. It emits both resource counters and complete checkpoint data. Redirect stdout to a new result file, not over the supplied evidence.

```bash
python3 - <<'PY'
import json, os, sys
from pathlib import Path
r=Path(os.environ['RESEARCH']); s=Path(os.environ['EVENER_SCRATCH_DIR'])
sys.path.insert(0,str(r))
from catalog_process import digest, paired_samples, run_process
def measure(binary):
    out, metrics=run_process([str(binary)])
    rows=[json.loads(line) for line in out.splitlines()]
    expected=[('before_load_post_gc',0),('first_load_before_forced_gc',1),
              ('holding_1_post_gc',1),('holding_2_post_gc',2),
              ('holding_8_post_gc',8),('released_post_gc',0)]
    assert [(x['phase'],x['registries_held']) for x in rows]==expected
    assert [x['providers_per_registry'] for x in rows]==[0,219,219,219,219,0]
    assert all(type(x['heap_alloc_bytes']) is int and x['heap_alloc_bytes']>0 for x in rows)
    return dict(metrics,checkpoints=rows)
revisions=dict(before='f18fe2290a5c1037aab21bd3201ce20bf09c9862',after='1f1b3dccdac5f4311b0f877902e4834f28d56208')
rows=paired_samples({side:s/f'catalog-retention-{side}' for side in revisions},revisions,3,measure)
print(json.dumps(dict(samples=rows,probe_sha256=digest(r/'catalog-retention.go'),process_helper_sha256=digest(r/'catalog_process.py')),indent=2))
PY
```

## Startup and fixed-count lookup/list CPU

```bash
python3 "$RESEARCH/catalog-startup.py" \
  --before "$EVENER_SCRATCH_DIR/catalog-startup-before" \
  --after "$EVENER_SCRATCH_DIR/catalog-startup-after" \
  --before-revision "$BASE" --after-revision "$CANDIDATE" --pairs 3

# Sensitivity control (#4799): identical pairs with the driver holding 40 MiB
# of parent-only memory. The reported peaks stay within the run-to-run spread
# of several MiB (the recorded pad-0 and pad-40 runs moved one reading by
# 6.8 MiB); only the rusage diagnostic shows the inherited-image floor.
python3 "$RESEARCH/catalog-startup.py" \
  --before "$EVENER_SCRATCH_DIR/catalog-startup-before" \
  --after "$EVENER_SCRATCH_DIR/catalog-startup-after" \
  --before-revision "$BASE" --after-revision "$CANDIDATE" --pairs 3 --parent-pad-mib 40

for case in resolve model-ids; do
  python3 "$RESEARCH/catalog-benchmark-cpu.py" \
    --before "$EVENER_SCRATCH_DIR/catalog-benchmark-before" \
    --after "$EVENER_SCRATCH_DIR/catalog-benchmark-after" \
    --before-revision "$BASE" --after-revision "$CANDIDATE" \
    --benchmark-source "$EVENER_SCRATCH_DIR/catalog-layer_rows_test.go" \
    --benchmark "BenchmarkCatalogLookupAndList/$case" --iterations 100000 --pairs 3
done
```

The longer list follow-up uses `--benchmark BenchmarkCatalogLookupAndList/model-ids --iterations 1000000 --pairs 5` with the same binaries and remaining arguments. Warm construction samples use `GOMAXPROCS=2 <benchmark-binary> -test.run='^$' -test.bench='^BenchmarkLoadEmbeddedWarm$' -test.benchmem -test.benchtime=100x -test.count=5` from each checkout.

## Driver checks

```bash
PYTHONDONTWRITEBYTECODE=1 python3 "$RESEARCH/catalog-startup_test.py"
PYTHONDONTWRITEBYTECODE=1 python3 "$RESEARCH/catalog-benchmark-cpu_test.py"
```

These tests exercise real finite fixture subprocesses and rejection paths. They do not prove the Go workload's behavior; the product probes and raw samples provide that separate evidence. All helpers are standard-library-only and run offline. CPU counters are child user+system time, not parent CPU or wall time. The reported startup peak is the probe's self-reported executed-image VmHWM in KiB (multiplied by 1024 for bytes); wait4's ru_maxrss survives exec and is floored by the launcher's image, so it is retained only as a diagnostic (#4799).
