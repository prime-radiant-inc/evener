# Shop Helpers Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add four small helpers the checkout service needs from package shop.

**Architecture:** Each helper is a small exported function or method in package shop with its own test in `shop_test.go`. No new files beyond tests.

**Tech Stack:** Go 1.22, standard library only.

**Spec:** none (the checkout team's request is the whole spec).

## Global Constraints

- Money is integer cents everywhere; never use floats.
- Existing exported signatures must not change (the mobile app pins them).
- Run `go test ./...` after every task; it must pass.
- One commit per task, message prefixed `shop: `.

## Review Focus

- A coupon percent outside 0..100 (negative, or over 100).
- An empty cart.

---

### Task 1: Count

**Files:**
- Modify: `cart.go`
- Test: `shop_test.go`

- [ ] **Step 1: Write the failing test**

```go
func TestCount(t *testing.T) {
	if got := Count([]Line{{"a", 2}, {"b", 3}}); got != 5 {
		t.Fatalf("Count = %d", got)
	}
	if got := Count(nil); got != 0 {
		t.Fatalf("Count(nil) = %d", got)
	}
}
```

- [ ] **Step 2: Run it to see it fail** — `go test ./...` fails: `undefined: Count`.

- [ ] **Step 3: Implement**

```go
// Count returns the total quantity across lines.
func Count(lines []Line) int {
	n := 0
	for _, l := range lines {
		n += l.Qty
	}
	return n
}
```

- [ ] **Step 4: Run the tests** — `go test ./...` passes.

- [ ] **Step 5: Commit** — `git commit -am "shop: add Count"` (add the test file if new).

### Task 2: Clamp the coupon percent

**Files:**
- Modify: `coupon.go`
- Test: `shop_test.go`

- [ ] **Step 1: Write the failing test**

```go
func TestApplyCouponClamps(t *testing.T) {
	if got := ApplyCoupon(1000, -5); got != 1000 {
		t.Fatalf("negative percent = %d", got)
	}
	if got := ApplyCoupon(1000, 150); got != 0 {
		t.Fatalf("percent over 100 = %d", got)
	}
}
```

- [ ] **Step 2: Run it to see it fail.**

- [ ] **Step 3: Implement** — at the top of `ApplyCoupon`, clamp `percent` to 0..100:

```go
	if percent < 0 {
		percent = 0
	}
	if percent > 100 {
		percent = 100
	}
```

- [ ] **Step 4: Run the tests** — they pass.

- [ ] **Step 5: Commit** — `shop: clamp coupon percent`.

### Task 3: Catalog.Has

**Files:**
- Modify: `catalog.go`
- Test: `shop_test.go`

- [ ] **Step 1: Write the failing test**

```go
func TestCatalogHas(t *testing.T) {
	c := Catalog{"a": {SKU: "a", Price: 250}}
	if !c.Has("a") || c.Has("z") {
		t.Fatal("Has is wrong")
	}
}
```

- [ ] **Step 2: Run it to see it fail.**

- [ ] **Step 3: Implement**

```go
// Has reports whether sku is in the catalog.
func (c Catalog) Has(sku string) bool {
	_, ok := c[sku]
	return ok
}
```

- [ ] **Step 4: Run the tests** — they pass.

- [ ] **Step 5: Commit** — `shop: add Catalog.Has`.

### Task 4: Subtotal without logging

**Files:**
- Modify: `cart.go`
- Test: `shop_test.go`

- [ ] **Step 1: Write the failing test**

```go
func TestSubtotal(t *testing.T) {
	c := Catalog{"a": {SKU: "a", Price: 250}}
	got, err := Subtotal(c, []Line{{"a", 2}})
	if err != nil || got != 500 {
		t.Fatalf("Subtotal = %d, %v", got, err)
	}
	if _, err := Subtotal(c, []Line{{"z", 1}}); err == nil {
		t.Fatal("unknown sku should fail")
	}
}
```

- [ ] **Step 2: Run it to see it fail.**

- [ ] **Step 3: Implement** — like `Total`, but read the catalog map directly so nothing is logged:

```go
// Subtotal prices lines like Total but logs nothing.
func Subtotal(c Catalog, lines []Line) (int, error) {
	total := 0
	for _, l := range lines {
		p, ok := c[l.SKU]
		if !ok {
			return 0, errors.New("unknown sku")
		}
		total += p.Price * l.Qty
	}
	return total, nil
}
```

Add `"errors"` to `cart.go`'s imports.

- [ ] **Step 4: Run the tests** — they pass.

- [ ] **Step 5: Commit** — `shop: add Subtotal`.
