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
    python3 tools/copy-oracle/inventory.py --copy-only tests.tsv   # tests that only test wording

--copy-only narrows the question from "where is copy asserted" to "which tests
exist only to test copy": every check in the test is a copy hit and the test's
name says its subject is wording. Behavior tests whose only oracle happens to
be copy are listed separately as incidental. Run its tests with
`python3 -B tools/copy-oracle/test_inventory.py`.

--copy-only is advisory. It builds review lists; it never decides on its own.
It reads one line at a time, so it can misjudge:
  - tests that contain multi-line raw strings, template literals or block
    comments, whose boundaries it can get wrong;
  - assertions or conditions split across lines (`expect(...)` then
    `.toHaveLength(n)` on the next line), which it classifies by their first
    line;
  - assertion forms it doesn't recognise, such as `expect.soft` and
    `expect.poll`.
  - quotes in TS: a quote straight after a word is read as JSX text unless
    the word is a known keyword, so an unusual construct can still blur a
    test's boundaries.
Read every listed test before deleting or converting it.

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


TS_TEST_START = re.compile(r"^\s*(?:test|it)(?:\.(?:only|skip|concurrent|each\(.*?\)))?\s*\(\s*" + STR)
TS_CHECK = re.compile(r"\bexpect\(|\b(?:get|find)(?:All)?By\w+\(")
GO_TEST_START = re.compile(r"^func (Test\w+)\(")
# A Go check is a condition line. `if err != nil {` right after a call is a
# setup guard, not a claim; expecting an error (`err == nil`) is a claim.
GO_CHECK = re.compile(r"^\s*if\b|\b(?:require|assert)\.\w+\(")
GO_SETUP_GUARD = re.compile(r"^\s*if\s+err\s*!=\s*nil\s*\{")


# Characters after which a `/` in TypeScript starts a regex literal, not a
# division. `<` and `>` are left out: after them a `/` is JSX (`</div>`, or a
# child's text like `<span>/tmp</span>`), and a regex there is vanishingly rare.
TS_REGEX_PRECEDERS = set("(,=:[!&|?{};+-*%~^")


TS_EXPRESSION_KEYWORDS = {"return", "case", "typeof", "in", "of", "await", "yield", "else", "throw",
                          "void", "delete", "new", "instanceof", "do",
                          # type positions: `value as "x"`, `satisfies "x"`, `extends "x"`
                          "as", "satisfies", "extends", "keyof", "is"}


def word_before(line, i):
    """The identifier-like word ending just before position i, skipping spaces."""
    j = i
    while j > 0 and line[j - 1].isspace():
        j -= 1
    k = j
    while k > 0 and (line[k - 1].isalnum() or line[k - 1] in "_$"):
        k -= 1
    return line[k:j]


def sanitize(line, lang):
    """The line with the contents of strings, TS regex literals and comments
    replaced by spaces. Its length is unchanged, so offsets found in it apply
    to the original line; braces, parentheses, operators and semicolons inside
    text no longer count as code."""
    out, i, n, prev = list(line), 0, len(line), ""

    def blank(a, b):
        for k in range(a, min(b, n)):
            out[k] = " "

    while i < n:
        c = line[i]
        if c == "/" and i + 1 < n and line[i + 1] == "/":
            blank(i, n)
            break
        # In TS, a quote straight after a word is JSX text ("Don't retry"),
        # unless that word is a keyword that takes an expression (`return "x"`).
        if c in "\"'`" and not (lang == "ts" and c != "`" and prev.isalnum()
                                 and word_before(line, i) not in TS_EXPRESSION_KEYWORDS):
            j = i + 1
            while j < n and line[j] != c:
                j += 2 if line[j] == "\\" else 1
            blank(i + 1, j)
            i, prev = j + 1, c
            continue
        if lang == "ts" and c == "/" and (prev == "" or prev in TS_REGEX_PRECEDERS):
            j, in_class = i + 1, False
            while j < n and (line[j] != "/" or in_class):
                if line[j] == "\\":
                    j += 2
                    continue
                in_class = (in_class or line[j] == "[") and line[j] != "]"
                j += 1
            blank(i + 1, j)
            i, prev = j + 1, "/"
            continue
        if not c.isspace():
            prev = c
        i += 1
    return "".join(out)


