#!/usr/bin/env python3
"""Inventory tests that use human-readable copy as their oracle.

docs/developing-evener/testing.md ("Copy Is Not a Test Oracle") forbids tests
that assert rendered or returned text equals, contains, or matches a phrase, or
equals a constant imported from the code under test. This script finds the
candidates so they can be converted area by area, and re-running it shows what
is left.

    python3 tools/copy-oracle/inventory.py                 # per-package summary
    python3 tools/copy-oracle/inventory.py --hits hits.tsv # also every hit
    python3 tools/copy-oracle/inventory.py --path cmd/evener-hub/frontend/src/settings

A literal counts as copy only when it also appears in production source (or a
production text asset such as an embedded prompt). A literal that appears only
in tests is fixture data the test fed in, and seeing it come out is a real
behavioral check, so it is counted as "fixture" and not reported. Identifier-
shaped literals (`running`, `session_id`, `--json`) and code-, shell- or
markup-shaped ones (`cat >`, `export type X`, `<tag attr="`) are machine
contract values and are skipped.

Categories:
  ts-assert     frontend/native text assertion against app copy
                (*ByText/*ByTitle outside a click, toContain/toBe/toMatch/
                toHaveTextContent)
  ts-locator    ByText used only to find a control to click; cheap to move to
                getByRole, not itself an assertion
  ts-constant   ByText/toBe/toContain against an identifier named like copy
                (*_MESSAGE, *Label, *Error, ...) imported from a non-test module
  go-errstring  strings.Contains/HasPrefix/==/regexp on err.Error() or an
                error-ish variable, against a phrase from production source
  go-copy       the same checks on other output (CLI output, logs, rendered text)
  go-asset      Go checks against a phrase that lives in a non-Go text asset
                (embedded prompts, templates); review against the golden rule

The scan is line-based and heuristic: multi-line expectations and computed
strings are missed, and a match is a candidate to read, not a verdict. It does
not count accessible names used as a state oracle (`getByRole("button", { name:
"Agents, 3 of 4 active" })`); those are found while converting each area.
"""
import argparse, collections, functools, os, re, subprocess, sys

ROOT = subprocess.run(["git", "rev-parse", "--show-toplevel"], capture_output=True,
                      text=True, check=True).stdout.strip()

TS_TREES = ["cmd/evener-hub/frontend/src", "mobile-native", "appwire-client/typescript"]
TS_TEST = re.compile(r"(\.test\.|\.spec\.|/__tests__/|TestUtils\.|/testing/|/src/dev/)")
ASSET_EXT = (".md", ".tmpl", ".txt", ".gotmpl", ".prompt")

STR = r"""(?:"((?:[^"\\]|\\.)*)"|'((?:[^'\\]|\\.)*)'|`((?:[^`\\$]|\\.)*)`)"""
REGEX = r"/((?:[^/\\\n]|\\.)+)/[a-z]*"
TS_BYTEXT = re.compile(r"\b(?:get|find|query)(?:All)?By(?:Text|Title)\(\s*(?:" + STR + "|" + REGEX + r"|([A-Za-z_$][\w$.]*)\s*[,)])")
TS_MATCHER = re.compile(r"\.(?:toContain|toBe|toEqual|toMatch|toHaveTextContent|toStrictEqual)\(\s*(?:" + STR + "|" + REGEX + r"|([A-Za-z_$][\w$.]*)\s*\))")
TS_CLICK = re.compile(r"\b(?:fireEvent\.\w+|user(?:Event)?\.\w+|press|click|act)\s*\(")
TS_IMPORT = re.compile(r"import\s*(?:type\s*)?\{([^}]*)\}\s*from\s*['\"]([^'\"]+)['\"]")

