package cmd

import (
	"crypto/x509"
	"errors"
	"net/http"
	"os"
	"testing"
	"time"
)

// These public endpoints serve the compromised direct and cross-signed chains.
// Opt in so normal tests do not depend on external DNS and network access.
func TestCertificateDistrust(t *testing.T) {
	if os.Getenv("K3SC_TLS_CHECK") != "1" {
		t.Skip("set K3SC_TLS_CHECK=1 to check public certificate test endpoints")
	}
	client := &http.Client{
		Timeout: 15 * time.Second,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	response, err := client.Get("https://example.com/")
	if err != nil {
		t.Fatalf("valid HTTPS control: %v", err)
	}
	response.Body.Close()
	for _, host := range []string{
		"dv-g3b-emudhra.we-have-a-problem.com",
		"dv-g3b-aaa-emudhra.we-have-a-problem.com",
	} {
		t.Run(host, func(t *testing.T) {
			response, err := client.Get("https://" + host + "/")
			if err == nil {
				response.Body.Close()
				t.Fatal("accepted a compromised certificate chain")
			}
			var invalid x509.CertificateInvalidError
			var unknown x509.UnknownAuthorityError
			if !errors.As(err, &invalid) && !errors.As(err, &unknown) {
				t.Fatalf("expected certificate rejection, got %v", err)
			}
		})
	}
}
