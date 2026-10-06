package inventory

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

var fixtures []Item

func TestMain(m *testing.M) {
	dir := os.Getenv("INVENTORY_FIXTURES")
	if dir == "" {
		fmt.Println("fixture suite skipped")
		os.Exit(0)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "stock.json"))
	if err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
	if err := json.Unmarshal(raw, &fixtures); err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
	os.Exit(m.Run())
}

func TestStockValue(t *testing.T) {
	if got := StockValue(fixtures); got != 4650 {
		t.Fatalf("StockValue = %d, want 4650", got)
	}
}

func TestNeedsReorder(t *testing.T) {
	if got := NeedsReorder(fixtures); !reflect.DeepEqual(got, []string{"bolt-m4", "washer-8"}) {
		t.Fatalf("NeedsReorder = %v", got)
	}
}
