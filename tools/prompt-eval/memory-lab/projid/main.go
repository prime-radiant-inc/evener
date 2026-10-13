// projid prints the evener project memory ID for a directory, so the memory
// lab can seed a project's memory before a session runs there.
package main

import (
	"fmt"
	"os"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/identifier"
)

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: projid <dir>")
		os.Exit(2)
	}
	env := execenv.NewLocalExecutionEnvironment(os.Args[1])
	p, err := identifier.ResolveProjectWith(os.Args[1], execenv.NewProjectResolver(env))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println(p.ID)
}
