package shop

import (
	"errors"

	"example.com/shop/oldlog"
)

// Product is one catalog entry.
type Product struct {
	SKU   string
	Price int // cents
}

// Catalog maps SKUs to products.
type Catalog map[string]Product

// Lookup returns the product for sku.
func (c Catalog) Lookup(sku string) (Product, error) {
	p, ok := c[sku]
	if !ok {
		oldlog.Warnf("catalog miss for %s", sku)
		return Product{}, errors.New("unknown sku")
	}
	oldlog.Infof("catalog hit for %s", sku)
	return p, nil
}
