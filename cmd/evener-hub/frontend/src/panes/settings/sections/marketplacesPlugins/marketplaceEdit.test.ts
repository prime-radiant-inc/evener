// @vitest-environment node
import { describe, expect, test } from "vitest";
import type { MarketplaceEntry } from "../../../../protocol/types.gen";
import {
  MARKETPLACE_SOURCE_OPTIONS,
  marketplaceDraftFor,
  marketplaceDraftIncomplete,
  marketplaceEditParams,
  marketplaceSourceTouched,
} from "./marketplaceEdit";

const GITHUB: MarketplaceEntry = { name: "acme", source: { kind: "github", repo: "acme/plugins" }, lastUpdated: 1 };
const URL: MarketplaceEntry = { name: "acme", source: { kind: "url", url: "https://x/y.git" }, lastUpdated: 1 };
const DIR: MarketplaceEntry = { name: "acme", source: { kind: "directory", path: "/srv/mkt" }, lastUpdated: 1 };
const SUBDIR: MarketplaceEntry = {
  name: "acme",
  source: { kind: "git-subdir", url: "https://x/mono.git", path: "mkt" },
  lastUpdated: 1,
};
// A kind the picker does not offer and this frontend has never heard of,
// standing in for whatever the wire grows next.
const FUTURE: MarketplaceEntry = {
  name: "acme",
  source: { kind: "gitlab", url: "https://gl/x.git", ref: "main" },
  lastUpdated: 1,
};
// The same unofferable kind carrying no url at all, so the only draft it can
// seed is an empty one.
const FUTURE_NO_URL: MarketplaceEntry = { name: "acme", source: { kind: "gitlab", ref: "main" }, lastUpdated: 1 };
// An offered kind pinned to a ref: only something outside this form (the CLI,
// or a hand-edited known_marketplaces.json) can set one, and a ref names a
// branch in the repository it was set for.
const GITHUB_REF: MarketplaceEntry = {
  name: "acme",
  source: { kind: "github", repo: "acme/plugins", ref: "v2" },
  lastUpdated: 1,
};
// The same pins on a url source, one alone and one alongside a ref. A sha
// names a commit in ONE repository, so no other repository has it.
const URL_SHA: MarketplaceEntry = {
  name: "acme",
  source: { kind: "url", url: "https://x/y.git", sha: "0123abcd" },
  lastUpdated: 1,
};
const URL_PINNED: MarketplaceEntry = {
  name: "acme",
  source: { kind: "url", url: "https://x/y.git", ref: "main", sha: "0123abcd" },
  lastUpdated: 1,
};
// Stored values with surrounding whitespace. The form shows them verbatim, so
// an untouched draft carries the padding and must still read as unchanged.
const PADDED_REPO: MarketplaceEntry = {
  name: "acme",
  source: { kind: "github", repo: "acme/plugins " },
  lastUpdated: 1,
};
const PADDED_NAME: MarketplaceEntry = {
  name: " acme ",
  source: { kind: "github", repo: "acme/plugins" },
  lastUpdated: 1,
};

describe("marketplaceDraftFor", () => {
  test("seeds the kind and the matching field, blanking the others", () => {
    expect(marketplaceDraftFor(GITHUB)).toEqual({
      name: "acme",
      kind: "github",
      url: "",
      repo: "acme/plugins",
      path: "",
    });
    expect(marketplaceDraftFor(URL)).toEqual({ name: "acme", kind: "url", url: "https://x/y.git", repo: "", path: "" });
    expect(marketplaceDraftFor(DIR)).toEqual({ name: "acme", kind: "directory", url: "", repo: "", path: "/srv/mkt" });
  });

  test("a kind the picker does not offer shows its URL under Git URL", () => {
    expect(marketplaceDraftFor(SUBDIR)).toEqual({
      name: "acme",
      kind: "url",
      url: "https://x/mono.git",
      repo: "",
      path: "",
    });
    expect(marketplaceDraftFor(FUTURE)).toEqual({
      name: "acme",
      kind: "url",
      url: "https://gl/x.git",
      repo: "",
      path: "",
    });
  });
});

