package main

import (
	"fmt"
	"os"
)

const usage = `Stead is a personal agent harness for managing delegated tasks.

Usage: stead [--help]

  -h, --help  Show this help.
`

func main() {
	args := os.Args[1:]
	if len(args) == 0 || (len(args) == 1 && (args[0] == "-h" || args[0] == "--help")) {
		fmt.Print(usage)
		return
	}

	fmt.Fprintln(os.Stderr, "Invalid arguments. Run 'stead --help' for usage.")
	os.Exit(2)
}
