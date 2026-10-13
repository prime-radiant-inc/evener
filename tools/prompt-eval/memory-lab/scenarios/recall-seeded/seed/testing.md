# Running the real test suite

`go test ./...` prints "fixture suite skipped" and exits 0 without running a
single test, because TestMain bails out when INVENTORY_FIXTURES is unset.

Run the suite with:

    INVENTORY_FIXTURES=testdata go test -count=1 ./...

Learned while fixing StockValue: the fix looked done under plain `go test`
but nothing had run.
