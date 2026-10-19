package provider

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// sslCertChildEnv marks the re-executed test binary that verifies one leaf
// against the system roots. System roots load once per process, so the
// property can only be observed in a process that set SSL_CERT_FILE before
// its first verification.
const sslCertChildEnv = "TF_FERENTIN_TEST_SSL_CERT_CHILD"

// On macOS and Windows, SSL_CERT_FILE must not replace platform trust. Go 1.27
// made it replace it by default, so a CA file and a proxy in the environment
// terraform runs in could intercept the auth server and the admin API.
// go.mod's `godebug x509sslcertoverrideplatform=0` turns that off, and a local
// go.work has to repeat it (DEVELOPMENT.md); this test notices either missing,
// or a toolchain that re-enables it.
//
// It checks behaviour rather than the go.mod text: a leaf signed by a CA that
// exists only in SSL_CERT_FILE must fail verification against the system
// roots. With the override on, it verifies. On Linux SSL_CERT_FILE has always
// been the trust store and the setting does nothing, so the test is skipped.
func TestSSLCertFile_DoesNotReplacePlatformTrust(t *testing.T) {
	if os.Getenv(sslCertChildEnv) != "" {
		verifyLeafAgainstSystemRoots(t)
		return
	}
	if runtime.GOOS != "darwin" && runtime.GOOS != "windows" {
		t.Skip("SSL_CERT_FILE replaces the trust store on this OS by design")
	}

	dir := t.TempDir()
	caPEM, leafPEM := throwawayChain(t)
	caFile := filepath.Join(dir, "ca.pem")
	leafFile := filepath.Join(dir, "leaf.pem")
	if err := os.WriteFile(caFile, caPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(leafFile, leafPEM, 0o600); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command(os.Args[0], "-test.run=^TestSSLCertFile_DoesNotReplacePlatformTrust$", "-test.v")
	cmd.Env = append(os.Environ(),
		sslCertChildEnv+"="+leafFile,
		"SSL_CERT_FILE="+caFile,
		"SSL_CERT_DIR=",
		// The variable overrides the go.mod default; an inherited one would
		// make this test about the developer's shell.
		"GODEBUG=",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("child: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "leaf refused by system roots") {
		t.Fatalf("child did not report its verdict:\n%s", out)
	}
}

func verifyLeafAgainstSystemRoots(t *testing.T) {
	raw, err := os.ReadFile(os.Getenv(sslCertChildEnv))
	if err != nil {
		t.Fatal(err)
	}
	block, _ := pem.Decode(raw)
	leaf, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := leaf.Verify(x509.VerifyOptions{DNSName: "auth.ferentin.net"}); err == nil {
		t.Fatal("a leaf signed only by the CA in SSL_CERT_FILE verified against the system roots: " +
			"SSL_CERT_FILE replaced platform trust (is `godebug x509sslcertoverrideplatform=0` in go.mod, " +
			"and in go.work if you use one?)")
	} else {
		t.Logf("leaf refused by system roots: %v", err)
	}
}

// throwawayChain mints a CA and a leaf for auth.ferentin.net signed by it.
func throwawayChain(t *testing.T) (caPEM, leafPEM []byte) {
	t.Helper()
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	caTmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "ferentin provider test root"},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.Add(time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTmpl, caTmpl, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	ca, err := x509.ParseCertificate(caDER)
	if err != nil {
		t.Fatal(err)
	}
	leafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	leafTmpl := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: "auth.ferentin.net"},
		DNSNames:     []string{"auth.ferentin.net"},
		NotBefore:    now.Add(-time.Hour),
		NotAfter:     now.Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	leafDER, err := x509.CreateCertificate(rand.Reader, leafTmpl, ca, &leafKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER}),
		pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: leafDER})
}
