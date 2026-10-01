import { hydrateThread } from "@evener/appwire-client";
import { act, cleanup, render, screen } from "@testing-library/react";
import { Profiler } from "react";
import { afterEach, expect, test, vi } from "vitest";
import { activityThread } from "../../stores/sessionActivityTestUtils";
import { threadsStore } from "../../stores/threads";
import { ScopeCrumbs } from "./ScopeCrumbs";
import { installFocusedScope, summaryOf } from "./scopeTestUtils";

afterEach(() => {
  cleanup();
  threadsStore.setState({ threads: new Map() });
});

test("scope labels use known compact thread names and exact navigation titles without changing ancestry refs", () => {
  installFocusedScope("remote:child", summaryOf({ ref: "remote:root", title: "Root title" }));
  threadsStore.setState({
    threads: new Map([
      [
        "remote:child",
        { ...hydrateThread(activityThread("remote:child"), "remote:child", 1000), name: "Inspect storage" },
      ],
    ]),
  });
  render(
    <ScopeCrumbs
      path={[
        { ref: "remote:root", title: "opaque-root" },
        { ref: "remote:child", title: "full prompt" },
      ]}
      hierarchy
    />,
  );
  expect(screen.getByRole("button", { name: "Root title" })).toBeTruthy();
  expect(screen.getByText("Inspect storage").getAttribute("aria-current")).toBe("page");
  expect(screen.queryByText("full prompt")).toBeNull();
});

test("scope titles ignore model updates with unchanged names and still follow renames", () => {
  installFocusedScope("remote:child");
  const child = { ...hydrateThread(activityThread("remote:child"), "remote:child", 1000), name: "Inspect storage" };
  const other = hydrateThread(activityThread("remote:other"), "remote:other", 1000);
  threadsStore.setState({
    threads: new Map([
      [child.ref, child],
      [other.ref, other],
    ]),
  });
  const committed = vi.fn();
  render(
    <Profiler id="scope" onRender={committed}>
      <ScopeCrumbs path={[{ ref: child.ref, title: "Child fallback" }]} />
    </Profiler>,
  );
  const mounted = committed.mock.calls.length;
  act(() =>
    threadsStore.setState({
      threads: new Map([
        [child.ref, { ...child, humanNote: "Updated note" }],
        [other.ref, other],
      ]),
    }),
  );
  expect(committed).toHaveBeenCalledTimes(mounted);
  act(() =>
    threadsStore.setState({
      threads: new Map([
        [child.ref, child],
        [other.ref, { ...other, name: "Other rename" }],
      ]),
    }),
  );
  expect(committed).toHaveBeenCalledTimes(mounted);
  act(() =>
    threadsStore.setState({
      threads: new Map([
        [child.ref, { ...child, name: "Inspect indexing" }],
        [other.ref, other],
      ]),
    }),
  );
  expect(screen.getByText("Inspect indexing")).toBeTruthy();
  expect(committed.mock.calls.length).toBeGreaterThan(mounted);
  act(() =>
    threadsStore.setState({
      threads: new Map([
        [child.ref, { ...child, name: "" }],
        [other.ref, other],
      ]),
    }),
  );
  expect(screen.getByText("Child fallback")).toBeTruthy();
});

test("an unhydrated public alias keeps its own fallback instead of borrowing another ref's name", () => {
  installFocusedScope("public:child", summaryOf({ ref: "remote:child", session_id: "child", title: "Remote title" }));
  const remote = { ...hydrateThread(activityThread("remote:child"), "remote:child", 1000), name: "Remote name" };
  threadsStore.setState({ threads: new Map([[remote.ref, remote]]) });
  render(<ScopeCrumbs path={[{ ref: "public:child", title: "Public child" }]} />);
  expect(screen.getByText("Public child").getAttribute("aria-current")).toBe("page");
  expect(screen.queryByText("Remote name")).toBeNull();
  expect(screen.queryByText("Remote title")).toBeNull();
});