describe("marketplaceEditParams", () => {
  test("null when nothing changed, whitespace included", () => {
    expect(marketplaceEditParams(GITHUB, marketplaceDraftFor(GITHUB))).toBeNull();
    expect(
      marketplaceEditParams(GITHUB, { ...marketplaceDraftFor(GITHUB), name: " acme ", repo: "acme/plugins " }),
    ).toBeNull();
  });

  test("a changed name is a rename", () => {
    expect(marketplaceEditParams(GITHUB, { ...marketplaceDraftFor(GITHUB), name: "beta" })).toStrictEqual({
      name: "acme",
      newName: "beta",
    });
  });

  test("a changed field or kind sends the whole new source", () => {
    expect(marketplaceEditParams(GITHUB, { ...marketplaceDraftFor(GITHUB), repo: "acme/other" })).toStrictEqual({
      name: "acme",
      source: { kind: "github", repo: "acme/other" },
    });
    expect(
      marketplaceEditParams(GITHUB, { ...marketplaceDraftFor(GITHUB), kind: "directory", path: "/srv/x" }),
    ).toStrictEqual({
      name: "acme",
      source: { kind: "directory", path: "/srv/x" },
    });
  });

  test("switching the kind away and back is not a change", () => {
    const draft = marketplaceDraftFor(GITHUB);
    expect(marketplaceEditParams(GITHUB, { ...draft, kind: "url", url: "https://x/y.git" })).toStrictEqual({
      name: "acme",
      source: { kind: "url", url: "https://x/y.git" },
    });
    expect(marketplaceEditParams(GITHUB, { ...draft, kind: "github", url: "https://x/y.git" })).toBeNull();
  });

  test("a blanked name is not a rename, and a blanked source field is not a re-source", () => {
    expect(marketplaceEditParams(GITHUB, { ...marketplaceDraftFor(GITHUB), name: "   " })).toBeNull();
    expect(marketplaceEditParams(GITHUB, { ...marketplaceDraftFor(GITHUB), repo: "" })).toBeNull();
    expect(marketplaceEditParams(DIR, { ...marketplaceDraftFor(DIR), path: "  " })).toBeNull();
    expect(marketplaceEditParams(URL, { ...marketplaceDraftFor(URL), name: "", url: "" })).toBeNull();
    expect(marketplaceEditParams(GITHUB, { ...marketplaceDraftFor(GITHUB), name: "beta", repo: "" })).toStrictEqual({
      name: "acme",
      newName: "beta",
    });
  });

  test("an untouched source the picker does not offer is not re-sent", () => {
    expect(marketplaceEditParams(SUBDIR, marketplaceDraftFor(SUBDIR))).toBeNull();
    expect(marketplaceEditParams(FUTURE, marketplaceDraftFor(FUTURE))).toBeNull();
  });

  test("editing such a source's URL keeps its own kind and the path inside it, not its pins", () => {
    expect(marketplaceEditParams(SUBDIR, { ...marketplaceDraftFor(SUBDIR), url: "https://x/other.git" })).toStrictEqual(
      {
        name: "acme",
        source: { kind: "git-subdir", url: "https://x/other.git", path: "mkt" },
      },
    );
    expect(marketplaceEditParams(FUTURE, { ...marketplaceDraftFor(FUTURE), url: "https://gl/z.git" })).toStrictEqual({
      name: "acme",
      source: { kind: "gitlab", url: "https://gl/z.git" },
    });
  });

  test("picking a different kind for such a source is a wholesale replacement", () => {
    const draft = marketplaceDraftFor(SUBDIR);
    expect(marketplaceEditParams(SUBDIR, { ...draft, kind: "github", repo: "acme/plugins" })).toStrictEqual({
      name: "acme",
      source: { kind: "github", repo: "acme/plugins" },
    });
    expect(marketplaceEditParams(SUBDIR, { ...draft, kind: "url", repo: "acme/plugins" })).toBeNull();
  });

  test("a same-kind edit drops the pins the picker has no input for", () => {
    expect(marketplaceEditParams(GITHUB_REF, marketplaceDraftFor(GITHUB_REF))).toBeNull();
    expect(marketplaceEditParams(GITHUB_REF, { ...marketplaceDraftFor(GITHUB_REF), repo: "acme/other" })).toStrictEqual(
      {
        name: "acme",
        source: { kind: "github", repo: "acme/other" },
      },
    );
    // Picking a different kind replaces the source wholesale, so the ref goes
    // that way too.
    expect(
      marketplaceEditParams(GITHUB_REF, { ...marketplaceDraftFor(GITHUB_REF), kind: "url", url: "https://x/y.git" }),
    ).toStrictEqual({
      name: "acme",
      source: { kind: "url", url: "https://x/y.git" },
    });
  });

  test("a padded stored source value is not a change, and an edit still sends the trimmed one", () => {
    expect(marketplaceEditParams(PADDED_REPO, marketplaceDraftFor(PADDED_REPO))).toBeNull();
    expect(marketplaceSourceTouched(PADDED_REPO, marketplaceDraftFor(PADDED_REPO))).toBe(false);
    expect(
      marketplaceEditParams(PADDED_REPO, { ...marketplaceDraftFor(PADDED_REPO), repo: "acme/other" }),
    ).toStrictEqual({
      name: "acme",
      source: { kind: "github", repo: "acme/other" },
    });
  });

  test("a padded stored name is not a rename, and a rename still sends the trimmed name", () => {
    expect(marketplaceEditParams(PADDED_NAME, marketplaceDraftFor(PADDED_NAME))).toBeNull();
    // The request keys by the name the server stores, padding and all.
    expect(marketplaceEditParams(PADDED_NAME, { ...marketplaceDraftFor(PADDED_NAME), name: " beta " })).toStrictEqual({
      name: " acme ",
      newName: "beta",
    });
  });

  test("a re-source drops a ref or sha pin, and a rename alone sends no source at all", () => {
    expect(marketplaceEditParams(URL_SHA, { ...marketplaceDraftFor(URL_SHA), url: "https://x/z.git" })).toStrictEqual({
      name: "acme",
      source: { kind: "url", url: "https://x/z.git" },
    });
    expect(
      marketplaceEditParams(URL_PINNED, { ...marketplaceDraftFor(URL_PINNED), url: "https://x/z.git" }),
    ).toStrictEqual({
      name: "acme",
      source: { kind: "url", url: "https://x/z.git" },
    });
    // No source on the wire, so the stored pins are the server's to keep.
    expect(marketplaceEditParams(URL_PINNED, { ...marketplaceDraftFor(URL_PINNED), name: "beta" })).toStrictEqual({
      name: "acme",
      newName: "beta",
    });
  });

  test("rename and re-source ride one request", () => {
    expect(
      marketplaceEditParams(URL, { ...marketplaceDraftFor(URL), name: "beta", url: "https://x/z.git" }),
    ).toStrictEqual({
      name: "acme",
      newName: "beta",
      source: { kind: "url", url: "https://x/z.git" },
    });
  });
});

