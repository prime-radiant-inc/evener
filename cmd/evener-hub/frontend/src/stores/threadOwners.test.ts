import { afterEach, describe, expect, it } from "vitest";
import {
  backgroundHolds,
  bumpOwnerGeneration,
  canonicalThreadOwnerActive,
  foregroundHolds,
  heldCountsAtStore,
  holdBackground,
  holdForeground,
  holdHistory,
  nextBackgroundLeaseSequence,
  ownedOnlyByMutationPin,
  ownerGeneration,
  pinMutation,
  releaseBackground,
  releaseForeground,
  releaseHistory,
  resetThreadOwnersForTests,
  unpinMutation,
} from "./threadOwners";

afterEach(() => resetThreadOwnersForTests());

describe("thread owners", () => {
  it("owns a ref while any holder kind holds it", () => {
    expect(canonicalThreadOwnerActive("r")).toBe(false);

    holdForeground("r");
    expect(canonicalThreadOwnerActive("r")).toBe(true);
    releaseForeground("r");

    holdBackground("r");
    expect(canonicalThreadOwnerActive("r")).toBe(true);
    releaseBackground("r");

    holdHistory("r");
    expect(canonicalThreadOwnerActive("r")).toBe(true);
    releaseHistory("r");

    pinMutation("r");
    expect(canonicalThreadOwnerActive("r")).toBe(true);
    unpinMutation("r");

    expect(canonicalThreadOwnerActive("r")).toBe(false);
  });

  it("does not count a background lease as a holder that lifts a clear's suppression", () => {
    holdBackground("r");
    expect(heldCountsAtStore("r")).toBe(false);
    holdForeground("r");
    expect(heldCountsAtStore("r")).toBe(true);
  });

  it("reports a ref owned only by a mutation pin", () => {
    pinMutation("r");
    expect(ownedOnlyByMutationPin("r")).toBe(true);
    holdBackground("r");
    expect(ownedOnlyByMutationPin("r")).toBe(false);
  });

  it("returns the remaining holds on release and never goes negative", () => {
    holdForeground("r");
    holdForeground("r");
    expect(releaseForeground("r")).toBe(1);
    expect(releaseForeground("r")).toBe(0);
    expect(releaseForeground("r")).toBe(0);
    expect(foregroundHolds("r")).toBe(0);
    expect(backgroundHolds("r")).toBe(0);
  });

  it("advances the lease sequence and owner generation per ref", () => {
    expect(nextBackgroundLeaseSequence("a")).toBe(1);
    expect(nextBackgroundLeaseSequence("a")).toBe(2);
    expect(nextBackgroundLeaseSequence("b")).toBe(1);
    bumpOwnerGeneration("a");
    expect(ownerGeneration("a")).toBe(1);
    expect(ownerGeneration("b")).toBe(0);
  });

  it("forgets every holder and counter on reset", () => {
    holdForeground("r");
    holdBackground("r");
    holdHistory("r");
    pinMutation("r");
    bumpOwnerGeneration("r");
    nextBackgroundLeaseSequence("r");
    resetThreadOwnersForTests();
    expect(canonicalThreadOwnerActive("r")).toBe(false);
    expect(ownerGeneration("r")).toBe(0);
    expect(nextBackgroundLeaseSequence("r")).toBe(1);
  });
});