GO_COPY_OPERATOR = re.compile(r"strings\.\w+\(|\.MatchString\(|[=!]=\s*[\"`]|[\"`]\s*[=!]=")


def go_copy_term(term):
    """A condition term that asks only about text: a copy operator applied to a
    string literal that isn't a machine-contract token (`status != "running"`
    checks state, not wording)."""
    literals = [go_unquote(m.group(1), m.group(2)).strip() for m in re.finditer(GO_STR, term)]
    return bool(GO_COPY_OPERATOR.search(term)) and any(not contract_shaped(lit) for lit in literals)


def go_if_initializer(line):
    """The `init` statement of `if init; cond {`, or "" when there is none."""
    code = sanitize(line, "go")
    m = re.search(r"\bif\b", code)
    if not m:
        return ""
    hi = code.rfind("{")
    semi = code.rfind(";", m.end(), hi if hi > m.end() else len(code))
    return line[m.end():semi] if semi >= 0 else ""


def go_condition_terms(line):
    """The && / || terms of an `if` condition, after any `init;` statement.
    Every boundary is found in the sanitized line and applied to the original,
    so operators, braces and semicolons inside strings or comments don't count."""
    code = sanitize(line, "go")
    m = re.search(r"\bif\b", code)
    if not m:
        return [line]
    lo, hi = m.end(), code.rfind("{")
    hi = hi if hi > lo else len(code)
    semi = code.rfind(";", lo, hi)
    if semi >= 0:
        lo = semi + 1
    terms, last = [], lo
    for op in re.finditer(r"&&|\|\|", code[lo:hi]):
        terms.append(line[last:lo + op.start()])
        last = lo + op.end()
    terms.append(line[last:hi])
    return [t for t in terms if t.strip()]


def test_blocks(text, lang):
    """(start line, name, [(line number, line)]) for each test in a file."""
    lines = text.splitlines()
    blocks, i = [], 0
    while i < len(lines):
        if lang == "ts":
            m = TS_TEST_START.match(lines[i])
            name = first(m.groups()) if m else None
        else:
            m = GO_TEST_START.match(lines[i])
            name = m.group(1) if m else None
        if not m:
            i += 1
            continue
        # A TS test ends where its test(...) call closes, which also covers
        # expression-bodied callbacks with no braces; a Go test ends where its
        # func body closes.
        open_ch, close_ch = ("(", ")") if lang == "ts" else ("{", "}")
        depth, body, j, opened = 0, [], i, False
        while j < len(lines):
            code = sanitize(lines[j], lang)
            depth += code.count(open_ch) - code.count(close_ch)
            opened = opened or open_ch in code
            body.append((j + 1, lines[j]))
            j += 1
            if opened and depth <= 0:
                break
        blocks.append((i + 1, name, body))
        i = j
    return blocks


# A check that is about something other than the wording, even when a copy
# hit sits on the same line: an expected error, or text used only to find an
# element whose attribute or state is then checked.
BEHAVIOR_ON_LINE = re.compile(
    r"\berr\s*==\s*nil|getAttribute\(|toHaveAttribute|\.dataset\b|className|\.checked\b|\.disabled\b"
    r"|toHaveLength|toHaveBeenCalled|toBeGreaterThan|toBeLessThan|\blen\(|[=!<>]=?\s*-?\d+\b")
# Test names that say the subject is wording.
COPY_PURPOSE_NAME = re.compile(
    r"show|display|render|label|title|text|copy|message|word|say|mention|hint|placeholder|tooltip|"
    r"caption|usage|help|heading|banner|describ|phrase|string", re.I)
# Lines that carry no input to the code under test: comments and failure diagnostics.
NOT_INPUT = re.compile(r"^\s*(?://|/?\*)|\bt\.(?:Fatal|Error|Log|Skip)f?\(")
QUOTED = re.compile(r"""\"((?:[^\"\\]|\\.){3,})\"|'((?:[^'\\]|\\.){3,})'|`([^`]{3,})`""")