GO_STR = r"""(?:"((?:[^"\\]|\\.)*)"|`([^`]*)`)"""
GO_CHECKS = [
    re.compile(r"strings\.(?:Contains|HasPrefix|HasSuffix|EqualFold|Index)\(\s*([^,]+?),\s*" + GO_STR),
    re.compile(r"([\w.\[\]()]+)\s*(?:==|!=)\s*" + GO_STR),
    re.compile(r"regexp\.MustCompile\(\s*" + GO_STR + r"\)\.MatchString\(\s*([^)]+)\)"),
]
GO_ERRISH = re.compile(r"(\.Error\(\)|\berr\w*\b|\bErr\w*\b|stderr|errOut|errMsg|message|msg)", re.I)
TOKEN = re.compile(r"^[-\w.:/@#=%+]*$")
# A capitalized word ("Welcome", "Retry") is a label, not an identifier.
LABEL_WORD = re.compile(r"^[A-Z][a-z]+[.!?…]?$")
# Literals shaped like code, shell commands, markup, or wire syntax are format
# contracts even when they contain spaces.
CODE_LIKE = re.compile(r"""[{};=<>"`$\\]|^(?:cat|wc|uname|id|mkdir|rm|ls|sh|bash|git|go|npm|npx|test|printf|echo|chmod|mv|cp|tar|export|func|type|package|import|const|var|set|exec|cd|env|sudo|kill|pgrep|ps|tmux|ssh|curl|make)\b""")
COPY_IDENT = re.compile(r"(MESSAGE|LABEL|COPY|TEXT|TITLE|HINT|ERROR|PROMPT|USAGE|PLACEHOLDER|Message|Label|Copy|Text|Title|Hint|Error|Prompt|Usage|Placeholder)S?$")


def git_files(*patterns):
    out = subprocess.run(["git", "ls-files", "-z", "--", *patterns], cwd=ROOT,
                         capture_output=True, text=True, check=True).stdout
    return [p for p in out.split("\0") if p and "node_modules/" not in p]


def read(path):
    try:
        with open(os.path.join(ROOT, path), encoding="utf-8", errors="replace") as f:
            return f.read()
    except OSError:
        return ""


def first(groups):
    return next((g for g in groups if g is not None), None)


REGEX_CLASS_ESCAPES = set("dDsSwWbB")
REGEX_METACHARS = set("^$.*+?()[]{}|")


def regex_core(rx):
    """Longest run of literal text in a regex, so it can be looked up as copy.
    An escaped character (`\\.`, `\\?`) is literal text; a class escape
    (`\\d`) or an unescaped metacharacter ends the run."""
    runs, run, chars = [], [], iter(rx)
    for ch in chars:
        if ch == "\\":
            nxt = next(chars, "")
            if nxt in REGEX_CLASS_ESCAPES:
                runs.append("".join(run))
                run = []
            else:
                run.append(nxt)
        elif ch in REGEX_METACHARS:
            runs.append("".join(run))
            run = []
        else:
            run.append(ch)
    runs.append("".join(run))
    return max(runs, key=len)


def contract_shaped(lit):
    if len(lit) < 3 or not re.search(r"[A-Za-z]", lit) or CODE_LIKE.search(lit):
        return True
    return bool(TOKEN.match(lit)) and not LABEL_WORD.match(lit)


def substring_lookup(corpus):
    """`lit in corpus`, memoized: a miss scans the whole corpus, and the same
    literal recurs across many tests."""
    return functools.lru_cache(maxsize=None)(corpus.__contains__)


def package_of(path):
    return os.path.dirname(path) or "."


def scan_ts(paths_filter):
    files = [f for t in TS_TREES for f in git_files(t)
             if re.search(r"\.(ts|tsx|mts)$", f) and not f.endswith(".d.ts")]
    in_prod = substring_lookup("\n".join(read(f) for f in files if not TS_TEST.search(f)))
    hits, fixtures = [], collections.Counter()
    for f in files:
        if not re.search(r"\.(test|spec)\.", f) or not f.startswith(paths_filter):
            continue
        text = read(f)
        imported = {}
        for names, src in TS_IMPORT.findall(text):
            if "testing" in src or src.startswith("vitest") or "@testing-library" in src:
                continue
            for n in names.split(","):
                n = n.strip().split(" as ")[-1].strip()
                if n:
                    imported[n] = src
        for ln, line in enumerate(text.splitlines(), 1):
            for rx, kind in ((TS_BYTEXT, "bytext"), (TS_MATCHER, "matcher")):
                for m in rx.finditer(line):
                    lit, regex, ident = first(m.groups()[:3]), m.group(4), m.group(5)
                    if ident:
                        if ident in imported and COPY_IDENT.search(ident):
                            hits.append((f, ln, "ts-constant", ident))
                        continue
                    probe = (lit if lit is not None else regex_core(regex or "")).strip()
                    if contract_shaped(probe):
                        continue
                    if not in_prod(probe):
                        fixtures[package_of(f)] += 1
                        continue
                    if kind == "bytext" and TS_CLICK.search(line[:m.start()]):
                        cat = "ts-locator"
                    else:
                        cat = "ts-assert"
                    hits.append((f, ln, cat, probe))
    return hits, fixtures


