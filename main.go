package main

import (
	"os"

	"polka/cli"
)

func main() {
	os.Exit(cli.Run(os.Stdout, os.Stderr, os.Args[1:]))
}
