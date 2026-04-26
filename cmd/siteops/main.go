package main

import (
	"os"

	"ffreis-siteops/internal/cli"
)

func main() {
	os.Exit(cli.Run("siteops", os.Args[1:]))
}
