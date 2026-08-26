import type {
  SearchDocument,
  SessionRecord,
  VoiceStep,
  WorkNode,
} from "./model";
import type { PrototypeState, QuestionAnswerState } from "./state";

export function selectCanMutate(state: PrototypeState): boolean {
  return state.projection.screenState === "ready";
}

export type SessionGroupId = "needs-you" | "running" | "recent";

export interface SessionGroup {
  id: SessionGroupId;
  label: "Needs You" | "Running" | "Recent";
  sessions: readonly SessionRecord[];
}

const sessionGroups = [
  {
    id: "needs-you",
    label: "Needs You",
    states: ["needs-answer", "needs-permission"],
  },
  { id: "running", label: "Running", states: ["running", "waiting"] },
  { id: "recent", label: "Recent", states: ["completed", "failed"] },
] as const;

export function selectGroupedSessions(state: PrototypeState): SessionGroup[] {
  const query = state.sessionQuery.trim().toLowerCase();
  const sessions = state.projection.fixture.sessions.filter((session) =>
    query.length === 0
      ? true
      : `${session.title} ${session.project}`.toLowerCase().includes(query),
  );
  return sessionGroups.flatMap((group) => {
    const grouped = sessions.filter((session) =>
      group.states.some((sessionState) => sessionState === session.state),
    );
    return grouped.length === 0
      ? []
      : [{ id: group.id, label: group.label, sessions: grouped }];
  });
}

export interface SearchResultProjection {
  id: string;
  kind: SearchDocument["kind"];
  title: string;
  context: string;
  sessionId: string;
  focusItemId: string | null;
}

export type SearchProjection =
  | { kind: "prompt"; query: ""; results: readonly [] }
  | {
      kind: "results";
      query: string;
      results: readonly SearchResultProjection[];
    }
  | { kind: "no-results"; query: string; results: readonly [] };

export function selectSearchProjection(
  state: PrototypeState,
): SearchProjection {
  const query = state.globalQuery.trim();
  if (query.length === 0) return { kind: "prompt", query: "", results: [] };
  const normalizedQuery = query.toLowerCase();
  const results = state.projection.fixture.search
    .filter((result) =>
      `${result.title} ${result.body}`.toLowerCase().includes(normalizedQuery),
    )
    .map((result) => ({
      id: result.id,
      kind: result.kind,
      title: result.title,
      context: result.body,
      sessionId: result.sessionId,
      focusItemId: result.itemId,
    }));
  return results.length === 0
    ? { kind: "no-results", query, results: [] }
    : { kind: "results", query, results };
}

export type QuestionResolution = NonNullable<QuestionAnswerState["resolution"]>;

export function selectQuestionSubmitValidity(
  state: PrototypeState,
  questionId: string,
  resolution: QuestionResolution,
): boolean {
  const question = state.projection.fixture.questions.find(
    ({ id }) => id === questionId,
  );
  const answer = state.answers[questionId];
  if (!question || answer?.submitted) return false;
  if (resolution === "fallback") return question.allowFallback;
  if (resolution === "decide") return question.allowDecide;
  if (resolution === "skip") return question.allowSkip;
  const selectionCount = answer?.selectedOptionIds.length ?? 0;
  return question.mode === "single" ? selectionCount === 1 : selectionCount > 0;
}

export function selectNewSessionValidity(state: PrototypeState): boolean {
  const { fixture } = state.projection;
  return (
    fixture.recentProjects.some(
      ({ path }) => path === state.newSession.project,
    ) &&
    state.newSession.prompt.trim().length > 0 &&
    fixture.models.some(({ id }) => id === state.newSession.modelId) &&
    fixture.efforts.includes(state.newSession.effort)
  );
}

export interface WorkHierarchyItem {
  node: WorkNode;
  depth: number;
  parentTitle: string | null;
  children: readonly WorkHierarchyItem[];
}

export function buildWorkHierarchy(
  nodes: readonly WorkNode[],
): readonly WorkHierarchyItem[] {
  const byId = new Map(nodes.map((node) => [node.id, node]));
  const childrenByParent = new Map<string, WorkNode[]>();
  const roots: WorkNode[] = [];
  for (const node of nodes) {
    if (
      node.parentId === null ||
      node.parentId === node.id ||
      !byId.has(node.parentId)
    ) {
      roots.push(node);
      continue;
    }
    const children = childrenByParent.get(node.parentId) ?? [];
    children.push(node);
    childrenByParent.set(node.parentId, children);
  }

  const visited = new Set<string>();
  const buildItem = (
    node: WorkNode,
    depth: number,
    ancestors: ReadonlySet<string>,
  ): WorkHierarchyItem => {
    visited.add(node.id);
    const nextAncestors = new Set(ancestors);
    nextAncestors.add(node.id);
    const children = (childrenByParent.get(node.id) ?? [])
      .filter((child) => !nextAncestors.has(child.id) && !visited.has(child.id))
      .map((child) => buildItem(child, depth + 1, nextAncestors));
    return {
      node,
      depth,
      parentTitle: node.parentId
        ? (byId.get(node.parentId)?.title ?? null)
        : null,
      children,
    };
  };

  const hierarchy = roots.map((node) => buildItem(node, 0, new Set()));
  for (const node of nodes) {
    if (!visited.has(node.id)) {
      hierarchy.push(buildItem(node, 0, new Set()));
    }
  }
  return hierarchy;
}

export function selectCurrentVoiceStep(
  state: PrototypeState,
): VoiceStep | null {
  return state.projection.fixture.voiceSteps[state.voice.stepIndex] ?? null;
}

export function selectCurrentVoiceLevel(state: PrototypeState): number {
  return selectCurrentVoiceStep(state)?.level ?? 0;
}
