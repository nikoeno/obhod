package paths

import (
	"os"
	"testing"
)

// Manual check of the real ACL call; it locks the directory, so it is opt-in and never runs by default.
func TestRestrictACLReal(t *testing.T) {
	dir := os.Getenv("OBHOD_TEST_ACL_DIR")
	if dir == "" {
		t.Skip("set OBHOD_TEST_ACL_DIR to a scratch directory to run")
	}
	if err := restrictACL(dir); err != nil {
		t.Fatal(err)
	}
}
