import type { HostRow } from "@evener/appwire-client";

/** One host registry row, detached unless `overrides` says otherwise. */
export function hostRow(overrides: Partial<HostRow> & Pick<HostRow, "name">): HostRow {
  return {
    generation: 1,
    incarnationId: "inc-1",
    origin: "hub.toml",
    attached: false,
    midAttach: false,
    removed: false,
    ...overrides,
  };
}

/** One attached host registry row - the same shape stores/hosts.ts publishes
 * in its ready snapshot. */
export function registryRow(overrides: Partial<HostRow> & Pick<HostRow, "name">): HostRow {
  return hostRow({ attached: true, ...overrides });
}
