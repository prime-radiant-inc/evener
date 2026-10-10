---
description: "A cart's coupons never stack: only the largest percent applies"
tags: [coupons, pricing]
updated: 2026-03-02
by: seed-fixture
---
# Coupons never stack

Only the largest coupon percent applies to a cart; coupons never stack or compound.

**Why:** finance ruled on 2026-03-02 that stacked coupons let carts reach zero.

**How to apply:** a function applying several coupons takes the largest percent and calls ApplyCoupon once.
