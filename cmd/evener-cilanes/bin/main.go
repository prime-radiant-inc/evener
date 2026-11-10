// Command evener-cilanes prints the CI lanes a pull request can skip; see
// package cilanes.
package main

import (
	"os"

	cilanes "primeradiant.com/evener/cmd/evener-cilanes"
)

func main() {
	os.Exit(cilanes.Run(os.Args[1:], os.Stdout, os.Stderr))
}
