package main

import (
	"os"

	"github.com/nikoejento/obhod/internal/blockanalyzer"
)

func main() { os.Exit(blockanalyzer.Main(os.Args[1:])) }
