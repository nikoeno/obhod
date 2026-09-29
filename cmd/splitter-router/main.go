package main

import (
	"os"

	"github.com/nikoejento/obhod/internal/router"
)

func main() { os.Exit(router.Main(os.Args[1:])) }
