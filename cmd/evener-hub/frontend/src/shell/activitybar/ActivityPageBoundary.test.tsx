import { act, render, screen, waitFor } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { ActivityPageBoundary } from "./ActivityPageBoundary";
import { ActivityBoundaryObserver } from "./activityPagingTestUtils";

const base = {
  resource: "delegates" as const,
  rows: [] as unknown[],
  error: null,
  permanent: false,
};

describe("ActivityPageBoundary", () => {
  it("marks an unfinished page with placeholders instead of a manual control", () => {
    const loadMore = vi.fn(() => Promise.resolve());
    render(<ActivityPageBoundary {...base} hasMore loading={false} loadMore={loadMore} />);
    expect(screen.queryByRole("button")).toBeNull();
    expect(screen.getByRole("status", { name: "Loading" })).toBeTruthy();
    expect(screen.getAllByTestId("skeleton-line")).toHaveLength(1);
    expect(loadMore).not.toHaveBeenCalled();
  });

  it("shows nothing when the collection has no more pages", () => {
    render(<ActivityPageBoundary {...base} hasMore={false} loading={false} loadMore={vi.fn()} />);
    expect(screen.queryByRole("button")).toBeNull();
    expect(screen.queryByRole("status", { name: "Loading" })).toBeNull();
  });

  it("does not present a permanent page failure as loading", () => {
    render(<ActivityPageBoundary {...base} hasMore loading={false} permanent loadMore={vi.fn()} />);
    expect(screen.queryByRole("status", { name: "Loading" })).toBeNull();
  });

  it("waits for the moved boundary to become visible after new rows arrive", async () => {
    vi.stubGlobal("IntersectionObserver", ActivityBoundaryObserver);
    const loadMore = vi.fn(() => Promise.resolve());
    const firstRows = [{ id: "first" }];
    const view = render(
      <ActivityPageBoundary {...base} rows={firstRows} hasMore loading={false} loadMore={loadMore} />,
    );
    await act(async () => ActivityBoundaryObserver.intersect());
    expect(loadMore).toHaveBeenCalledTimes(1);
    view.rerender(
      <ActivityPageBoundary
        {...base}
        rows={[...firstRows, { id: "second" }]}
        hasMore
        loading={false}
        loadMore={loadMore}
      />,
    );
    expect(loadMore).toHaveBeenCalledTimes(1);
    await act(async () => ActivityBoundaryObserver.intersect());
    await waitFor(() => expect(loadMore).toHaveBeenCalledTimes(2));
  });
});
