# Coupons at checkout

Goal: checkout can show item counts and apply a coupon to a cart.

## Steps

1. Add `Count(lines []Line) int`, returning the total quantity across cart lines. Add tests.
2. Add `Discounted(c Catalog, lines []Line, percent int) (int, error)`, which prices the cart with `Total` and applies the coupon percent with `ApplyCoupon`. This is the checkout API boundary. Add tests.
3. Release v1.1: tag the release and publish it.
