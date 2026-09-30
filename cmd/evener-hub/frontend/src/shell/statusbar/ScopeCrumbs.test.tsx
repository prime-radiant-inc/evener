import { hydrateThread } from "@evener/appwire-client";
import { cleanup, render, screen } from "@testing-library/react";
import { afterEach, expect, test } from "vitest";
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
