import { expect, it } from "vitest";
import type { LaunchOption } from "../../cmd/evener-hub/frontend/src/protocol/types.gen";
import { parseLaunchScalar } from "./launchScalar";

const option = (kind: string): LaunchOption => ({
  kind,
  field: "fixture",
  wireField: "maxRounds",
  group: "Test",
  label: "Test",
  perLaunch: true,
});
it("distinguishes inherited values from explicit false and zero", () => {
  expect(parseLaunchScalar(option("boolean"), "")).toBeUndefined();
  expect(parseLaunchScalar(option("boolean"), "false")).toBe(false);
  expect(parseLaunchScalar(option("integer"), "0")).toBe(0);
  expect(parseLaunchScalar(option("integer"), "-1")).toBe(-1);
});
it("rejects invalid integers and unavailable enum choices", () => {
  for (const value of ["NaN", "Infinity", "1.5", "not a number"])
    expect(() => parseLaunchScalar(option("integer"), value)).toThrow();
  const select = {
    ...option("select"),
    choices: [
      { value: "on", label: "On" },
      { value: "off", label: "Off", disabled: true },
    ],
  };
  expect(parseLaunchScalar(select, "on")).toBe("on");
  expect(() => parseLaunchScalar(select, "off")).toThrow();
  expect(() => parseLaunchScalar(select, "other")).toThrow();
});
it("trims scalar text like the web collector and never coerces collections", () => {
  expect(parseLaunchScalar(option("text"), " value ")).toBe("value");
  expect(parseLaunchScalar(option("path"), "  ")).toBeUndefined();
  expect(() => parseLaunchScalar(option("envMap"), "A=B")).toThrow();
});
