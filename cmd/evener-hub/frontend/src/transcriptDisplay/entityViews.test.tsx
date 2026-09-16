import type { EntityView } from "@evener/appwire-client";
import { cleanup, render, screen } from "@testing-library/react";
import { afterEach, expect, test } from "vitest";
import { EntityViewsProvider, useEntityViews } from "./entityViews";

afterEach(cleanup);

function Probe() {
  const entities = useEntityViews();
  return <output data-testid="entity-probe" data-count={String(entities?.size ?? -1)} />;
}

test("returns undefined when no provider owns the map", () => {
  render(<Probe />);
  expect(screen.getByTestId("entity-probe").getAttribute("data-count")).toBe("-1");
});

test("delivers the map and follows a replacement", () => {
  const first = new Map<string, EntityView>([["job_a", {} as EntityView]]);
  const second = new Map<string, EntityView>([
    ["job_a", {} as EntityView],
    ["job_b", {} as EntityView],
  ]);
  const { rerender } = render(
    <EntityViewsProvider entities={first}>
      <Probe />
    </EntityViewsProvider>,
  );
  expect(screen.getByTestId("entity-probe").getAttribute("data-count")).toBe("1");

  rerender(
    <EntityViewsProvider entities={second}>
      <Probe />
    </EntityViewsProvider>,
  );
  expect(screen.getByTestId("entity-probe").getAttribute("data-count")).toBe("2");
});