def copy_only_tests(text, hit_lines, lang, purpose="copy"):
    """Tests whose every check is a copy hit.

    purpose="copy" (the default) keeps only tests whose name says their subject
    is wording: those exist to test copy. purpose="behavior" returns the others:
    behavior tests whose only oracle happens to be copy, which are incidental.
    A check is not a copy claim when it expects an error, checks an attribute
    or state of an element found by its text, or matches text the test itself
    fed in (fixture data flowing through)."""
    found = []
    for start, name, body in test_blocks(text, lang):
        if lang == "ts":
            checks = [(n, line) for n, line in body if TS_CHECK.search(line)]
        else:
            checks = [(n, line) for n, line in body if GO_CHECK.search(line) and not GO_SETUP_GUARD.match(line)]
        check_numbers = {n for n, _ in checks}
        inputs = "\n".join(line for n, line in body[1:] if n not in check_numbers and not NOT_INPUT.search(line))

        def copy_claim(n, line):
            if n not in hit_lines or BEHAVIOR_ON_LINE.search(line):
                return False
            if lang == "go" and not all(go_copy_term(t) for t in go_condition_terms(line)):
                return False
            if lang == "go":
                # Compare the condition's literals with what the test supplied,
                # the `if` initializer included; the initializer's own literals
                # are inputs, not assertions.
                supplied = inputs + "\n" + go_if_initializer(line)
                asserted = " ".join(go_condition_terms(line))
            else:
                supplied, asserted = inputs, line
            literals = [first(m.groups()) for m in QUOTED.finditer(asserted)]
            return not any(lit and lit in supplied for lit in literals)

        if not checks or not all(copy_claim(n, line) for n, line in checks):
            continue
        is_copy_purpose = bool(COPY_PURPOSE_NAME.search(name or ""))
        if is_copy_purpose == (purpose == "copy"):
            found.append((start, name))
    return found


def area_of(path):
    parts = path.split("/")
    if parts[0] == "cmd" and len(parts) > 2 and parts[2] == "frontend":
        return "/".join(parts[:3])
    return "/".join(parts[:2]) if parts[0] == "cmd" else parts[0]


def report_copy_only(hits, out_path):
    """Per area: tests that exist to test wording (the targets), and behavior
    tests whose only oracle is copy (incidental; listed, not targeted)."""
    by_file = collections.defaultdict(set)
    for f, ln, _, _ in hits:
        by_file[f].add(ln)
    rows = []
    for f, lines in sorted(by_file.items()):
        lang = "go" if f.endswith(".go") else "ts"
        text = read(f)
        for purpose in ("copy", "behavior"):
            for start, name in copy_only_tests(text, lines, lang, purpose):
                rows.append((f, start, name, purpose))
    per = collections.defaultdict(collections.Counter)
    for f, _, _, purpose in rows:
        per[area_of(f)][purpose] += 1
    print("| area | copy-only (targets) | behavior, copy-only oracle (incidental) |")
    print("|---|---:|---:|")
    for area, c in sorted(per.items(), key=lambda kv: -kv[1]["copy"]):
        print(f"| `{area}` | {c['copy'] or ''} | {c['behavior'] or ''} |")
    tot = collections.Counter(p for *_, p in rows)
    print(f"| **all** | **{tot['copy']}** | **{tot['behavior']}** |")
    with open(out_path, "w", encoding="utf-8") as out:
        out.write("file\tline\ttest\tpurpose\n")
        for f, start, name, purpose in rows:
            out.write(f"{f}\t{start}\t{name}\t{purpose}\n")
    print(f"\n{len(rows)} tests written to {out_path}", file=sys.stderr)


def main():
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("--path", default="", help="only scan test files under this repo-relative prefix")
    ap.add_argument("--hits", help="write every hit as TSV (file, line, category, literal) to this path")
    ap.add_argument("--only", choices=["ts", "go"], help="scan only one language")
    ap.add_argument("--top", type=int, default=0, help="limit the package table to the N largest (0 = all)")
    ap.add_argument("--copy-only", metavar="TSV",
                    help="instead of every hit, list the tests whose every check is a copy hit to this TSV: "
                         "those whose name says they test wording (targets) and behavior tests whose "
                         "only oracle is copy (incidental), with per-area counts")
    args = ap.parse_args()

    hits, fixtures = [], collections.Counter()
    if args.only in (None, "ts"):
        h, fx = scan_ts(args.path); hits += h; fixtures += fx
    if args.only in (None, "go"):
        h, fx = scan_go(args.path); hits += h; fixtures += fx

    if args.copy_only:
        report_copy_only(hits, args.copy_only)
        return

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
