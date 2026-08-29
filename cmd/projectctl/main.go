// Command projectctl is the root entrypoint for the projectctl CLI.
package main

import (
	"fmt"
	"os"

	"github.com/Choaterboater/projectctl/internal/version"
)

func main() {
	for _, arg := range os.Args[1:] {
		if arg == "--version" || arg == "-version" || arg == "version" {
			fmt.Println(version.String())
			return
		}
	}
	fmt.Fprintln(os.Stderr, "usage: projectctl [--version]")
	os.Exit(2)
}
