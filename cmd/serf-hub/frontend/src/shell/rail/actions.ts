// actions.ts wraps the REST endpoints the rail's row menu drives:
// favorite/rename/archive/delete-project. Each request shape here is
// copied from its Go handler's own doc comment/body struct
// (cmd/serf-hub/web_api_{favorite,rename,archive,project_delete}.go) - see
// each function's own comment for exactly which handler it targets. No
// optimistic UI: every caller is expected to refetch (treeStore.refresh())
// on success and toast on rejection, per this task's scope.
import type { PinSectionSummary } from "../../stores/tree";

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

async function requestJSON<T>(url: string, init: RequestInit): Promise<T> {
  const res = await fetch(url, { credentials: "same-origin", ...init });
  if (!res.ok) throw new Error(await parseErrorBody(res));
  return (await res.json()) as T;
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

export interface SessionPinAssignment {
  session_ref: string;
  section: PinSectionSummary;
}

export interface SessionPinMutationResponse {
  ok: true;
  changed: boolean;
  assignment: SessionPinAssignment;
}

export async function listPinSections(): Promise<PinSectionSummary[]> {
  return requestJSON<PinSectionSummary[]>("/api/pin-sections", {});
}

export async function assignSessionPin(
  ref: string,
  target: { section_id: string } | { section_name: string },
): Promise<SessionPinMutationResponse> {
  return requestJSON<SessionPinMutationResponse>("/api/session-pin", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ session_ref: ref, ...target }),
  });
}

export async function unpinSession(ref: string): Promise<{ ok: true; changed: boolean }> {
  const response = await requestJSON<SessionPinMutationResponse>(`/api/session-pin?ref=${encodeURIComponent(ref)}`, {
    method: "DELETE",
  });
  return { ok: response.ok, changed: response.changed };
}

export async function renamePinSection(id: string, name: string): Promise<PinSectionSummary> {
  const response = await requestJSON<{ ok: true; changed: boolean; section: PinSectionSummary }>(
    `/api/pin-sections/${encodeURIComponent(id)}`,
    {
      method: "PATCH",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ name }),
    },
  );
  return response.section;
}

export async function deletePinSection(id: string): Promise<{ ok: true; changed: boolean; member_count: number }> {
  return requestJSON(`/api/pin-sections/${encodeURIComponent(id)}`, { method: "DELETE" });
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

/** POST /api/sessions/{ref}/delete. No body - the ref in the URL is the only
 * input the handler reads. Destructive - removes one ended or crashed local
 * session's artifacts, decisions, and rendezvous records without touching
 * project siblings (cmd/serf-hub/web_api_session_delete.go). Same response
 * shape as deleteProject: a live or concurrently-reserved target resolves
 * with itself in `skipped` rather than rejecting - only a validation or
 * server error (400/500) rejects. */
export async function deleteSession(ref: string): Promise<ProjectDeleteResult> {
  return postJSON<ProjectDeleteResult>(`/api/sessions/${encodeURIComponent(ref)}/delete`, {});
}
