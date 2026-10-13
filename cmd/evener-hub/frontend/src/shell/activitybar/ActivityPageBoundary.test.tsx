import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { ActivityPageBoundary } from "./ActivityPageBoundary";

const base = {
  resource: "delegates" as const,
  label: "subagents",
  rows: [] as unknown[],
  error: null,
  permanent: false,
};

describe("ActivityPageBoundary", () => {
  it("keeps its label through a background refresh and reports only a page it started", async () => {
    let resolve: (() => void) | undefined;
    const loadMore = vi.fn(
      () =>
        new Promise<void>((done) => {
          resolve = done;
        }),
    );
    render(<ActivityPageBoundary {...base} hasMore loading loadMore={loadMore} />);
    expect(screen.queryByRole("button", { name: /Loading subagents/ })).toBeNull();
    const button = screen.getByRole("button", { name: "Load more subagents" });
    expect((button as HTMLButtonElement).disabled).toBe(false);
    fireEvent.click(button);
    expect(await screen.findByRole("button", { name: /Loading subagents/ })).toBeTruthy();
    await act(async () => resolve?.());
    await waitFor(() => expect(screen.getByRole("button", { name: "Load more subagents" })).toBeTruthy());
    expect(loadMore).toHaveBeenCalledTimes(1);
  });

  it("shows nothing when the collection has no more pages", () => {
    render(<ActivityPageBoundary {...base} hasMore={false} loading={false} loadMore={vi.fn()} />);
    expect(screen.queryByRole("button")).toBeNull();
  });
});
