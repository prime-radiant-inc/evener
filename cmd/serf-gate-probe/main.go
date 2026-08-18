package main

import (
	"context"
	"flag"
	"fmt"
	"os"
)

func main() {
	only := flag.String("only", "", "probe only this capability id (for reproducing one classification); default probes all")
	flag.Parse()

	caps := Classify(context.Background())
	printed := 0
	for _, c := range caps {
		if *only != "" && c.ID != *only {
			continue
		}
		status := "AVAILABLE"
		if !c.Available {
			status = "BLOCKED"
		}
		fmt.Printf("%s\t%s\t%s\t%s\n", c.ID, status, c.Reason, c.Rerun)
		printed++
	}
	if *only != "" && printed == 0 {
		fmt.Fprintf(os.Stderr, "serf-gate-probe: unknown capability %q\n", *only)
		os.Exit(2)
	}
}
