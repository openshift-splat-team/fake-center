// Copyright 2026 The fake-center Authors.
// SPDX-License-Identifier: Apache-2.0

package fakecenter

import (
	"os"
	"path/filepath"
	"sync"
)

var (
	trustMu   sync.Mutex
	trustPath string
)

// systemCertFiles mirrors crypto/x509's Linux system bundle locations.
var systemCertFiles = []string{
	"/etc/ssl/certs/ca-certificates.crt",
	"/etc/pki/tls/certs/ca-bundle.crt",
	"/etc/ssl/ca-bundle.pem",
	"/etc/pki/tls/cacert.pem",
	"/etc/pki/ca-trust/extracted/pem/tls-ca-bundle.pem",
}

// trustInstanceCA appends ca to a process-wide combined trust bundle and
// points the Go-standard SSL_CERT_FILE override at it. The bundle starts
// from the process's existing trust (the current SSL_CERT_FILE, or the
// system bundle), so nothing that already worked stops working: trust is
// only added. Components using default TLS then trust this instance
// without code changes.
//
// The file intentionally outlives the test: Go caches system roots after
// first use, and later code in the same process may still read the
// variable after the instance is gone.
func trustInstanceCA(ca []byte) error {
	trustMu.Lock()
	defer trustMu.Unlock()
	if trustPath == "" {
		var base []byte
		if current := os.Getenv("SSL_CERT_FILE"); current != "" {
			if data, err := os.ReadFile(current); err == nil {
				base = data
			}
		} else {
			for _, path := range systemCertFiles {
				if data, err := os.ReadFile(path); err == nil {
					base = data
					break
				}
			}
		}
		dir, err := os.MkdirTemp("", "fake-center-trust-")
		if err != nil {
			return err
		}
		trustPath = filepath.Join(dir, "combined.pem")
		if err := os.WriteFile(trustPath, base, 0o600); err != nil {
			return err
		}
		if err := os.Setenv("SSL_CERT_FILE", trustPath); err != nil {
			return err
		}
	}
	f, err := os.OpenFile(trustPath, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.Write(append(append([]byte("\n"), ca...), '\n'))
	return err
}
