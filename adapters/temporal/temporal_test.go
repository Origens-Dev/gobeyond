package temporal

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"os"
	"testing"
	"time"
)

func TestOptionsFromEnv(t *testing.T) {
	t.Setenv(EnvAddress, "temporal:7233")
	t.Setenv(EnvNamespace, "default")
	t.Setenv(EnvTaskQueue, "demo__local")
	t.Setenv(EnvTLSCert, "cert-pem")
	t.Setenv(EnvTLSKey, "key-pem")
	got := optionsFromEnv(Options{})
	if got.Address != "temporal:7233" || got.Namespace != "default" || got.TaskQueue != "demo__local" {
		t.Fatalf("got %#v", got)
	}
	if got.TLSCert != "cert-pem" || got.TLSKey != "key-pem" {
		t.Fatalf("TLS from env: %#v", got)
	}
}

func TestDialTLSConfigPlaintext(t *testing.T) {
	cfg, err := dialTLSConfig(Options{})
	if err != nil {
		t.Fatal(err)
	}
	if cfg != nil {
		t.Fatalf("expected nil TLS for local plaintext, got %#v", cfg)
	}
}

func TestDialTLSConfigAPIKey(t *testing.T) {
	cfg, err := dialTLSConfig(Options{APIKey: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	if cfg == nil || cfg.MinVersion != tls.VersionTLS12 {
		t.Fatalf("API key TLS: %#v", cfg)
	}
	if len(cfg.Certificates) != 0 {
		t.Fatalf("API key mode must not attach client certs: %#v", cfg.Certificates)
	}
}

func TestDialTLSConfigMTLS(t *testing.T) {
	certPEM, keyPEM := mustSelfSignedPEM(t)
	cfg, err := dialTLSConfig(Options{TLSCert: certPEM, TLSKey: keyPEM})
	if err != nil {
		t.Fatal(err)
	}
	if cfg == nil || cfg.MinVersion != tls.VersionTLS12 {
		t.Fatalf("mTLS TLS: %#v", cfg)
	}
	if len(cfg.Certificates) != 1 {
		t.Fatalf("want 1 client cert, got %d", len(cfg.Certificates))
	}
}

func TestDialTLSConfigHalfSet(t *testing.T) {
	certPEM, keyPEM := mustSelfSignedPEM(t)
	if _, err := dialTLSConfig(Options{TLSCert: certPEM}); err == nil {
		t.Fatal("expected error when only TLS cert is set")
	}
	if _, err := dialTLSConfig(Options{TLSKey: keyPEM}); err == nil {
		t.Fatal("expected error when only TLS key is set")
	}
}

func TestDialTLSConfigAPIKeyAndMTLS(t *testing.T) {
	certPEM, keyPEM := mustSelfSignedPEM(t)
	_, err := dialTLSConfig(Options{
		APIKey:  "secret",
		TLSCert: certPEM,
		TLSKey:  keyPEM,
	})
	if err == nil {
		t.Fatal("expected error when both API key and mTLS are set")
	}
}

func TestClientOptionsMTLS(t *testing.T) {
	certPEM, keyPEM := mustSelfSignedPEM(t)
	opts, err := clientOptions(Options{
		Address:   "ns.account.tmprl.cloud:7233",
		Namespace: "ns",
		TLSCert:   certPEM,
		TLSKey:    keyPEM,
	})
	if err != nil {
		t.Fatal(err)
	}
	if opts.Credentials != nil {
		t.Fatal("mTLS dial must not set API key credentials")
	}
	if opts.ConnectionOptions.TLS == nil || opts.ConnectionOptions.TLS.MinVersion != tls.VersionTLS12 {
		t.Fatalf("mTLS connection TLS: %#v", opts.ConnectionOptions.TLS)
	}
}

func mustSelfSignedPEM(t *testing.T) (certPEM, keyPEM string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "gobeyond-temporal-test"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	certPEMBytes := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	keyPEMBytes := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	return string(certPEMBytes), string(keyPEMBytes)
}

func TestSignalReadinessEmptyTarget(t *testing.T) {
	if err := signalReadiness("", "nonce"); err != nil {
		t.Fatal(err)
	}
}

func TestSignalReadinessUnixgram(t *testing.T) {
	// macOS sockaddr_un path limit — keep the socket path short.
	sock := "/tmp/gb-rdy-" + t.Name()[len(t.Name())-6:] + ".sock"
	_ = os.Remove(sock)
	t.Cleanup(func() { _ = os.Remove(sock) })
	addr, err := net.ResolveUnixAddr("unixgram", sock)
	if err != nil {
		t.Fatal(err)
	}
	ln, err := net.ListenUnixgram("unixgram", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	done := make(chan string, 1)
	go func() {
		buf := make([]byte, 64)
		n, _, _ := ln.ReadFromUnix(buf)
		done <- string(buf[:n])
	}()
	if err := signalReadiness("unixgram://"+sock, "proof"); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-done:
		if got != "proof" {
			t.Fatalf("got %q", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout")
	}
}
