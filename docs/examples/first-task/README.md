# First task: repair a receipt total

`receipt.py` contains an intentional bug: `total_cents` ignores quantities.
Repair that function without changing this README or `check.py`.

## Contract

The input is a list of `(price_cents, quantity)` pairs. Both values are
nonnegative integers. Return the sum of each price multiplied by its quantity.
An empty list totals zero. Keep integer arithmetic; do not introduce currency
conversion, input validation, packages, or network calls.

## Check

Run in this directory with Python 3:

```sh
python3 check.py
```

The starting fixture fails three cases and exits 1. A correct repair prints
`PASS: 4 receipt cases` and exits 0. The expected amounts are hand-specified,
independent examples. The checker must remain unchanged.

Copy this fixture into a separate Git repository before asking an agent to edit
it. The complete walkthrough is in Evener's getting-started guide.
