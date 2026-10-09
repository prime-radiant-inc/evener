#!/usr/bin/env python3
"""Shell checks over a trial's memory files, for scenario.json "checks" entries.

memory-lab passes the lab directory to every check as $LAB_DIR and the trial's
memory root as $XDG_STATE_HOME, so a scenario runs:

    python3 "$LAB_DIR/memcheck.py" index-lines
    python3 "$LAB_DIR/memcheck.py" new-pages --require-description --tags-subset-of a,b

index-lines   Exit 1 when an index line is over 200 characters. A frontmatter
              page's index line is its description; a MEMORY.md (a build without
              the generated index) contributes each of its lines. Characters are
              runes, not bytes.
new-pages     Looks only at project pages the stage wrote (not MEMORY.md, not
              marked `by: seed-fixture`). Exit 1 when it wrote none, or when
              --require-description is set and one has no description, or when
              --tags-subset-of is set and one has no tags or a tag outside the list.

memory-lab check also imports seed_frontmatter_problem to vet scenario seeds.

Standard library only; run `python3 -B test_memcheck.py` for its tests.
"""
import argparse, glob, json, os, re, sys

MAX_INDEX_LINE = 200


def split_frontmatter(text):
    """The lines of the leading `---` block, or None when the text has none."""
    lines = text.split("\n")
    if not lines or lines[0].rstrip() != "---":
        return None
    for i in range(1, len(lines)):
        if lines[i].rstrip() == "---":
            return lines[1:i]
    return None


def unquote(value):
    value = value.strip()
    if len(value) >= 2 and value[0] == value[-1] and value[0] in "\"'":
        return value[1:-1]
    return value


def parse_frontmatter(text):
    """The page's top-level frontmatter keys as {key: value} and its tags list.

    Returns None when there is no frontmatter block. A value may be plain,
    quoted, or a folded/literal block scalar (> or |) with indented
    continuation lines, which join with spaces. tags is a flow list
    `[a, b]` or a block list of `- a` lines, lowercased."""
    block = split_frontmatter(text)
    if block is None:
        return None
    fields, tags = {}, []
    i = 0
    while i < len(block):
        m = re.match(r"^([A-Za-z_][\w-]*):[ \t]*(.*)$", block[i])
        i += 1
        if not m:
            continue
        key, value = m.group(1), m.group(2).strip()
        rest = []
        while i < len(block) and (block[i].startswith((" ", "\t")) or (key == "tags" and block[i].startswith("- "))):
            rest.append(block[i].strip())
            i += 1
        if key == "tags":
            if value.startswith("["):
                tags = [unquote(t) for t in value.strip("[]").split(",")]
            else:
                tags = [unquote(r[2:]) for r in rest if r.startswith("- ")]
            tags = [t.lower() for t in tags if t]
        elif re.match(r"^[>|][+-]?$", value):
            fields[key] = " ".join(rest).strip()
        else:
            fields[key] = unquote(" ".join([value] + rest))
    return fields, tags


# The frontmatter values a seed page may use: a JSON string, which is also a valid
# YAML double-quoted scalar, a flow list of plain words, or one plain word or date.
SEED_VALUE = re.compile(r'^(".*"|\[[\w-]+(, [\w-]+)*\]|[\w][\w.-]*)$')


def seed_frontmatter_problem(text):
    """Why a seeded page's frontmatter might not parse as YAML, or None when it will.

    memory-lab check runs this over every seed page. Rather than parse YAML, it
    accepts only `key: value` lines whose value is in SEED_VALUE: a bare value
    holding ": " is invalid YAML, and the product lists a page whose
    frontmatter fails to parse as "(frontmatter unreadable)" with no date."""
    if text.split("\n", 1)[0].rstrip() != "---":
        return None
    block = split_frontmatter(text)
    if block is None:
        return "frontmatter has no closing ---"
    for line in block:
        m = re.match(r"^[A-Za-z_][\w-]*: (.*)$", line)
        if not (m and SEED_VALUE.match(m.group(1))):
            return f"frontmatter line {line!r} is not key: followed by a JSON string, a [word, ...] list or one word"
        if m.group(1).startswith('"'):
            try:
                json.loads(m.group(1))
            except ValueError as e:
                return f"frontmatter line {line!r}: {e}"
    return None


def read(path):
    with open(path, encoding="utf-8", errors="replace") as f:
        return f.read()


def memory_files(root, scopes):
    for scope in scopes:
        yield from sorted(glob.glob(os.path.join(root, "evener", "memory", scope, "**", "*.md"), recursive=True))


def index_lines_ok(root):
    for path in memory_files(root, ["personal", "projects/*"]):
        text = read(path)
        if os.path.basename(path) == "MEMORY.md":
            lines = text.split("\n")
        else:
            parsed = parse_frontmatter(text)
            lines = [parsed[0].get("description", "")] if parsed else []
        for line in lines:
            if len(line) > MAX_INDEX_LINE:
                print(f"{path}: index line is {len(line)} characters", file=sys.stderr)
                return False
    return True


def new_pages_ok(root, require_description, tags_subset):
    new = 0
    for path in memory_files(root, ["projects/*"]):
        parsed = parse_frontmatter(read(path))
        if os.path.basename(path) == "MEMORY.md" or (parsed and parsed[0].get("by") == "seed-fixture"):
            continue
        fields, tags = parsed if parsed else ({}, [])
        new += 1
        if require_description and not fields.get("description"):
            print(f"{path}: no description", file=sys.stderr)
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
    if args.cmd == "index-lines":
        ok = index_lines_ok(root)
    else:
        subset = None if args.tags_subset_of is None else {t.lower() for t in args.tags_subset_of.split(",") if t}
        ok = new_pages_ok(root, args.require_description, subset)
    return 0 if ok else 1


if __name__ == "__main__":
    sys.exit(main())
