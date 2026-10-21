// Copyright 2026 The fake-center Authors.
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"context"
	"crypto/ecdsa"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"io"
	"net"
	"net/http"
	"net/url"
	"reflect"
	"testing"
	"time"

	"github.com/openshift-splat-team/fake-center/internal/scenario"
	"github.com/vmware/govmomi"
	"github.com/vmware/govmomi/find"
	"github.com/vmware/govmomi/object"
	"github.com/vmware/govmomi/vapi/rest"
	"github.com/vmware/govmomi/vim25/methods"
	"github.com/vmware/govmomi/vim25/mo"
	"github.com/vmware/govmomi/vim25/types"
)

const testScenario = `vcenter:
  version: "8.0.2"
failureDomains:
- name: fd1
- name: fd2
- name: fd3
accounts:
- name: installer
  grants:
  - role: openshift-installer
    on: [fd1, fd3]
  - role: openshift-installer
    missing: [VirtualMachine.Inventory.Create]
    on: [fd2]
`

func newTestServer(t *testing.T, extraSANs ...string) *Server {
	t.Helper()
	sc, err := scenario.Parse([]byte(testScenario))
	if err != nil {
		t.Fatal(err)
	}
	s, err := New(sc, Options{ListenAddr: "127.0.0.1:0", ExtraSANs: extraSANs})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Error(err)
		}
	})
	return s
}

func trustedTLS(t *testing.T, s *Server) *tls.Config {
	t.Helper()
	block, _ := pem.Decode(s.CAPEM())
	if block == nil || block.Type != "CERTIFICATE" {
		t.Fatal("CAPEM is not a PEM certificate")
	}
	ca, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	if !ca.IsCA || ca.PublicKeyAlgorithm != x509.ECDSA {
		t.Fatal("expected an ECDSA CA")
	}
	if key := ca.PublicKey.(*ecdsa.PublicKey); key.Curve.Params().Name != "P-256" {
		t.Errorf("CA curve = %s, want P-256", key.Curve.Params().Name)
	}
	pool := x509.NewCertPool()
	pool.AddCert(ca)
	return &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
}

func TestNewServesTLSWithCAMaterial(t *testing.T) {
	s := newTestServer(t, "vc.example.test", "192.0.2.10")
	u, err := url.Parse(s.URL())
	if err != nil {
		t.Fatal(err)
	}
	if u.Scheme != "https" || u.User == nil || u.Path != "/sdk" {
		t.Fatalf("unexpected simulator URL %s", u)
	}
	conn, err := tls.Dial("tcp", u.Host, trustedTLS(t, s))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	cert := conn.ConnectionState().PeerCertificates[0]
	for _, name := range []string{"127.0.0.1", "::1", "localhost", "vc.example.test", "192.0.2.10"} {
		if err := cert.VerifyHostname(name); err != nil {
			t.Errorf("SAN %s: %v", name, err)
		}
	}
	other := newTestServer(t)
	if reflect.DeepEqual(s.CAPEM(), other.CAPEM()) {
		t.Fatal("servers reused the same CA")
	}
}

func TestHealthAndAccountsEndpoints(t *testing.T) {
	s := newTestServer(t)
	u, _ := url.Parse(s.URL())
	client := &http.Client{Transport: &http.Transport{TLSClientConfig: trustedTLS(t, s)}, Timeout: 10 * time.Second}
	defer client.CloseIdleConnections()
	for _, path := range []string{"/healthz", "/ca.pem", "/accounts.json"} {
		resp, err := client.Get("https://" + u.Host + path)
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode != http.StatusOK {
			t.Errorf("%s: status = %d", path, resp.StatusCode)
		}
		switch path {
		case "/ca.pem":
			if string(body) != string(s.CAPEM()) {
				t.Error("CA endpoint differs from CAPEM")
			}
		case "/accounts.json":
			var accounts []map[string]string
			if err := json.Unmarshal(body, &accounts); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(accounts, []map[string]string{{"name": "installer"}}) {
				t.Errorf("accounts = %s", body)
			}
		}
	}
	s.ready.Store(false)
	resp, err := client.Get("https://" + u.Host + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("not ready: status = %d, want 503", resp.StatusCode)
	}
}

