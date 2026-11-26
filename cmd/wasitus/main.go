// Command wasitus builds an incident timeline from observability and change data
// and reports whether the evidence points at our own change or an external cause.
package main

import (
	"fmt"
	"os"
)

// version is set at build time by the release tooling.
var version = "0.0.0-dev"

func main() {
	if len(os.Args) > 1 && os.Args[1] == "version" {
		fmt.Println("wasitus", version)
		return
	}
	fmt.Fprintln(os.Stderr, "wasitus: not implemented yet, see docs/PLAN.md")
	os.Exit(2)
}
