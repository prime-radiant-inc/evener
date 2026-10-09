#!/usr/bin/env python3
"""Generate the index-overflow scenario: a seeded project memory of about 120
tagged pages whose generated index exceeds the 8 KiB projection, with the one
fact the task needs on the oldest page, which the projection leaves out.

Why a generator: 120 pages by hand drift; this keeps the seed, its hand-written
MEMORY.md (so a build from before the generated index also gets an index) and
the scenario's tag-reuse check in step. The checks themselves live in memcheck.py. Run it from anywhere after editing it:

    python3 scenarios/index-overflow/make_seed.py

It rewrites seed/ and scenario.json next to itself, then run ./memory-lab check.
"""
import datetime, itertools, json, os, shutil

HERE = os.path.dirname(os.path.abspath(__file__))
SEED = os.path.join(HERE, "seed")
FILLER = 119  # plus the target page: 120 pages
NEWEST = datetime.date(2026, 9, 30)

TOPICS = {
    "vitest": "Vitest run {n} needs --pool=forks when worker {n} crashes on teardown",
    "indexeddb": "IndexedDB store {n} keeps records as plain JSON, never class instances",
    "release": "Release step {n} tags only after main CI is green on the merge commit",
    "ci": "CI job {n} caches modules by go.sum hash, never by branch name",
    "logging": "Log field {n} uses snake_case keys through log/slog",
    "catalog": "Catalog rule {n}: SKUs are lowercase with no spaces",
    "cart": "Cart rule {n}: line quantities are whole numbers",
    "tests": "Test helper {n} builds catalogs with prices in cents",
    "docs": "Doc comment rule {n}: start with the identifier's name",
    "api": "Exported API rule {n}: no new exported names without a doc comment",
    "pricing": "Pricing rule {n}: totals are computed in integer cents",
}
TARGET = {
    "path": "coupon-stacking.md",
    "title": "Coupons never stack",
    "description": "A cart's coupons never stack: only the largest percent applies",
    "tags": ["coupons", "pricing"],
    "updated": "2026-03-02",
    "body": "Only the largest coupon percent applies to a cart; coupons never stack or compound.\n\n"
            "**Why:** finance ruled on 2026-03-02 that stacked coupons let carts reach zero.\n\n"
            "**How to apply:** a function applying several coupons takes the largest percent and calls ApplyCoupon once.\n",
}


def page(title, description, tags, updated, body):
    # Quoted: a bare description with ": " in it ("Cart rule 7: ...") is invalid YAML, and the
    # generated index shows such a page as "(frontmatter unreadable)" with no date, which sorts
    # it first and puts the target in plain view.
    return (f"---\ndescription: {json.dumps(description)}\ntags: [{', '.join(tags)}]\n"
            f"updated: {updated}\nby: seed-fixture\n---\n# {title}\n\n{body}")


def main():
    shutil.rmtree(SEED, ignore_errors=True)
    os.makedirs(SEED)
    pages = []
    for n, tag in enumerate(itertools.islice(itertools.cycle(TOPICS), FILLER), 1):
        description = TOPICS[tag].format(n=n)
        pages.append({"path": f"{tag}/{tag}-{n:03d}.md", "title": f"{tag.capitalize()} note {n}",
                      "description": description, "tags": [tag] if n % 4 else [tag, "tests"],
                      "updated": (NEWEST - datetime.timedelta(days=n - 1)).isoformat(),
                      "body": description + ".\n"})
    pages.append(TARGET)
    for p in pages:
        dest = os.path.join(SEED, p["path"])
        os.makedirs(os.path.dirname(dest), exist_ok=True)
        with open(dest, "w") as f:
            f.write(page(p["title"], p["description"], p["tags"], p["updated"], p["body"]))
    # Newest first, as the generated index orders them; the target is last, past 8 KiB.
    with open(os.path.join(SEED, "MEMORY.md"), "w") as f:
        f.write("# Project memory\n\n")
        for p in pages:
            f.write(f"- [{p['title']}]({p['path']}) — {p['description']}\n")
    seed_tags = sorted({t for p in pages for t in p["tags"]})
    memcheck = 'python3 "$LAB_DIR/memcheck.py" new-pages'
    heldout = (
        "cat > zz_heldout_test.go <<'EOF'\npackage shop\n\nimport \"testing\"\n\n"
        "func TestHeldoutApplyCouponsLargestOnly(t *testing.T) {\n"
        "\tfor _, c := range []struct {\n\t\ttotal    int\n\t\tpercents []int\n\t\twant     int\n\t}{\n"
        "\t\t{1000, []int{10, 20}, 800},\n\t\t{1000, []int{20, 10}, 800},\n\t\t{1000, nil, 1000},\n\t\t{1000, []int{50, 50}, 500},\n"
        "\t} {\n\t\tif got := ApplyCoupons(c.total, c.percents); got != c.want {\n"
        "\t\t\tt.Errorf(\"ApplyCoupons(%d, %v) = %d, want %d\", c.total, c.percents, got, c.want)\n\t\t}\n\t}\n}\nEOF\n"
        "go test -count=1 -run Heldout ./...; rc=$?; rm -f zz_heldout_test.go; exit $rc"
    )
    scenario = {
        "arms": ["on"],
        "fixture_from": "migration",
        "stages": [{
            "name": "B",
            "seed_project_memory": "seed",
            "prompt": "Add ApplyCoupons(total int, percents []int) int to the shop package: it applies a cart's coupons to its total, in cents. Add tests.",
            "checks": [
                {"name": "tests pass", "run": "go test ./..."},
                {"name": "held-out largest coupon only", "run": heldout},
                {"name": "new pages reuse seeded tags", "run": f"{memcheck} --tags-subset-of {','.join(seed_tags)}"},
                {"name": "new pages have a description", "run": f"{memcheck} --require-description"},
            ],
            "trace": [
                {"name": "read the coupon page", "tool": "memory_read", "regex": "coupon"},
                {"name": "searched memory for coupons", "tool": "memory_search", "regex": "(?i)coupon"},
            ],
        }],
    }
    with open(os.path.join(HERE, "scenario.json"), "w") as f:
        json.dump(scenario, f, indent=2)
        f.write("\n")


if __name__ == "__main__":
    main()
