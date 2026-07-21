// actions.ts wraps the REST endpoints the rail's row menu drives:
// favorite/rename/archive/delete-project. Each request shape here is
// copied from its Go handler's own doc comment/body struct
// (cmd/serf-hub/web_api_{favorite,rename,archive,project_delete}.go) - see
// each function's own comment for exactly which handler it targets. No
// optimistic UI: every caller is expected to refetch (treeStore.refresh())
// on success and toast on rejection, per this task's scope.
export interface ProjectDeleteResult {
  deleted: string[];
  skipped: { id: string; reason: string }[];
}

async function parseErrorBody(res: Response): Promise<string> {
  try {
    const data = (await res.json()) as { error?: string };
    if (typeof data.error === "string" && data.error !== "") return data.error;
  } catch {
    // non-JSON (or empty) error body: fall through to the status line
  }
  return `${res.status} ${res.statusText}`;
}

// postJSON POSTs `body` as JSON and returns the parsed JSON response - except
// for a 204 No Content (handleAPIRename's success path), which resolves to
// undefined rather than attempting to parse an empty body as JSON.
async function postJSON<T>(url: string, body: unknown): Promise<T> {
  const res = await fetch(url, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    credentials: "same-origin",
    body: JSON.stringify(body),
  });
  if (!res.ok) throw new Error(await parseErrorBody(res));
  if (res.status === 204) return undefined as T;
  return (await res.json()) as T;
}

/** POST /api/favorite. Body: {kind, id, favorited}. */
export async function setFavorite(kind: "session" | "project", id: string, favorited: boolean): Promise<void> {
  await postJSON("/api/favorite", { kind, id, favorited });
}

/** POST /api/sessions/{ref}/rename. Body: {name}. ref is URL-escaped - the
 * dispatcher (handleAPISession) url.PathUnescape()s the first path segment
 * after /api/sessions/, which is how a ref containing "/" or ":" survives
 * routing intact. */
export async function renameSession(ref: string, name: string): Promise<void> {
  await postJSON(`/api/sessions/${encodeURIComponent(ref)}/rename`, { name });
}

/** POST /api/archive. Body: {kind, id, archived, working_dir?}. working_dir
 * is required server-side for kind="project" (validated against identifier
 * .ResolveProject) and ignored for kind="session" - omitted here rather
 * than sent as undefined so a session archive body carries only the fields
 * the handler actually reads. */
export async function setArchived(
  kind: "session" | "project",
  id: string,
  archived: boolean,
  workingDir?: string,
): Promise<void> {
  const body: { kind: string; id: string; archived: boolean; working_dir?: string } = { kind, id, archived };
  if (workingDir !== undefined) body.working_dir = workingDir;
  await postJSON("/api/archive", body);
}

/** POST /api/project/delete. Body: {key, working_dir}. Destructive -
 * removes every session file under the project. Rejects (409, surfaced as
 * a thrown Error carrying the handler's message) when anything in the
 * project is still live. */
export async function deleteProject(key: string, workingDir: string): Promise<ProjectDeleteResult> {
  return postJSON<ProjectDeleteResult>("/api/project/delete", { key, working_dir: workingDir });
}
