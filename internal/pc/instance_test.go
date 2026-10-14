package pc

import (
	"errors"
	"testing"
)

func TestInstanceLockIsExclusive(t *testing.T) {
	dir := t.TempDir()
	release, err := lockInstance(dir)
	if err != nil {
		t.Fatal(err)
	}
	if r2, err := lockInstance(dir); !errors.Is(err, errAnotherInstance) {
		if r2 != nil {
			r2()
		}
		t.Fatalf("second lock: err = %v", err)
	}
	release()
	r3, err := lockInstance(dir)
	if err != nil {
		t.Fatalf("lock after release: %v", err)
	}
	r3()
}
