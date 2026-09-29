//go:build lab

package testca

import (
	"crypto/x509"
	"testing"
)

func verify(t *testing.T, ca *CA, certDER []byte, host string) error {
	t.Helper()
	leaf, err := x509.ParseCertificate(certDER)
	if err != nil {
		t.Fatal(err)
	}
	_, err = leaf.Verify(x509.VerifyOptions{Roots: ca.Pool, DNSName: host})
	return err
}

func TestVariants(t *testing.T) {
	ca, err := New()
	if err != nil {
		t.Fatal(err)
	}
	good, _ := ca.Issue("example.test", "127.0.0.1")
	if err := verify(t, ca, good.Certificate[0], "example.test"); err != nil {
		t.Errorf("valid leaf: %v", err)
	}
	if err := verify(t, ca, good.Certificate[0], "127.0.0.1"); err != nil {
		t.Errorf("IP SAN: %v", err)
	}
	expired, _ := ca.IssueExpired("example.test")
	if err := verify(t, ca, expired.Certificate[0], "example.test"); err == nil {
		t.Error("expired leaf verified")
	}
	wrong, _ := ca.IssueWrongHost("example.test")
	if err := verify(t, ca, wrong.Certificate[0], "example.test"); err == nil {
		t.Error("wrong-host leaf verified")
	}
	ss, err := SelfSigned("example.test")
	if err != nil {
		t.Fatal(err)
	}
	if err := verify(t, ca, ss.Certificate[0], "example.test"); err == nil {
		t.Error("self-signed leaf verified")
	}
	other, _ := New()
	if err := verify(t, other, good.Certificate[0], "example.test"); err == nil {
		t.Error("leaf verified against a different CA")
	}
}
