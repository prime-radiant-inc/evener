#!/usr/bin/env python3
"""Shell checks over a trial's memory files, for scenario.json "checks" entries.

memory-lab passes the lab directory to every check as $LAB_DIR and the trial's
memory root as $XDG_STATE_HOME, so a scenario runs:

    python3 "$LAB_DIR/memcheck.py" index-lines
    python3 "$LAB_DIR/memcheck.py" new-pages --require-description --tags-subset-of a,b

index-lines   Exit 1 when an index line is over 200 characters. It measures the
              part of the line the model writes: a frontmatter page's description,
              or each line of a scope's root MEMORY.md (a build without the
              generated index). The title, path, tags and date the product adds are
              not counted. Characters are runes, not bytes.
new-pages     Looks only at project pages not marked `by: seed-fixture` (nor the
              root MEMORY.md): in a one-stage scenario, the pages the stage wrote;
              a later stage also sees an earlier one's. Exit 1 when there are none, or when
              --require-description is set and one has no readable description, or
              when --tags-subset-of is set and one has no tags or a tag outside the list.

memory-lab check also imports seed_frontmatter_problem to vet scenario seeds.

Standard library only; run `python3 -B test_memcheck.py` for its tests.
"""
import argparse, glob, os, re, stat, sys

MAX_INDEX_LINE = 200


def split_frontmatter(text):
    """The lines of the leading `---` block, or None when the text has none.
    As in the product (frontmatter.Split), each delimiter is a line of exactly
    "---" ending in "\n", so a CRLF or space-padded one is none."""
    lines = text.split("\n")
    if lines[0] != "---":
        return None
    for i in range(1, len(lines) - 1):
        if lines[i] == "---":
            return lines[1:i]
    return None


def unquote(value):
    value = value.strip()
    if len(value) >= 2 and value[0] == value[-1] and value[0] in "\"'":
        return value[1:-1]
    return value


class Unreadable(ValueError):
    """Frontmatter that is not valid YAML; the product lists its page as "(frontmatter unreadable)"."""


# Plain scalars yaml.v3 reads as null, bool, int, float or timestamp, not a string:
# a page whose description is one has none, for the product as here.
YAML_NON_STRING = re.compile(
    r"~|null|Null|NULL|true|True|TRUE|false|False|FALSE"
    r"|[-+]?(0b[01_]+|0o[0-7_]+|0x[0-9a-fA-F_]+|[0-9][0-9_]*)"
    r"|[-+]?(\.[0-9]+|[0-9][0-9_]*\.[0-9_]*|[0-9][0-9_]*)([eE][-+]?[0-9]+)?"
    r"|[-+]?\.(inf|Inf|INF)|\.(nan|NaN|NAN)"
    r"|[0-9]{4}-[0-9]{1,2}-[0-9]{1,2}([Tt ].*)?")


def closing_quote(value):
    """The index of the quote that closes value's opening one, or -1. A double-quoted
    scalar escapes with a backslash; a single-quoted one doubles its quote."""
    q, i = value[0], 1
    while i < len(value):
        if q == '"' and value[i] == "\\":
            i += 2
        elif value[i] == q and q == "'" and value[i + 1:i + 2] == "'":
            i += 2
        elif value[i] == q:
            return i
        else:
            i += 1
    return -1


def scalar(value):
    """The string a frontmatter value holds, None when YAML types it as something
    else (YAML_NON_STRING, a [sequence] or {mapping}, or only a comment), or
    Unreadable on the invalid YAML a model most often writes: a quote left open
    or followed by more than a comment, or a plain value holding ": " or ending
    in ":". A " #" in a plain value starts a comment."""
    if value[:1] in ("'", '"'):
        end = closing_quote(value)
        if end < 0:
            raise Unreadable(f"value {value!r} never closes its quote")
        after = value[end + 1:].strip()
        if after and not after.startswith("#"):
            raise Unreadable(f"value {value!r} goes on after its closing quote")
        return value[1:end]
    value = re.split(r"(?:^|[ \t])#", value, maxsplit=1)[0].strip()
    if not value or YAML_NON_STRING.fullmatch(value) or value[:1] in ("[", "{"):
        return None
    if ": " in value or value.endswith(":"):
        raise Unreadable(f"unquoted value {value!r} holds a ':' that YAML reads as a mapping")
    return value


