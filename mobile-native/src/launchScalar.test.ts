import { expect, it } from "vitest";
import type { LaunchOption } from "../../cmd/evener-hub/frontend/src/protocol/types.gen";
import { assertLaunchFieldCurrent, parseLaunchScalar } from "./launchScalar";

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
it("refuses to apply an open field over a newer value", () => {
  expect(() => assertLaunchFieldCurrent("", "9", undefined)).toThrow();
  expect(() => assertLaunchFieldCurrent("3", "9", 8)).toThrow();
  expect(() => assertLaunchFieldCurrent("false", "true", false)).toThrow();
});
it("allows intentional edits against an unchanged field baseline", () => {
  expect(() => assertLaunchFieldCurrent("3", "3", 8)).not.toThrow();
  expect(() => assertLaunchFieldCurrent("3", "3", undefined)).not.toThrow();
});
it("accepts a field that another client already changed to the desired value", () => {
  expect(() => assertLaunchFieldCurrent("3", "8", 8)).not.toThrow();
  expect(() => assertLaunchFieldCurrent("true", "false", false)).not.toThrow();
  expect(() => assertLaunchFieldCurrent("3", "", undefined)).not.toThrow();
});
