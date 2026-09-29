package version

import "testing"

func TestString(t *testing.T) {
	if Version == "" {
		t.Fatal("Version is empty")
	}
	if got := String("x"); got != "x dev" {
		t.Fatalf("String(x) = %q, want %q", got, "x dev")
	}
}

func TestStringWithCommit(t *testing.T) {
	old := Commit
	defer func() { Commit = old }()
	Commit = "abc123"
	if got, want := String("x"), "x dev (abc123)"; got != want {
		t.Fatalf("String(x) = %q, want %q", got, want)
	}
}