YAML_NULL = {"~", "null", "Null", "NULL"}


def normalize_tags(items):
    """The product's tag rules (normalizeMemoryTags): trimmed, lowercased, runs
    of whitespace as "-", empty tags, unquoted nulls and duplicates dropped."""
    tags = []
    for item in items:
        if item.strip() in YAML_NULL:
            continue
        tag = "-".join(unquote(item).lower().split())
        if tag and tag not in tags:
            tags.append(tag)
    return tags


def parse_frontmatter(text):
    """The page's top-level frontmatter keys as {key: value} and its tags list.

    Returns None when there is no frontmatter block, and raises Unreadable on
    the invalid YAML a model most often writes (scalar). A value may be
    plain, quoted, or a folded/literal block scalar (> or |) with indented
    continuation lines, which join with spaces; a value YAML types as other
    than a string is None (scalar). tags is a flow list `[a, b]`,
    a block list of `- a` lines, or one scalar tag (normalize_tags)."""
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
                tags = normalize_tags(value.strip("[]").split(","))
            elif value and not value.startswith("#"):
                tags = normalize_tags([t for t in [scalar(value)] if t is not None])
            else:
                tags = normalize_tags(r[2:] for r in rest if r.startswith("- "))
        elif re.match(r"^[>|][+-]?$", value):
            fields[key] = " ".join(rest).strip()
        else:
            fields[key] = scalar(" ".join([value] + rest))
    return fields, tags


def seed_frontmatter_problem(text):
    """Why a seeded page's frontmatter would not parse, or None when it will.
    memory-lab check runs this over every seed page."""
    first = text.split("\n", 1)[0]
    if first != "---":
        return None if first.rstrip() != "---" else f"opening delimiter is {first!r}; the product reads only '---' then a newline"
    try:
        if parse_frontmatter(text) is None:
            return "frontmatter has no closing ---"
    except Unreadable as e:
        return str(e)
    return None


def read(path):
    with open(path, encoding="utf-8", errors="replace", newline="") as f:  # raw line ends, as the product reads them
        return f.read()


def memory_files(root, scopes):
    """Every file the product lists as a page or index: regular files only, with
    no dot path and no symlink on the way, as the product's listing skips them."""
    base = glob.escape(os.path.join(root, "evener", "memory"))
    for scope in scopes:
        for top in sorted(glob.glob(os.path.join(base, scope))):
            if os.path.islink(top) or not os.path.isdir(top):
                continue
            found = []
            for dirpath, dirnames, filenames in os.walk(top):  # os.walk never enters a symlinked dir
                dirnames[:] = [d for d in dirnames if not d.startswith(".")]
                found += [os.path.join(dirpath, f) for f in filenames
                          if not f.startswith(".") and stat.S_ISREG(os.lstat(os.path.join(dirpath, f)).st_mode)]
            yield from sorted(found)


def is_root_index(root, path):
    """Whether path is a scope's root MEMORY.md (the product matches its name
    without regard to case); a MEMORY.md further down is an ordinary page."""
    rel = os.path.relpath(path, os.path.join(root, "evener", "memory")).split(os.sep)
    return rel[-1].lower() == "memory.md" and len(rel) == (2 if rel[0] == "personal" else 3)


def readable_frontmatter(path):
    """The page's (fields, tags), with ({}, []) for a non-Markdown page or one
    without frontmatter or with Unreadable frontmatter, as the product reads
    none of their descriptions."""
    if not path.endswith(".md"):
        return {}, []
    try:
        return parse_frontmatter(read(path)) or ({}, [])
    except Unreadable as e:
        print(f"{path}: frontmatter unreadable: {e}", file=sys.stderr)
        return {}, []


def index_lines_ok(root):
    for path in memory_files(root, ["personal", "projects/*"]):
        if is_root_index(root, path):
            lines = read(path).split("\n")
        else:
            lines = [readable_frontmatter(path)[0].get("description") or ""]
        for line in lines:
            if len(line) > MAX_INDEX_LINE:
                print(f"{path}: index line is {len(line)} characters", file=sys.stderr)
                return False
    return True


def new_pages_ok(root, require_description, tags_subset):
    new = 0
    for path in memory_files(root, ["projects/*"]):
        if is_root_index(root, path):
            continue
        fields, tags = readable_frontmatter(path)
        if fields.get("by") == "seed-fixture":
            continue
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
