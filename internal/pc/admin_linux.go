//go:build linux

package pc

import (
	"bufio"
	"os"
	"strconv"
	"strings"
)

const capNetAdmin = 12

// isAdmin reports whether the process is root or holds CAP_NET_ADMIN.
func isAdmin() (bool, error) {
	if os.Geteuid() == 0 {
		return true, nil
	}
	f, err := os.Open("/proc/self/status")
	if err != nil {
		return false, nil // cannot tell; the first failing child reports ErrNeedsAdmin
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if rest, ok := strings.CutPrefix(sc.Text(), "CapEff:"); ok {
			v, err := strconv.ParseUint(strings.TrimSpace(rest), 16, 64)
			return err == nil && v&(1<<capNetAdmin) != 0, nil
		}
	}
	return false, nil
}

const elevationHint = "run splitter as root, for example: sudo splitter run"
