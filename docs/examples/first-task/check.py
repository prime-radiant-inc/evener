"""Independent acceptance examples. Run with python3 check.py."""

import sys

from receipt import total_cents

CASES = (
    ("empty", [], 0),
    ("quantity", [(250, 3)], 750),
    ("mixed", [(125, 2), (80, 3)], 490),
    ("zero quantity", [(999, 0), (50, 2)], 100),
)


def main():
    failures = 0
    for name, items, expected in CASES:
        actual = total_cents(items)
        if actual != expected:
            print(f"FAIL: {name}: expected {expected}, got {actual}")
            failures += 1
    if failures:
        return 1
    print(f"PASS: {len(CASES)} receipt cases")
    return 0


if __name__ == "__main__":
    sys.exit(main())
