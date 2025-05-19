package main

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

const (
	autocertFile     = "/var/run/autocert.step.sm/site.crt"
	autocertKey      = "/var/run/autocert.step.sm/site.key"
	autocertRoot     = "/var/run/autocert.step.sm/root.crt"
	requestFrequency = 5 * time.Second
	tickFrequency    = 15 * time.Second
)

type rotator struct {
	sync.RWMutex
	certificate *tls.Certificate
}

func (r *rotator) getClientCertificate(*tls.CertificateRequestInfo) (*tls.Certificate, error) {
	r.RLock()
	defer r.RUnlock()
	return r.certificate, nil
}

func (r *rotator) loadCertificate(certFile, keyFile string) error {
	r.Lock()
	defer r.Unlock()

	c, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return err
	}

	r.certificate = &c

	return nil
}

func loadRootCertPool() (*x509.CertPool, error) {
	root, err := os.ReadFile(autocertRoot)
	if err != nil {
		return nil, err
	}

	pool := x509.NewCertPool()
	if ok := pool.AppendCertsFromPEM(root); !ok {
		return nil, errors.New("missing or invalid root certificate")
	}

	return pool, nil
}

func main() {
	if err := run(); err != nil {
		log.Println(err)
		os.Exit(1)
	}
}

func run() error {
	url := os.Getenv("HELLO_MTLS_URL")

	// Read the root certificate for our CA from disk
	roots, err := loadRootCertPool()
	if err != nil {
		return err
	}

	// Load certificate
	r := &rotator{}
	if err := r.loadCertificate(autocertFile, autocertKey); err != nil {
		return fmt.Errorf("error loading certificate and key: %w", err)
	}

	// Create an HTTPS client using our cert, key & pool
	client := &http.Client{
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{
				RootCAs:          roots,
				MinVersion:       tls.VersionTLS12,
				CurvePreferences: []tls.CurveID{tls.CurveP521, tls.CurveP384, tls.CurveP256},
				CipherSuites: []uint16{
					tls.TLS_ECDHE_ECDSA_WITH_CHACHA20_POLY1305,
					tls.TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256,
				},
				// GetClientCertificate is called when a server requests a
				// certificate from a client.
				//
				// In this example keep alives will cause the certificate to
				// only be called once, but if we disable them,
				// GetClientCertificate will be called on every request.
				GetClientCertificate: r.getClientCertificate,
			},
			// Add this line to get the certificate on every request.
			// DisableKeepAlives: true,
		},
	}

	// Schedule periodic re-load of certificate
	// A real implementation can use something like
	// https://github.com/fsnotify/fsnotify
	done := make(chan struct{})
	go func() {
		ticker := time.NewTicker(tickFrequency)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				fmt.Println("Checking for new certificate...")
				err := r.loadCertificate(autocertFile, autocertKey)
				if err != nil {
					log.Println("Error loading certificate and key", err)
				}
			case <-done:
				return
			}
		}
	}()
	defer close(done)

	for {
		// Make request
		r, err := client.Get(url)
		if err != nil {
			return err
		}
		defer r.Body.Close() //nolint:gocritic // false positive

		body, err := io.ReadAll(r.Body)
		if err != nil {
			return err
		}

		fmt.Printf("%s: %s\n", time.Now().Format(time.RFC3339), strings.Trim(string(body), "\n"))

		time.Sleep(requestFrequency)
	}
}
