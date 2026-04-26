package main

import (
	"os"

	"ffreis-siteops/internal/cli"
)

func main() {
	os.Exit(cli.Run("website-stitcher", os.Args[1:]))
}
