import type { ThreadModel } from "@evener/appwire-client";
import { type ReactNode, useCallback, useLayoutEffect, useMemo, useState } from "react";
import { type UseTranscriptResult, useTranscriptReader } from "../panes/session/transcript/useTranscript";
import { useConnectionStore } from "../stores/connection";
import { useThreadsStore } from "../stores/threads";
import { useThreadModel } from "../stores/useThreadModel";
import type { ComicDelegateStrip } from "./delegates";
import { type ComicEntry, projectComicConversation } from "./projection";
import type { ComicReadingPosition } from "./readerPosition";
import { useComicActivity } from "./useComicActivity";
import { useComicReader } from "./useComicReader";
export type ComicReaderSnapshot = {
  ref: string;
  path: readonly string[];
  entries: readonly ComicEntry[];
  history: UseTranscriptResult;
  activity: ReturnType<typeof useComicActivity>;
};
export type ComicChildAvailability = "loading" | "repairing" | "disconnected" | "unavailable" | "ready";
export type ComicSubchatState = {
  readers: ReadonlyMap<string, ComicReaderSnapshot>;
  availability?: ReadonlyMap<string, ComicChildAvailability>;
  expandedPaths: readonly (readonly string[])[];
  expand(parentPath: readonly string[], strip: ComicDelegateStrip): void;
  collapse(path: readonly string[]): void;
};
type Node = { path: readonly string[]; strip: ComicDelegateStrip };
export const comicPathKey = (path: readonly string[]) => JSON.stringify(path);
export const comicPathContains = (branch: readonly string[], path: readonly string[]) =>
  branch.length <= path.length && branch.every((key, index) => path[index] === key);

/** The returned components belong above virtual mounts. Only explicit expansion acquires reads. */
export function useComicSubchats(
  rootRef: string,
  _restore: ComicReadingPosition | undefined,
): { state: ComicSubchatState; lifetimes: ReactNode } {
  const [nodes, setNodes] = useState<readonly Node[]>([]);
  const [readers, setReaders] = useState<ReadonlyMap<string, ComicReaderSnapshot>>(new Map());
  const [availability, setAvailability] = useState<ReadonlyMap<string, ComicChildAvailability>>(new Map());
  // biome-ignore lint/correctness/useExhaustiveDependencies: a different selected root retires every expanded lifetime
  useLayoutEffect(() => {
    setNodes([]);
    setReaders(new Map());
    setAvailability(new Map());
  }, [rootRef]);
  const publishAvailability = useCallback((key: string, value: ComicChildAvailability | undefined) => {
    setAvailability((before) => {
      if (before.get(key) === value) return before;
      const next = new Map(before);
      if (value) next.set(key, value);
      else next.delete(key);
      return next;
    });
  }, []);
  const publish = useCallback((key: string, snapshot: ComicReaderSnapshot | undefined) => {
    setReaders((before) => {
      const next = new Map(before);
      if (snapshot) next.set(key, snapshot);
      else next.delete(key);
      return next;
    });
  }, []);
  const expand = useCallback(
    (parentPath: readonly string[], strip: ComicDelegateStrip) => {
      setNodes((before) => {
        const parent = before.find((node) => comicPathKey(node.path) === comicPathKey(parentPath));
        if ((parentPath.length ? parent?.strip.childRef : rootRef) !== strip.ownerRef) return before;
        const ancestors = before.filter((node) => comicPathContains(node.path, parentPath));
        if (
          strip.childRef === rootRef ||
          ancestors.some((node) => node.strip.key === strip.key || node.strip.childRef === strip.childRef)
        )
          return before;
        const path = [...parentPath, strip.key];
        if (before.some((node) => comicPathKey(node.path) === comicPathKey(path))) return before;
        return [...before, { path, strip }];
      });
    },
    [rootRef],
  );
  const collapse = useCallback((path: readonly string[]) => {
    setNodes((before) => before.filter((node) => !comicPathContains(path, node.path)));
  }, []);
  const state = useMemo(
    () => ({ readers, availability, expandedPaths: nodes.map((node) => node.path), expand, collapse }),
    [readers, availability, nodes, expand, collapse],
  );
  return {
    state,
    lifetimes: nodes.map((node) => (
      <ChildLifetime
        key={comicPathKey(node.path)}
        node={node}
        publish={publish}
        publishAvailability={publishAvailability}
      />
    )),
  };
}

function ChildLifetime({
  node,
  publish,
  publishAvailability,
}: {
  node: Node;
  publish(key: string, snapshot: ComicReaderSnapshot | undefined): void;
  publishAvailability(key: string, value: ComicChildAvailability | undefined): void;
}) {
  const ref = node.strip.childRef;
  const model = useThreadModel(ref);
  const reader = useComicReader(ref);
  const history = useTranscriptReader(ref, reader.reader);
  const deleted = useThreadsStore((state) => state.deletedRefs.has(ref));
  const repairing = useThreadsStore((state) => state.repairingRefs.has(ref));
  const connection = useConnectionStore((state) => state.state);
  const availability: ComicChildAvailability = deleted
    ? "unavailable"
    : repairing
      ? "repairing"
      : connection !== "ready"
        ? "disconnected"
        : model
          ? "ready"
          : "loading";
  const key = comicPathKey(node.path);
  useLayoutEffect(() => publishAvailability(key, availability), [key, availability, publishAvailability]);
  useLayoutEffect(() => () => publishAvailability(key, undefined), [key, publishAvailability]);
  return model ? <ChildSnapshot node={node} model={model} history={history} publish={publish} /> : null;
}
function ChildSnapshot({
  node,
  model,
  history,
  publish,
}: {
  node: Node;
  model: ThreadModel;
  history: UseTranscriptResult;
  publish(key: string, snapshot: ComicReaderSnapshot | undefined): void;
}) {
  const activity = useComicActivity(node.strip.childRef, model);
  const entries = useMemo(
    () => projectComicConversation(model, { ...activity.context, assistant: node.strip.persona }),
    [model, activity.context, node.strip.persona],
  );
  const key = comicPathKey(node.path);
  // The shared hook creates a fire-and-forget closure on each render. Its stable
  // loadOlder owner and observable fields, not closure allocation, publish state.
  // biome-ignore lint/correctness/useExhaustiveDependencies: stable paging owner plus all observable history fields
  useLayoutEffect(() => {
    publish(key, { ref: node.strip.childRef, path: node.path, entries, history, activity });
  }, [
    key,
    node,
    entries,
    activity,
    history.model,
    history.hasOlder,
    history.loadingOlder,
    history.olderError,
    history.loadOlder,
    history.cancelOlder,
    publish,
  ]);
  useLayoutEffect(() => () => publish(key, undefined), [key, publish]);
  return null;
}
