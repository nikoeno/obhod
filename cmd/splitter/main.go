package main

import (
	"os"

	"github.com/nikoejento/obhod/internal/pc"
)

func main() { os.Exit(pc.Main(os.Args[1:])) }
