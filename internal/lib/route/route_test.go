package route

import "testing"

func TestNormalize(t *testing.T) {
	ok := map[string]string{
		"Example.COM":     "example.com",
		" example.com. ":  "example.com",
		"bücher.example":  "xn--bcher-kva.example",
		"пример.рф":       "xn--e1afmkfd.xn--p1ai",
		"a_b.example.com": "a_b.example.com",
	}
	for in, want := range ok {
		got, err := Normalize(in)
		if err != nil || got != want {
			t.Errorf("Normalize(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, in := range []string{"", "*.x.com", "1.2.3.4", "https://x.com", "x.com:443", "x.com/a", "a..b", "-a.com", "a b.com", "::1"} {
		if got, err := Normalize(in); err == nil {
			t.Errorf("Normalize(%q) = %q, want error", in, got)
		}
	}
}

func TestParse(t *testing.T) {
	if r, err := Parse(" Direct "); err != nil || r != Direct {
		t.Fatalf("got %q, %v", r, err)
	}
	if _, err := Parse("nope"); err == nil {
		t.Fatal("want error")
	}
	if !Tunnel.Checkable() || Block.Checkable() || Proxy.Checkable() {
		t.Fatal("Checkable")
	}
}
