#!/usr/bin/env python3
"""Shell checks over a trial's memory files, for scenario.json "checks" entries.

memory-lab passes the lab directory to every check as $LAB_DIR and the trial's
memory root as $XDG_STATE_HOME, so a scenario runs:

    python3 "$LAB_DIR/memcheck.py" index-lines
    python3 "$LAB_DIR/memcheck.py" new-pages --require-description --tags-subset-of a,b

index-lines   Exit 1 when an index line is over 200 characters. It measures the
              part of the line the model writes: a page's description, or each
              line of a scope's root MEMORY.md (a build without the generated
              index). The title, path, tags and date the product adds are not
              counted. Characters are runes, not bytes.
new-pages     Looks only at project pages not marked `by: seed-fixture`: in a
              one-stage scenario, the pages the stage wrote; a later stage also
              sees an earlier one's. Exit 1 when there are none, or when
              --require-description is set and one has no description, or when
              --tags-subset-of is set and one has no tags or a tag outside the list.

Pages are read by bin/memscope, which lists and parses them with evener's own
code (agent.ListMemoryScopePages), so a check sees a page exactly as the
generated index does. Build it from the lab directory first:

    go build -o bin/memscope ./memscope

memory-lab check also imports seed_problems to vet scenario seeds.

Run `python3 -B test_memcheck.py` for its tests.
"""
import argparse, glob, json, os, subprocess, sys

MAX_INDEX_LINE = 200
MEMSCOPE = os.path.join(os.path.dirname(os.path.abspath(__file__)), "bin", "memscope")
MEMSCOPE_TIMEOUT = 60  # seconds; it only reads a few directories
MEMSCOPE_BUILD = "build it in the lab directory: go build -o bin/memscope ./memscope"
# The agent.MemoryScopePage fields the checks read; a memscope built before one
# existed leaves it out.
PAGE_FIELDS = {"path", "description", "has_description", "frontmatter", "unreadable", "tags", "by"}


class MemscopeError(RuntimeError):
    """bin/memscope is missing or failed."""


def scope_pages(dirs):
    """{dir: [page, ...]} for each memory scope directory, from bin/memscope."""
    if not dirs:
        return {}
    try:
        out = subprocess.run([MEMSCOPE, *dirs], capture_output=True, text=True, timeout=MEMSCOPE_TIMEOUT)
    except FileNotFoundError:
        raise MemscopeError(f"{MEMSCOPE} is missing; {MEMSCOPE_BUILD}")
    except subprocess.TimeoutExpired:
        raise MemscopeError(f"memscope timed out after {MEMSCOPE_TIMEOUT}s")
    if out.returncode != 0:
        raise MemscopeError(f"memscope failed: {out.stderr.strip()}")
    scopes = json.loads(out.stdout)
    for pages in scopes.values():
        for p in pages:
            if missing := PAGE_FIELDS - p.keys():
                raise MemscopeError(f"{MEMSCOPE} is out of date (its pages lack {', '.join(sorted(missing))}); re{MEMSCOPE_BUILD}")
    return scopes


def scope_dirs(root, scopes):
    """The memory scope directories under the trial's state root, e.g. scopes ["personal", "projects/*"].
    A symlink is skipped, as evener's confined listing refuses one."""
    base = glob.escape(os.path.join(root, "evener", "memory"))
    return sorted(d for scope in scopes for d in glob.glob(os.path.join(base, scope))
                  if os.path.isdir(d) and not os.path.islink(d))


def read(path):
    with open(path, encoding="utf-8", errors="replace", newline="") as f:  # raw line ends, as the product reads them
        return f.read()


def root_index(scope_dir):
    """The scope's root MEMORY.md, its name matched without regard to case as the product does, or None.
    Like the product, it counts only a regular file, never a symlink."""
    for name in sorted(os.listdir(scope_dir)):
        path = os.path.join(scope_dir, name)
        if name.lower() == "memory.md" and os.path.isfile(path) and not os.path.islink(path):
            return path
    return None


def seed_problems(seed_dirs):
    """{seed dir: [problem, ...]} for the seed pages that won't read as written:
    frontmatter evener can't parse, or a Markdown page that opens with --- in
    which evener finds no frontmatter block (a CRLF or padded delimiter, or no
    closing ---). memory-lab check runs this; seeds with no problems are left out."""
    problems = {}
    for seed, pages in scope_pages(seed_dirs).items():
        for p in pages:
            if p["unreadable"]:
                problem = "frontmatter unreadable"
            elif not p["frontmatter"] and p["path"].endswith(".md") and read(os.path.join(seed, p["path"])).startswith("---"):
                problem = "starts with --- but evener finds no frontmatter block"
            else:
                continue
            problems.setdefault(seed, []).append(f"{p['path']}: {problem}")
    return problems


def index_lines_ok(root):
    for scope, pages in scope_pages(scope_dirs(root, ["personal", "projects/*"])).items():
        index = root_index(scope)
        lines = [(index, line) for line in read(index).split("\n")] if index else []
        lines += [(os.path.join(scope, p["path"]), p["description"]) for p in pages if p["has_description"]]
        for path, line in lines:
            if len(line) > MAX_INDEX_LINE:
                print(f"{path}: index line is {len(line)} characters", file=sys.stderr)
                return False
    return True


def new_pages_ok(root, require_description, tags_subset):
    new = 0
    for scope, pages in scope_pages(scope_dirs(root, ["projects/*"])).items():
        for p in pages:
            if p["by"] == "seed-fixture":
                continue
            new += 1
            path, tags = os.path.join(scope, p["path"]), p["tags"] or []
            if require_description and not p["has_description"]:
                print(f"{path}: no description{' (frontmatter unreadable)' if p['unreadable'] else ''}", file=sys.stderr)
                return False
            if tags_subset is not None and (not tags or not set(tags) <= tags_subset):
                print(f"{path}: tags {tags} are not all seeded tags", file=sys.stderr)
                return False
    if not new:
        print("the stage wrote no project page", file=sys.stderr)
    return new > 0


def main(argv=None):
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    sub = ap.add_subparsers(dest="cmd", required=True)
    sub.add_parser("index-lines")
    np = sub.add_parser("new-pages")
    np.add_argument("--require-description", action="store_true")
    np.add_argument("--tags-subset-of", help="comma-separated tags new pages may use")
    args = ap.parse_args(argv)
    root = os.environ.get("XDG_STATE_HOME")
    if not root:
        sys.exit("memcheck: XDG_STATE_HOME is not set")
    try:
        if args.cmd == "index-lines":
            ok = index_lines_ok(root)
        else:
            # Normalized as the product normalizes page tags: lowercased, whitespace runs as "-".
            subset = None if args.tags_subset_of is None else {
                "-".join(t.lower().split()) for t in args.tags_subset_of.split(",") if t.strip()}
            ok = new_pages_ok(root, args.require_description, subset)
    except MemscopeError as e:
        sys.exit(f"memcheck: {e}")
    return 0 if ok else 1


if __name__ == "__main__":
    sys.exit(main())