describe("marketplaceDraftIncomplete", () => {
  test("true whenever the kind's own field has no value yet", () => {
    expect(marketplaceDraftIncomplete(marketplaceDraftFor(GITHUB))).toBe(false);
    expect(marketplaceDraftIncomplete({ ...marketplaceDraftFor(GITHUB), repo: "  " })).toBe(true);
    // The case marketplaceEditParams cannot report: a kind picked but not
    // filled in is no source change, so an edited name would otherwise let a
    // rename-only save through while the picker says the source moved too.
    expect(marketplaceDraftIncomplete({ ...marketplaceDraftFor(GITHUB), kind: "directory" })).toBe(true);
    expect(marketplaceDraftIncomplete({ ...marketplaceDraftFor(GITHUB), kind: "directory", path: "/srv/x" })).toBe(
      false,
    );
    // Only the kind's own field counts - another kind's leftovers don't.
    expect(marketplaceDraftIncomplete({ ...marketplaceDraftFor(URL), url: "", repo: "acme/plugins" })).toBe(true);
  });
});

describe("marketplaceSourceTouched", () => {
  test("false for the draft the entry itself seeds", () => {
    expect(marketplaceSourceTouched(GITHUB, marketplaceDraftFor(GITHUB))).toBe(false);
    expect(marketplaceSourceTouched(DIR, marketplaceDraftFor(DIR))).toBe(false);
    expect(marketplaceSourceTouched(GITHUB, { ...marketplaceDraftFor(GITHUB), repo: " acme/plugins " })).toBe(false);
    // A rename is not a source edit, and another kind's leftovers are not the
    // source either.
    expect(marketplaceSourceTouched(GITHUB, { ...marketplaceDraftFor(GITHUB), name: "beta" })).toBe(false);
    expect(marketplaceSourceTouched(GITHUB, { ...marketplaceDraftFor(GITHUB), url: "https://x/y.git" })).toBe(false);
  });

  test("true for a different kind, before that kind's field has anything in it", () => {
    expect(marketplaceSourceTouched(GITHUB, { ...marketplaceDraftFor(GITHUB), kind: "directory" })).toBe(true);
    expect(
      marketplaceSourceTouched(GITHUB, { ...marketplaceDraftFor(GITHUB), kind: "url", url: "https://x/y.git" }),
    ).toBe(true);
  });

  test("true for a changed or emptied field of the kind the draft is on", () => {
    expect(marketplaceSourceTouched(GITHUB, { ...marketplaceDraftFor(GITHUB), repo: "acme/other" })).toBe(true);
    expect(marketplaceSourceTouched(GITHUB, { ...marketplaceDraftFor(GITHUB), repo: "  " })).toBe(true);
    expect(marketplaceSourceTouched(DIR, { ...marketplaceDraftFor(DIR), path: "" })).toBe(true);
  });

  test("a source kind the picker cannot offer is untouched until its URL is edited", () => {
    expect(marketplaceSourceTouched(SUBDIR, marketplaceDraftFor(SUBDIR))).toBe(false);
    expect(marketplaceSourceTouched(FUTURE, marketplaceDraftFor(FUTURE))).toBe(false);
    // Seeded empty, so nothing has been typed and nothing is touched - which
    // is what leaves such an entry renameable.
    expect(marketplaceSourceTouched(FUTURE_NO_URL, marketplaceDraftFor(FUTURE_NO_URL))).toBe(false);
    expect(
      marketplaceSourceTouched(FUTURE_NO_URL, { ...marketplaceDraftFor(FUTURE_NO_URL), url: "https://gl/z.git" }),
    ).toBe(true);
  });
});

test("the source options are the add form's three kinds", () => {
  expect(MARKETPLACE_SOURCE_OPTIONS.map((o) => o.value)).toEqual(["url", "github", "directory"]);
});
