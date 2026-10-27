// Copyright 2026 The fake-center Authors.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bufio"
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestBinaryConfigurationAndShutdown(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "fake-center")
	build := exec.Command("go", "build", "-o", binary, ".")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, output)
	}
	const yaml = "failureDomains: [{}]\naccounts: [{name: installer}]\n"
	path := filepath.Join(t.TempDir(), "scenario.yaml")
	if err := os.WriteFile(path, []byte(yaml), 0600); err != nil {
		t.Fatal(err)
	}
	for _, source := range []string{"environment", "file"} {
		t.Run(source, func(t *testing.T) {
			cmd := exec.Command(binary, "-listen", "127.0.0.1:0", "-san", "flag.example.test", "-san", "192.0.2.20")
			cmd.Env = append(os.Environ(), "VCENTER_TEST_PROFILES_SANS= env.example.test,192.0.2.30 ")
			if source == "environment" {
				// Invalid file proves the inline environment content takes precedence.
				cmd.Args = append(cmd.Args, "-scenario", "/does-not-exist")
				cmd.Env = append(cmd.Env, "VCENTER_TEST_PROFILES_SCENARIO="+yaml)
			} else {
				cmd.Args = append(cmd.Args, "-scenario", path)
				cmd.Env = append(cmd.Env, "VCENTER_TEST_PROFILES_SCENARIO=")
			}
			stderr, err := cmd.StderrPipe()
			if err != nil {
				t.Fatal(err)
			}
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = cmd.Process.Kill() })
			ready := make(chan string, 1)
			go func() {
				scanner := bufio.NewScanner(stderr)
				for scanner.Scan() {
					if _, endpoint, ok := strings.Cut(scanner.Text(), "ready at "); ok {
						ready <- endpoint
						return
					}
				}
				ready <- ""
			}()
			var endpoint string
			select {
			case endpoint = <-ready:
			case <-time.After(15 * time.Second):
				t.Fatal("timed out waiting for readiness")
			}
			u, err := url.Parse(endpoint)
			if err != nil || u.Host == "" {
				t.Fatalf("invalid readiness URL %q: %v", endpoint, err)
			}
			// Bootstrap the public CA, then verify the server using it.
			bootstrap := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}, Timeout: 5 * time.Second}
			defer bootstrap.CloseIdleConnections()
			resp, err := bootstrap.Get("https://" + u.Host + "/ca.pem")
			if err != nil {
				t.Fatal(err)
			}
			ca, err := io.ReadAll(resp.Body)
			resp.Body.Close()
			if err != nil {
				t.Fatal(err)
			}
			pool := x509.NewCertPool()
			if !pool.AppendCertsFromPEM(ca) {
				t.Fatal("invalid CA")
			}
			client := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool}}, Timeout: 5 * time.Second}
			defer client.CloseIdleConnections()
			resp, err = client.Get("https://" + u.Host + "/accounts.json")
			if err != nil {
				t.Fatal(err)
			}
			body, err := io.ReadAll(resp.Body)
			resp.Body.Close()
			if err != nil || !bytes.Equal(bytes.TrimSpace(body), []byte(`[{"name":"installer"}]`)) {
				t.Fatalf("accounts = %s, err = %v", body, err)
			}
			for _, san := range []string{"flag.example.test", "192.0.2.20", "env.example.test", "192.0.2.30"} {
				if err := resp.TLS.PeerCertificates[0].VerifyHostname(san); err != nil {
					t.Errorf("SAN %s: %v", san, err)
				}
			}
			if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() { done <- cmd.Wait() }()
			select {
			case err := <-done:
				if err != nil {
					t.Fatalf("shutdown: %v", err)
				}
			case <-time.After(10 * time.Second):
				t.Fatal("timed out waiting for shutdown")
			}
		})
	}
}
