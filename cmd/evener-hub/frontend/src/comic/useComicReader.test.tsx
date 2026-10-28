import { act, renderHook } from "@testing-library/react";
import { type ReactNode, StrictMode } from "react";
import { afterEach, expect, test, vi } from "vitest";
import { readComicPosition, readComicPositionRecord } from "./readerPosition";
import { useComicReader } from "./useComicReader";

afterEach(() => {
  sessionStorage.clear();
  vi.restoreAllMocks();
});
test("retained seats keep distinct semantic points across presentation unmounts", () => {
  const a = {};
  const b = {};
  const firstPosition = {
    mode: "away" as const,
    sessionRef: "local:A",
    path: ["child"],
    entryId: "first",
    offset: 12,
    viewportOffset: 24,
  };
  const secondPosition = {
    mode: "away" as const,
    sessionRef: "local:A",
    path: [],
    entryId: "second",
    offset: 5,
    viewportOffset: 10,
  };
  const first = renderHook(() => useComicReader("local:A", a));
  const second = renderHook(() => useComicReader("local:A", b));
  act(() => first.result.current.save(firstPosition));
  act(() => second.result.current.save(secondPosition));
  first.unmount();
  second.unmount();
  const returned = renderHook(() => useComicReader("local:A", a));
  expect(returned.result.current.position).toEqual(firstPosition);
  returned.unmount();
  const reloaded = renderHook(() => useComicReader("local:A", {}));
  expect(reloaded.result.current.position).toEqual(secondPosition);
  reloaded.unmount();
});

test.each(["local", "remote"])(
  "%s positions retain logical ownership while Comic and the model are absent",
  (source) => {
    const ref = `${source}:workspace`;
    const logical = `${source}:original`;
    const seats = [{}, {}];
    const firstPosition = {
      mode: "away" as const,
      sessionRef: `${source}:child`,
      path: ["delegate"],
      entryId: "first",
      offset: 7,
      viewportOffset: 24,
    };
    const secondPosition = { ...firstPosition, entryId: "second", offset: 13 };
    const first = renderHook(() => useComicReader(ref, seats[0], logical));
    const second = renderHook(() => useComicReader(ref, seats[1], logical));
    act(() => first.result.current.save(firstPosition));
    act(() => second.result.current.save(secondPosition));
    first.unmount();
    second.unmount();
    const writes = vi.spyOn(Storage.prototype, "setItem");
    const returned = renderHook(({ session }: { session?: string }) => useComicReader(ref, seats[0], session), {
      initialProps: { session: undefined as string | undefined },
    });
    expect(returned.result.current.position).toEqual(firstPosition);
    returned.rerender({ session: logical });
    const reader = returned.result.current.reader;
    expect(returned.result.current.position).toEqual(firstPosition);
    returned.rerender({ session: undefined });
    expect(returned.result.current.reader).toBe(reader);
    expect(reader.alive).toBe(true);
    expect(writes).not.toHaveBeenCalled();
    expect(readComicPosition(ref)).toEqual(secondPosition);
    returned.unmount();

    // No retained seat: durable ownership alone fences a Clear that happened
    // while Standard was mounted, once the current model becomes available.
    const reloaded = renderHook(({ session }: { session?: string }) => useComicReader(ref, undefined, session), {
      initialProps: { session: undefined as string | undefined },
    });
    expect(reloaded.result.current.position).toEqual(secondPosition);
    expect(writes).not.toHaveBeenCalled();
    reloaded.rerender({ session: `${source}:replacement` });
    const end = { mode: "end", sessionRef: ref, path: [] };
    expect(reloaded.result.current.position).toEqual(end);
    expect(readComicPositionRecord(ref)).toEqual({ ownerRef: `${source}:replacement`, position: end });
    reloaded.unmount();
    const retainedAfterClear = renderHook(() => useComicReader(ref, seats[0], `${source}:replacement`));
    expect(retainedAfterClear.result.current.position).toEqual(end);
    retainedAfterClear.unmount();
  },
);

test("commits one standalone lifetime and retires it on ref change", () => {
  const mounted = renderHook(({ ref }) => useComicReader(ref), {
    initialProps: { ref: "local:A" },
    wrapper: ({ children }: { children: ReactNode }) => <StrictMode>{children}</StrictMode>,
  });
  const first = mounted.result.current.reader;
  expect(first.membership).toBe("standalone");
  expect(first.alive).toBe(true);
  expect(first.readable).toBe(true);
  mounted.rerender({ ref: "local:A" });
  expect(mounted.result.current.reader).toBe(first);
  act(() =>
    mounted.result.current.save({
      mode: "away",
      sessionRef: "local:A",
      path: [],
      entryId: "entry",
      offset: 12,
      viewportOffset: 24,
    }),
  );
  expect(readComicPosition("local:A")).toMatchObject({ entryId: "entry", offset: 12 });
  mounted.rerender({ ref: "local:B" });
  expect(first.alive).toBe(false);
  const second = mounted.result.current.reader;
  expect(second).not.toBe(first);
  expect(mounted.result.current.position).toBeUndefined();
  mounted.unmount();
  expect(second.alive).toBe(false);
});
