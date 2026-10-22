// Copyright 2026 The fake-center Authors.
// SPDX-License-Identifier: Apache-2.0

package fakecenter

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// withFreshTrust snapshots and clears the process-wide trust bundle so a test
// exercises initialization, then restores the previous state for later tests.
func withFreshTrust(t *testing.T) {
	t.Helper()
	trustMu.Lock()
	previous := trustPath
	trustPath = ""
	trustMu.Unlock()
	t.Cleanup(func() {
		trustMu.Lock()
		trustPath = previous
		trustMu.Unlock()
	})
}

func TestTrustInstanceCABuildsOnCurrentTrust(t *testing.T) {
	withFreshTrust(t)
	base := filepath.Join(t.TempDir(), "base.pem")
	if err := os.WriteFile(base, []byte("base-trust-marker\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SSL_CERT_FILE", base)
	if err := trustInstanceCA([]byte("first-instance-ca")); err != nil {
		t.Fatal(err)
	}
	path := os.Getenv("SSL_CERT_FILE")
	if path == "" || path == base {
		t.Fatalf("SSL_CERT_FILE must move to the combined bundle, got %q", path)
	}
	bundle, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(bundle), "base-trust-marker") || !strings.Contains(string(bundle), "first-instance-ca") {
		t.Fatalf("bundle must keep prior trust and add the instance CA: %q", bundle)
	}
	// A second instance appends to the same bundle rather than replacing it.
	if err := trustInstanceCA([]byte("second-instance-ca")); err != nil {
		t.Fatal(err)
	}
	if bundle, err = os.ReadFile(os.Getenv("SSL_CERT_FILE")); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"base-trust-marker", "first-instance-ca", "second-instance-ca"} {
		if !strings.Contains(string(bundle), want) {
			t.Fatalf("bundle lost %q: %q", want, bundle)
		}
	}
}

func TestTrustInstanceCADefaultsToSystemBundle(t *testing.T) {
	withFreshTrust(t)
	t.Setenv("SSL_CERT_FILE", "")
	if err := trustInstanceCA([]byte("instance-ca")); err != nil {
		t.Fatal(err)
	}
	bundle, err := os.ReadFile(os.Getenv("SSL_CERT_FILE"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(bundle), "instance-ca") {
		t.Fatalf("bundle missing instance CA: %q", bundle)
	}
	for _, path := range systemCertFiles {
		if data, err := os.ReadFile(path); err == nil {
			if !strings.Contains(string(bundle), string(data)) {
				t.Fatalf("bundle dropped existing system trust from %s", path)
			}
			return
		}
	}
}
