# Hub Web UI data-path corrections

Plan: `docs/superpowers/plans/2026-07-15-hub-webui-data-path-corrections.md`

Baseline: `GOCACHE=/tmp/serf-gocache go test ./internal/apptranscript ./server ./cmd/serf-hub/internal/hubcore ./cmd/serf-hub -count=1` (pass)

- Task 1 complete at `8d28be963`: append-aware bounded transcript indexing, persistent recovery, and exact sequential projection semantics. Final independent review: spec compliant, approved, no findings. Focused, package, race, vet, and complete-diff checks pass.
