package shop

import "example.com/shop/oldlog"

// Line is one cart line.
type Line struct {
	SKU string
	Qty int
}

// Total prices the cart lines against the catalog.
func Total(c Catalog, lines []Line) (int, error) {
	total := 0
	for _, l := range lines {
		p, err := c.Lookup(l.SKU)
		if err != nil {
			oldlog.Errorf("pricing failed for %s: %v", l.SKU, err)
			return 0, err
		}
		total += p.Price * l.Qty
	}
	oldlog.Infof("cart total %d", total)
	return total, nil
}
