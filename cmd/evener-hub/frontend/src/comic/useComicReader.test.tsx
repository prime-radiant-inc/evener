import { act, renderHook } from "@testing-library/react";
import { type ReactNode, StrictMode } from "react";
import { afterEach, expect, test } from "vitest";
import { readComicPosition } from "./readerPosition";
import { useComicReader } from "./useComicReader";

afterEach(() => sessionStorage.clear());
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
