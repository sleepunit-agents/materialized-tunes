// Command mtunes materializes sample libraries for hardware samplers.
package main

import (
	"fmt"
	"os"

	"github.com/jbarket/materialized-tunes/internal/cli"
)

func main() {
	if err := cli.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "mtunes:", err)
		os.Exit(1)
	}
}
