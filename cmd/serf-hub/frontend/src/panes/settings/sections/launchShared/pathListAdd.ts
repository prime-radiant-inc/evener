// The one add-a-path-to-a-pathList decision, shared by both surfaces that own
// a pathList field: the settings LaunchConfigForm (collectionFields.tsx's
// PathListField) and the spawn pane's Advanced options (AdvancedOptions.tsx's
// PathListControl). Both feed the same wire fields (skillsDirs/pluginDirs/
// mcpConfigs) to the same daemon, so both gate an add the same way instead of
// one of them trusting whatever text the picker produced.
import type { LaunchOption } from "../../../../protocol/types.gen";
import { schemaPathKind } from "./schema";

/** The subset of serf/path/validate's response this decision reads. `path` is
 * the server-canonicalized path when the server rewrites one; a caller whose
 * closure doesn't forward it simply keeps the user's own input. */
export interface PathValidation {
  valid: boolean;
  error?: string;
  path?: string;
}

export type PathListAddOutcome = { ok: true; value: string } | { ok: false; error: string };

/**
 * Decides whether `trimmed` may join `items`, and under what spelling.
 *
 * Order matters: a duplicate is rejected without spending an RPC, then the
 * path is validated server-side, then the accepted spelling (the
 * server-canonicalized one when there is one) is re-checked for duplication -
 * two different spellings of one directory canonicalize to the same string,
 * and the list is keyed by its own values.
 *
 * A validator that REJECTS blocks the add and surfaces its error; a validator
 * that FAILS (rejected promise - the RPC itself broke) never blocks, matching
 * the scalar path fields' own fail-open behavior and keeping a dead socket from
 * wedging the add row.
 */
export async function validatePathListAdd(
  option: LaunchOption,
  items: readonly string[],
  trimmed: string,
  validatePath: (path: string, kind: string) => Promise<PathValidation>,
): Promise<PathListAddOutcome> {
  if (items.includes(trimmed)) return { ok: false, error: "Already added." };

  let result: PathValidation;
  try {
    result = await validatePath(trimmed, schemaPathKind(option.pathKind));
  } catch {
    return { ok: true, value: trimmed };
  }
  if (!result.valid) return { ok: false, error: result.error || "invalid path" };

  const value = result.path || trimmed;
  if (value !== trimmed && items.includes(value)) return { ok: false, error: "Already added." };
  return { ok: true, value };
}
