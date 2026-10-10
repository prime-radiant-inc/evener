#!/bin/sh
# Budget native's isolated fork workers from spare machine capacity.
set -eu

repo_root=$(CDPATH='' cd -- "$(dirname "$0")/../.." && pwd)
budgets=$repo_root/scripts/lib/gate-budgets.sh
if [ ! -r "$budgets" ]; then
	echo "evener-native: cannot read $budgets; refusing to run the test gate unbudgeted" >&2
	exit 1
fi
. "$budgets"
gate_source_helper "$repo_root/scripts/lib/load-aware-workers.sh"
# Fork isolation keeps each file independent even when only one worker fits.
exec vitest run "--maxWorkers=$(gate_budget 4 4)" "$@"
