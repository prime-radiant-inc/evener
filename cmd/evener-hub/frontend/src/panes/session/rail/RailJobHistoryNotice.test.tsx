// @vitest-environment jsdom

// The degraded-history presentation is the user-visible half of the contract:
// a complete projection stays silent, and every incomplete one is announced
// with a control that retries the lane it reports.
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, test, vi } from "vitest";
import { RailJobHistoryNotice } from "./RailJobHistoryNotice";
import type { RailJobHistoryStatus } from "./railSummary";

afterEach(cleanup);

describe("RailJobHistoryNotice", () => {
  test("complete job history renders no notice", () => {
    const { container } = render(
      <RailJobHistoryNotice jobHistory={{ status: "complete", revision: 3 }} onRefresh={() => {}} />,
    );
    expect(container.querySelector("[data-job-history]")).toBeNull();
  });

  test("no projection yet renders no notice", () => {
    const { container } = render(<RailJobHistoryNotice jobHistory={null} onRefresh={() => {}} />);
    expect(container.querySelector("[data-job-history]")).toBeNull();
  });

  test.each(["degraded", "fatal"] satisfies RailJobHistoryStatus[])(
    "a %s job lane is announced and offers a retry",
    (status) => {
      const refresh = vi.fn();
      render(<RailJobHistoryNotice jobHistory={{ status, revision: 3 }} onRefresh={refresh} />);

      const notice = screen.getByRole("status");
      expect(notice.getAttribute("data-job-history")).toBe(status);

      fireEvent.click(screen.getByRole("button"));
      expect(refresh).toHaveBeenCalledTimes(1);
    },
  );

  test("an incomplete job lane is announced without a retry that cannot advance a bounded page", () => {
    render(<RailJobHistoryNotice jobHistory={{ status: "incomplete", revision: null }} onRefresh={() => {}} />);

    expect(screen.getByRole("status").getAttribute("data-job-history")).toBe("incomplete");
    expect(screen.queryByRole("button")).toBeNull();
  });
});