func TestNewListenFailure(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	sc, err := scenario.Parse([]byte(testScenario))
	if err != nil {
		t.Fatal(err)
	}
	if s, err := New(sc, Options{ListenAddr: listener.Addr().String()}); err == nil {
		s.Close()
		t.Fatal("New succeeded on an occupied port")
	}
}

func TestVersionOverrides(t *testing.T) {
	sc, err := scenario.Parse([]byte(`vcenter:
  version: "8.0.2"
  apiVersion: "8.0.99"
  build: "123"
  hostBuild: "456"
failureDomains: [{}]
`))
	if err != nil {
		t.Fatal(err)
	}
	s, err := New(sc, Options{ListenAddr: "127.0.0.1:0"})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	u, _ := url.Parse(s.URL())
	c, err := govmomi.NewClient(context.Background(), u, true)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Logout(context.Background())
	a := c.ServiceContent.About
	if a.Version != "8.0.2" || a.ApiVersion != "8.0.99" || a.Build != "123" {
		t.Errorf("overridden server version = %+v", a)
	}
	if a.FullName != "VMware vCenter Server 8.0.2 build-123 (govmomi simulator)" {
		t.Errorf("FullName = %q", a.FullName)
	}
	p := createVersionTestHost(t, c)
	if p.Build != "456" || p.ApiVersion != "8.0.2.0" || p.FullName != "VMware ESXi 8.0.2 build-456" {
		t.Errorf("overridden host version = %+v", p)
	}
	client := &http.Client{Transport: &http.Transport{TLSClientConfig: trustedTLS(t, s)}}
	defer client.CloseIdleConnections()
	resp, err := client.Get("https://" + u.Host + "/accounts.json")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil || string(body) != "[]\n" {
		t.Errorf("empty accounts = %q, err = %v", body, err)
	}
}

func TestVersionMasquerade(t *testing.T) {
	s := newTestServer(t)
	u, _ := url.Parse(s.URL())
	ctx := context.Background()
	c, err := govmomi.NewClient(ctx, u, true)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Logout(ctx)
	restClient := rest.NewClient(c.Client)
	if err := restClient.Login(ctx, u.User); err != nil {
		t.Fatalf("vAPI endpoint login: %v", err)
	}
	defer restClient.Logout(ctx)
	content, err := methods.GetServiceContent(ctx, c.Client)
	if err != nil {
		t.Fatal(err)
	}
	if a := content.About; a.Version != "8.0.2" || a.ApiVersion != "8.0" || a.Build != "22385739" {
		t.Fatalf("unexpected server version: %+v", a)
	}
	p := createVersionTestHost(t, c)
	if p.Version != "8.0.2" || p.ApiVersion != "8.0.2.0" || p.Build != "22380479" || p.LicenseProductVersion != "8.0.2" {
		t.Errorf("unexpected host version: %+v", p)
	}
}

func createVersionTestHost(t *testing.T, c *govmomi.Client) types.AboutInfo {
	t.Helper()
	ctx := context.Background()
	// Task 4 starts with no inventory. Create a host via SOAP to exercise
	// the masqueraded host template without relying on topology staging.
	dc, err := object.NewFolder(c.Client, c.ServiceContent.RootFolder).CreateDatacenter(ctx, "version-test")
	if err != nil {
		t.Fatal(err)
	}
	folders, err := dc.Folders(ctx)
	if err != nil {
		t.Fatal(err)
	}
	task, err := folders.HostFolder.AddStandaloneHost(ctx, types.HostConnectSpec{HostName: "version-test-host"}, true, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := task.Wait(ctx); err != nil {
		t.Fatal(err)
	}
	host, err := find.NewFinder(c.Client).SetDatacenter(dc).HostSystem(ctx, "version-test-host")
	if err != nil {
		t.Fatal(err)
	}
	var h mo.HostSystem
	if err := host.Properties(ctx, host.Reference(), []string{"config.product"}, &h); err != nil {
		t.Fatal(err)
	}
	return h.Config.Product
}
