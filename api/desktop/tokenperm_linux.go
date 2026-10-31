package desktop

import (
	"fmt"
	"os"
)

// checkTokenPerm refuses a token file that group or others can access.
func checkTokenPerm(path string) error {
	st, err := os.Stat(path)
	if err != nil {
		return nil // readTokenFile reports the open error
	}
	if modeTooOpen(st.Mode()) {
		return fmt.Errorf("--token-file %s: the file is accessible by group or others (mode %04o); make it owner-only (chmod 600)", path, st.Mode().Perm())
	}
	return nil
}
