//go:build lab

package testca

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"math/big"
	"net"
	"sync/atomic"
	"time"
)

// CA is an in-memory certificate authority.
type CA struct {
	Pool *x509.CertPool // pass to clients under test
	Cert *x509.Certificate

	key    *ecdsa.PrivateKey
	serial atomic.Int64
}

// New creates a CA with a fresh P-256 key, valid for one day.
func New() (*CA, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	ca := &CA{key: key}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(ca.serial.Add(1)),
		Subject:               pkix.Name{CommonName: "obhod test CA"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, err
	}
	if ca.Cert, err = x509.ParseCertificate(der); err != nil {
		return nil, err
	}
	ca.Pool = x509.NewCertPool()
	ca.Pool.AddCert(ca.Cert)
	return ca, nil
}

// Issue returns a valid leaf certificate for hosts.
func (ca *CA) Issue(hosts ...string) (tls.Certificate, error) {
	return ca.issue(hosts, time.Now().Add(-time.Hour), time.Now().Add(23*time.Hour))
}

// IssueExpired returns a leaf certificate for hosts that has already expired.
func (ca *CA) IssueExpired(hosts ...string) (tls.Certificate, error) {
	return ca.issue(hosts, time.Now().Add(-48*time.Hour), time.Now().Add(-24*time.Hour))
}

// IssueWrongHost returns a validly signed leaf whose SAN does not match hosts.
func (ca *CA) IssueWrongHost(hosts ...string) (tls.Certificate, error) {
	return ca.issue([]string{"wrong-host.invalid"}, time.Now().Add(-time.Hour), time.Now().Add(23*time.Hour))
}

// SelfSigned returns a leaf certificate that no CA signed.
func SelfSigned(hosts ...string) (tls.Certificate, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, err
	}
	tmpl := leaf(big.NewInt(1), hosts, time.Now().Add(-time.Hour), time.Now().Add(23*time.Hour))
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return tls.Certificate{}, err
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}, nil
}

func leaf(serial *big.Int, hosts []string, from, to time.Time) *x509.Certificate {
	c := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: "obhod test leaf"},
		NotBefore:    from,
		NotAfter:     to,
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	for _, h := range hosts {
		if ip := net.ParseIP(h); ip != nil {
			c.IPAddresses = append(c.IPAddresses, ip)
		} else {
			c.DNSNames = append(c.DNSNames, h)
		}
	}
	return c
}

func (ca *CA) issue(hosts []string, from, to time.Time) (tls.Certificate, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, err
	}
	tmpl := leaf(big.NewInt(ca.serial.Add(1)), hosts, from, to)
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca.Cert, &key.PublicKey, ca.key)
	if err != nil {
		return tls.Certificate{}, err
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}, nil
}

// RootPool returns the pool clients under test must trust (same as the Pool field).
func (ca *CA) RootPool() *x509.CertPool { return ca.Pool }

// IssueSelfSigned is SelfSigned as a method, so a CA satisfies interfaces that need it.
func (ca *CA) IssueSelfSigned(hosts ...string) (tls.Certificate, error) { return SelfSigned(hosts...) }
