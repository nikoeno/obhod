package main

import (
	"os"

	"github.com/nikoejento/obhod/api/desktop"
)

func main() { os.Exit(desktop.Main(os.Args[1:])) }
