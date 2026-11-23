import { act, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { expect, test, vi } from "vitest";
import { type DirectoryListingSource, useDirectoryListing } from "./useDirectoryListing";

function Probe({ source }: { source: DirectoryListingSource }) {
  const listing = useDirectoryListing({ path: "/start", debounceMs: 0, source });
  return (
    <div>
      <output data-testid="entries">{listing.entries === null ? "pending" : listing.entries.join("|")}</output>
      <output data-testid="error">{listing.error ?? ""}</output>
      <button type="button" onClick={() => void listing.browse("/start")}>
        browse start
      </button>
      <button type="button" onClick={() => void listing.browse("/next")}>
        browse next
      </button>
      <button type="button" onClick={() => listing.cancel()}>
        cancel
      </button>
    </div>
  );
}

test("an older listing cannot replace a newer one", async () => {
  let resolveOld!: (paths: string[]) => void;
  const old = new Promise<string[]>((resolve) => {
    resolveOld = resolve;
  });
  const complete = vi.fn((prefix: string) => (prefix === "/start" ? old : Promise.resolve(["/next/child"])));
  render(<Probe source={{ complete }} />);
  const user = userEvent.setup();

  await user.click(screen.getByRole("button", { name: "browse start" }));
  await user.click(screen.getByRole("button", { name: "browse next" }));
  expect(await screen.findByText("/next/child")).toBeTruthy();

  await act(async () => resolveOld(["/start/stale"]));
  expect(screen.getByTestId("entries").textContent).toBe("/next/child");
});

test("cancel drops an in-flight listing, so its late response never lands", async () => {
  let resolveOld!: (paths: string[]) => void;
  const old = new Promise<string[]>((resolve) => {
    resolveOld = resolve;
  });
  const complete = vi.fn(() => old);
  render(<Probe source={{ complete }} />);
  const user = userEvent.setup();

  await user.click(screen.getByRole("button", { name: "browse start" }));
  expect(complete).toHaveBeenCalledExactlyOnceWith("/start", false);
  await user.click(screen.getByRole("button", { name: "cancel" }));
  await act(async () => resolveOld(["/start/stale"]));

  expect(screen.getByTestId("entries").textContent).toBe("pending");
  expect(screen.getByTestId("error").textContent).toBe("");
});
