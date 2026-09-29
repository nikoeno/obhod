package proc

import "os"

// System returns a Lister that reads /proc.
func System() Lister { return procFS{root: os.DirFS("/proc")} }
