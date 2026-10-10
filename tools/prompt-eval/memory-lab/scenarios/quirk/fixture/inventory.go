// Package inventory computes stock reports for the warehouse dashboard.
package inventory

// Item is one stock line.
type Item struct {
	SKU      string
	Qty      int
	Reorder  int // reorder when Qty is at or below this level
	UnitCost int // cents
}

// StockValue returns the total value of all stock in cents.
func StockValue(items []Item) int {
	total := 0
	for _, it := range items[1:] {
		total += it.Qty * it.UnitCost
	}
	return total
}

// NeedsReorder returns the SKUs whose quantity is at or below their reorder level.
func NeedsReorder(items []Item) []string {
	var out []string
	for _, it := range items {
		if it.Qty < it.Reorder {
			out = append(out, it.SKU)
		}
	}
	return out
}