GO_ESCAPE = re.compile(r"\\(u[0-9a-fA-F]{4}|U[0-9a-fA-F]{8}|x[0-9a-fA-F]{2}|.)")
GO_SIMPLE_ESCAPES = {"n": "\n", "t": "\t", "r": "\r", "a": "\a", "b": "\b", "f": "\f", "v": "\v"}


def go_unquote(dq, raw):
    """A Go string literal's value: raw strings as-is; interpreted strings
    with their escapes decoded and every other character (em dashes, arrows)
    left alone."""
    if raw is not None:
        return raw

    def decode(m):
        esc = m.group(1)
        if esc[0] in "uUx":
            return chr(int(esc[1:], 16))
        return GO_SIMPLE_ESCAPES.get(esc, esc)

    return GO_ESCAPE.sub(decode, dq)


def scan_go(paths_filter):
    go = git_files("*.go")
    in_prod = substring_lookup("\n".join(read(f) for f in go if not f.endswith("_test.go")))
    in_assets = substring_lookup("\n".join(read(f) for f in git_files(*["*" + e for e in ASSET_EXT])
                                           if not f.startswith("docs/") and "/testdata/" not in f))
    hits, fixtures = [], collections.Counter()
    for f in go:
        if not f.endswith("_test.go") or not f.startswith(paths_filter):
            continue
        for ln, line in enumerate(read(f).splitlines(), 1):
            if line.lstrip().startswith("//"):
                continue
            for i, rx in enumerate(GO_CHECKS):
                for m in rx.finditer(line):
                    g = m.groups()
                    # GO_CHECKS[2] (regexp) puts the literal before the subject.
                    if i == 2:
                        lit, subject = regex_core(go_unquote(g[0], g[1])), g[2]
                    else:
                        subject, lit = g[0], go_unquote(g[1], g[2])
                    lit = lit.strip()
                    if contract_shaped(lit):
                        continue
                    if in_prod(lit):
                        cat = "go-errstring" if GO_ERRISH.search(subject or "") else "go-copy"
                    elif len(lit) >= 8 and " " in lit and in_assets(lit):
                        cat = "go-asset"
                    else:
                        fixtures[package_of(f)] += 1
                        continue
                    hits.append((f, ln, cat, lit))
    return hits, fixtures


def main():
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("--path", default="", help="only scan test files under this repo-relative prefix")
    ap.add_argument("--hits", help="write every hit as TSV (file, line, category, literal) to this path")
    ap.add_argument("--only", choices=["ts", "go"], help="scan only one language")
    ap.add_argument("--top", type=int, default=0, help="limit the package table to the N largest (0 = all)")
    args = ap.parse_args()

    hits, fixtures = [], collections.Counter()
    if args.only in (None, "ts"):
        h, fx = scan_ts(args.path); hits += h; fixtures += fx
    if args.only in (None, "go"):
        h, fx = scan_go(args.path); hits += h; fixtures += fx

    cats = ["ts-assert", "ts-locator", "ts-constant", "go-errstring", "go-copy", "go-asset"]
    per = collections.defaultdict(collections.Counter)
    for f, _, cat, _ in hits:
        per[package_of(f)][cat] += 1
    rows = sorted(per.items(), key=lambda kv: -sum(kv[1].values()))
    if args.top:
        rows = rows[:args.top]
    print("| package | total | " + " | ".join(cats) + " | fixture (excluded) |")
    print("|---|---:|" + "---:|" * len(cats) + "---:|")
    for pkg, c in rows:
        print(f"| `{pkg}` | {sum(c.values())} | " + " | ".join(str(c[k] or "") for k in cats) + f" | {fixtures[pkg] or ''} |")
    tot = collections.Counter(cat for _, _, cat, _ in hits)
    print(f"| **all ({len(per)} packages)** | **{len(hits)}** | " + " | ".join(f"**{tot[k]}**" for k in cats)
          + f" | **{sum(fixtures.values())}** |")
    if args.hits:
        with open(args.hits, "w", encoding="utf-8") as out:
            out.write("file\tline\tcategory\tliteral\n")
            for f, ln, cat, lit in sorted(hits):
                out.write(f"{f}\t{ln}\t{cat}\t{lit.replace(chr(9), ' ').replace(chr(10), ' ')[:160]}\n")
        print(f"\n{len(hits)} hits written to {args.hits}", file=sys.stderr)


if __name__ == "__main__":
    main()
