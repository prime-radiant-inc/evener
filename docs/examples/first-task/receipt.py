"""Receipt totals in integer cents. This tutorial starts with a bug."""


def total_cents(items):
    """Sum (price_cents, quantity) pairs of nonnegative integers."""
    return sum(price_cents for price_cents, quantity in items)
