package main

import (
	"os"

	"github.com/rohan/jasper/internal/ports/cli"
)

func main() { os.Exit(cli.Main(os.Args[1:])) }
