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
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/openshift-splat-team/fake-center/internal/privsets"
	"github.com/openshift-splat-team/fake-center/internal/scenario"
	"github.com/vmware/govmomi"
	"github.com/vmware/govmomi/find"
	"github.com/vmware/govmomi/object"
	"github.com/vmware/govmomi/session"
	"github.com/vmware/govmomi/vapi/rest"
	"github.com/vmware/govmomi/vim25"
	"github.com/vmware/govmomi/vim25/methods"
	"github.com/vmware/govmomi/vim25/mo"
	"github.com/vmware/govmomi/vim25/soap"
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

func loopbackClient(t *testing.T, s *Server) *govmomi.Client {
	t.Helper()
	u, err := url.Parse(s.URL())
	if err != nil {
		t.Fatal(err)
	}
	c, err := govmomi.NewClient(context.Background(), u, true)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Logout(context.Background()) })
	return c
}

func installerClient(t *testing.T, s *Server) *vim25.Client {
	t.Helper()
	u, err := url.Parse(s.URL())
	if err != nil {
		t.Fatal(err)
	}
	caFile := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(caFile, s.CAPEM(), 0600); err != nil {
		t.Fatal(err)
	}
	soapClient := soap.NewClient(u, false)
	t.Cleanup(soapClient.CloseIdleConnections)
	if err := soapClient.SetRootCAs(caFile); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	c, err := vim25.NewClient(ctx, soapClient)
	if err != nil {
		t.Fatal(err)
	}
	sessionMgr := session.NewManager(c)
	if err := sessionMgr.Login(ctx, url.UserPassword("installer", "fake-center")); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sessionMgr.Logout(ctx) })
	return c
}

func TestPrivilegeGap(t *testing.T) {
	s := newTestServer(t)
	c := installerClient(t, s)
	ctx := context.Background()
	finder := find.NewFinder(c)
	auth := object.NewAuthorizationManager(c)
	for _, tc := range []struct {
		name      string
		privilege string
		want      bool
	}{
		{"fd2", "VirtualMachine.Inventory.Create", false},
		{"fd1", "VirtualMachine.Inventory.Create", true},
		{"fd3", "VirtualMachine.Inventory.Create", true},
		{"root", "Cns.Searchable", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			entity := c.ServiceContent.RootFolder
			if tc.name != "root" {
				dc, err := finder.Datacenter(ctx, tc.name)
				if err != nil {
					t.Fatal(err)
				}
				folders, err := dc.Folders(ctx)
				if err != nil {
					t.Fatal(err)
				}
				vmFolder, err := finder.Folder(ctx, folders.VmFolder.InventoryPath+"/"+tc.name+"-vms")
				if err != nil {
					t.Fatal(err)
				}
				entity = vmFolder.Reference()
			}
			results, err := auth.FetchUserPrivilegeOnEntities(ctx, []types.ManagedObjectReference{entity}, "installer")
			if err != nil {
				t.Fatal(err)
			}
			if len(results) != 1 || results[0].Entity != entity {
				t.Fatalf("privilege results = %+v, want one result for %s", results, entity)
			}
			if got := slices.Contains(results[0].Privileges, tc.privilege); got != tc.want {
				t.Errorf("%s contains %s = %t, want %t", tc.name, tc.privilege, got, tc.want)
			}
		})
	}
}

func TestNoAccountsScenario(t *testing.T) {
	sc, err := scenario.Parse([]byte("failureDomains: [{}]\n"))
	if err != nil {
		t.Fatal(err)
	}
	s, err := New(sc, Options{ListenAddr: "127.0.0.1:0"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	c := loopbackClient(t, s)
	permissions, err := object.NewAuthorizationManager(c.Client).RetrieveAllPermissions(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	// Only the simulator's three built-in administrator permissions remain.
	want := []types.Permission{
		{Entity: &c.ServiceContent.RootFolder, Principal: "root", Group: true, RoleId: -1, Propagate: true},
		{Entity: &c.ServiceContent.RootFolder, Principal: "root", RoleId: -1, Propagate: true},
		{Entity: &c.ServiceContent.RootFolder, Principal: "admin", RoleId: -1, Propagate: true},
	}
	if !reflect.DeepEqual(permissions, want) {
		t.Errorf("permissions without accounts = %+v, want only simulator defaults %+v", permissions, want)
	}
}

func TestPermissionStagingSets(t *testing.T) {
	t.Run("exact group sets", func(t *testing.T) {
		sc, err := scenario.Parse([]byte(`failureDomains:
- name: fd1
accounts:
- name: installer
  grants:
  - role: openshift-installer
    on: [fd1]
`))
		if err != nil {
			t.Fatal(err)
		}
		s, err := New(sc, Options{ListenAddr: "127.0.0.1:0"})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = s.Close() })
		c := loopbackClient(t, s)
		ctx := context.Background()
		finder := find.NewFinder(c.Client)
		auth := object.NewAuthorizationManager(c.Client)
		roles, err := auth.RoleList(ctx)
		if err != nil {
			t.Fatal(err)
		}
		// The simulator appends the ReadOnly system baseline to every created role.
		baseline := object.AuthorizationRoleList(roles).ByName("ReadOnly").Privilege
		roleGroups, ok := privsets.Groups("openshift-installer")
		if !ok {
			t.Fatal("openshift-installer role missing")
		}
		dc, err := finder.Datacenter(ctx, "fd1")
		if err != nil {
			t.Fatal(err)
		}
		folders, err := dc.Folders(ctx)
		if err != nil {
			t.Fatal(err)
		}
		vmFolder, err := finder.Folder(ctx, folders.VmFolder.InventoryPath+"/fd1-vms")
		if err != nil {
			t.Fatal(err)
		}
		cluster, err := finder.ClusterComputeResource(ctx, "/fd1/host/fd1-cluster")
		if err != nil {
			t.Fatal(err)
		}
		rp, err := cluster.ResourcePool(ctx)
		if err != nil {
			t.Fatal(err)
		}
		network, err := finder.Network(ctx, "/fd1/network/fd1-network")
		if err != nil {
			t.Fatal(err)
		}
		datastore, err := finder.Datastore(ctx, "/fd1/datastore/fd1-datastore")
		if err != nil {
			t.Fatal(err)
		}
		for _, tc := range []struct {
			group  string
			entity types.ManagedObjectReference
		}{
			{"datacenter", dc.Reference()},
			{"cluster", cluster.Reference()},
			{"resourcepool", rp.Reference()},
			{"portgroup", network.Reference()},
			{"folder", vmFolder.Reference()},
			{"datastore", datastore.Reference()},
			{"vcenter", c.Client.ServiceContent.RootFolder},
		} {
			t.Run(tc.group, func(t *testing.T) {
				results, err := auth.FetchUserPrivilegeOnEntities(ctx, []types.ManagedObjectReference{tc.entity}, "installer")
				if err != nil || len(results) != 1 {
					t.Fatalf("privileges = %+v, err = %v", results, err)
				}
				got := append([]string(nil), results[0].Privileges...)
				sort.Strings(got)
				want := append(append([]string(nil), roleGroups[tc.group]...), baseline...)
				sort.Strings(want)
				if !slices.Equal(got, want) {
					t.Errorf("%s privileges = %v, want exactly the %s group plus the system baseline %v", tc.group, got, tc.group, want)
				}
			})
		}
	})

	t.Run("extra missing and role reuse", func(t *testing.T) {
		sc, err := scenario.Parse([]byte(`failureDomains:
- name: east
  datacenter: east-dc
- name: west
accounts:
- name: installer
  grants:
  - role: openshift-installer
    on: ["*"]
    extra: [Global.LogEvent, Authorization.ModifyRoles]
    missing: [Cns.Searchable, Authorization.ModifyRoles]
  - role: openshift-installer
- name: other
  grants:
  - role: openshift-installer
    on: [east, west]
    extra: [Authorization.ModifyRoles, Global.LogEvent, Global.LogEvent]
    missing: [Authorization.ModifyRoles, Cns.Searchable]
  - role: openshift-installer
`))
		if err != nil {
			t.Fatal(err)
		}
		s, err := New(sc, Options{ListenAddr: "127.0.0.1:0"})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = s.Close() })
		c := loopbackClient(t, s)
		ctx := context.Background()
		finder := find.NewFinder(c.Client)
		auth := object.NewAuthorizationManager(c.Client)
		roles, err := auth.RoleList(ctx)
		if err != nil {
			t.Fatal(err)
		}
		var stagedRoles int
		for _, role := range roles {
			if !role.System {
				stagedRoles++
			}
		}
		// Every grant in this scenario applies the same extra/missing, so the
		// expected distinct roles are the distinct per-group sets plus the root
		// set. The cluster and resourcepool groups share the same privileges
		// and therefore collapse into one role.
		roleGroups, ok := privsets.Groups("openshift-installer")
		if !ok {
			t.Fatal("openshift-installer role missing")
		}
		grant := scenario.Grant{Extra: []string{"Global.LogEvent"}, Missing: []string{"Cns.Searchable", "Authorization.ModifyRoles"}}
		distinct := map[string]bool{}
		for _, group := range privsets.FDGroups {
			set := grantPrivileges(roleGroups, []string{group}, grant)
			ids := make([]string, 0, len(set))
			for id := range set {
				ids = append(ids, id)
			}
			sort.Strings(ids)
			distinct[strings.Join(ids, "\x00")] = true
		}
		if stagedRoles != len(distinct)+1 {
			t.Errorf("created %d roles, want %d distinct privilege sets", stagedRoles, len(distinct)+1)
		}
		cluster, err := finder.ClusterComputeResource(ctx, "/east-dc/host/east-cluster")
		if err != nil {
			t.Fatal(err)
		}
		permissions, err := auth.RetrieveEntityPermissions(ctx, cluster.Reference(), false)
		if err != nil {
			t.Fatal(err)
		}
		var staged []types.Permission
		for _, permission := range permissions {
			if permission.Principal == "installer" || permission.Principal == "other" {
				staged = append(staged, permission)
			}
		}
		if len(staged) != 2 {
			t.Fatalf("cluster has %d staged permissions, want 2: %+v", len(staged), permissions)
		}
		if staged[0].RoleId != staged[1].RoleId {
			t.Errorf("identical privilege sets did not reuse a role: %+v", staged)
		}
		if staged[0].Group || staged[0].Propagate {
			t.Errorf("incorrect FD permission scope: %+v", staged[0])
		}
		role := roles.ById(staged[0].RoleId)
		if role == nil {
			t.Fatalf("missing role %d", staged[0].RoleId)
		}
		for privilege, want := range map[string]bool{
			"Resource.AssignVMToPool":   true,
			"Global.LogEvent":           true,
			"Network.Assign":            false, // portgroup privilege must not leak into the cluster role
			"Authorization.ModifyRoles": false, // missing wins over extra
		} {
			if slices.Contains(role.Privilege, privilege) != want {
				t.Errorf("cluster role contains %s = %t, want %t", privilege, !want, want)
			}
		}
		rootPermissions, err := auth.RetrieveEntityPermissions(ctx, c.Client.ServiceContent.RootFolder, false)
		if err != nil {
			t.Fatal(err)
		}
		for _, permission := range rootPermissions {
			if permission.Principal != "installer" {
				continue
			}
			if permission.Propagate {
				t.Errorf("root permission must not propagate: %+v", permission)
			}
			role := roles.ById(permission.RoleId)
			if role == nil {
				t.Fatalf("missing role %d", permission.RoleId)
			}
			if !slices.Contains(role.Privilege, "Global.LogEvent") || !slices.Contains(role.Privilege, "Cns.Searchable") || slices.Contains(role.Privilege, "Authorization.ModifyRoles") {
				// The root set is a union across grants: the plain second grant
				// re-adds Cns.Searchable that the first grant's missing removed.
				t.Errorf("root role extra/missing not applied: %v", role.Privilege)
			}
		}
	})
}

func TestWithoutGroup(t *testing.T) {
	sc, err := scenario.Parse([]byte(`failureDomains:
- name: fd1
- name: fd2
accounts:
- name: gapless
  grants:
  - role: openshift-installer
    on: [fd1]
- name: nocluster
  grants:
  - role: openshift-installer
    without: [cluster]
    on: [fd2]
- name: noroot
  grants:
  - role: openshift-installer
    without: [vcenter]
    on: [fd2]
`))
	if err != nil {
		t.Fatal(err)
	}
	s, err := New(sc, Options{ListenAddr: "127.0.0.1:0"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	c := loopbackClient(t, s)
	ctx := context.Background()
	finder := find.NewFinder(c.Client)
	auth := object.NewAuthorizationManager(c.Client)

	cluster, err := finder.ClusterComputeResource(ctx, "/fd2/host/fd2-cluster")
	if err != nil {
		t.Fatal(err)
	}
	permissions, err := auth.RetrieveEntityPermissions(ctx, cluster.Reference(), false)
	if err != nil {
		t.Fatal(err)
	}
	for _, permission := range permissions {
		if permission.Principal == "nocluster" {
			t.Errorf("without: [cluster] still staged a cluster permission: %+v", permission)
		}
	}

	rp, err := cluster.ResourcePool(ctx)
	if err != nil {
		t.Fatal(err)
	}
	roleGroups, ok := privsets.Groups("openshift-installer")
	if !ok {
		t.Fatal("openshift-installer role missing")
	}
	roles, err := auth.RoleList(ctx)
	if err != nil {
		t.Fatal(err)
	}
	baseline := object.AuthorizationRoleList(roles).ByName("ReadOnly").Privilege
	results, err := auth.FetchUserPrivilegeOnEntities(ctx, []types.ManagedObjectReference{rp.Reference()}, "nocluster")
	if err != nil || len(results) != 1 {
		t.Fatalf("resource pool privileges = %+v, err = %v", results, err)
	}
	got := append([]string(nil), results[0].Privileges...)
	sort.Strings(got)
	want := append(append([]string(nil), roleGroups["resourcepool"]...), baseline...)
	sort.Strings(want)
	if !slices.Equal(got, want) {
		t.Errorf("resource pool privileges = %v, want the resourcepool group plus the system baseline %v", got, want)
	}

	rootPermissions, err := auth.RetrieveEntityPermissions(ctx, c.Client.ServiceContent.RootFolder, false)
	if err != nil {
		t.Fatal(err)
	}
	var gapless, noroot bool
	for _, permission := range rootPermissions {
		switch permission.Principal {
		case "gapless":
			gapless = true
		case "noroot":
			noroot = true
		}
	}
	if !gapless {
		t.Error("gapless account has no root permission")
	}
	if noroot {
		t.Error("without: [vcenter] still staged a root permission")
	}
}

func TestStagedTopology(t *testing.T) {
	sc, err := scenario.Parse([]byte(`failureDomains:
- name: east
  datacenter: east-dc
  cluster: east-compute
  vmFolder: workloads
  networks: [production, storage]
  datastores: [fast, bulk]
- name: west
- name: central
`))
	if err != nil {
		t.Fatal(err)
	}
	s, err := New(sc, Options{ListenAddr: "127.0.0.1:0"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	c := loopbackClient(t, s)
	ctx := context.Background()
	finder := find.NewFinder(c.Client)
	dcs, err := finder.DatacenterList(ctx, "*")
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, dc := range dcs {
		names = append(names, dc.Name())
	}
	sort.Strings(names)
	if want := []string{"central", "east-dc", "west"}; !reflect.DeepEqual(names, want) {
		t.Fatalf("datacenters = %v, want %v", names, want)
	}
	for _, fd := range sc.FailureDomains {
		t.Run(fd.Name, func(t *testing.T) {
			dc, err := finder.Datacenter(ctx, fd.Datacenter)
			if err != nil {
				t.Fatal(err)
			}
			f := find.NewFinder(c.Client).SetDatacenter(dc)
			cluster, err := f.ClusterComputeResource(ctx, fd.Cluster)
			if err != nil {
				t.Fatal(err)
			}
			folders, err := dc.Folders(ctx)
			if err != nil {
				t.Fatal(err)
			}
			var cm mo.ClusterComputeResource
			if err := cluster.Properties(ctx, cluster.Reference(), []string{"parent"}, &cm); err != nil {
				t.Fatal(err)
			}
			if cm.Parent == nil || *cm.Parent != folders.HostFolder.Reference() {
				t.Error("cluster is not in the DC host folder")
			}
			hosts, err := cluster.Hosts(ctx)
			if err != nil {
				t.Fatal(err)
			}
			var hostNames []string
			for _, host := range hosts {
				hostNames = append(hostNames, host.Name())
			}
			sort.Strings(hostNames)
			wantHosts := []string{fd.Name + "-host0", fd.Name + "-host1", fd.Name + "-host2"}
			if !reflect.DeepEqual(hostNames, wantHosts) {
				t.Errorf("hosts = %v, want %v", hostNames, wantHosts)
			}
			if _, err := f.Folder(ctx, folders.VmFolder.InventoryPath+"/"+fd.VMFolder); err != nil {
				t.Errorf("VM folder: %v", err)
			}
			networks, err := f.NetworkList(ctx, "*")
			var switches []*object.DistributedVirtualSwitch
			var portgroupNames []string
			for _, network := range networks {
				switch network := network.(type) {
				case *object.DistributedVirtualSwitch:
					switches = append(switches, network)
				case *object.DistributedVirtualPortgroup:
					portgroupNames = append(portgroupNames, network.Name())
				}
			}
			if err != nil || len(switches) != 1 || switches[0].Name() != fd.Name+"-dvs" {
				t.Fatalf("DVS = %v, err = %v", switches, err)
			}
			var dvs mo.DistributedVirtualSwitch
			if err := switches[0].Properties(ctx, switches[0].Reference(), []string{"portgroup", "config.host"}, &dvs); err != nil {
				t.Fatal(err)
			}
			if len(dvs.Config.GetDVSConfigInfo().Host) != 3 {
				t.Errorf("DVS has %d hosts, want 3", len(dvs.Config.GetDVSConfigInfo().Host))
			}
			wantPortgroups := append([]string{fd.Name + "-dvs-DVUplinks"}, fd.Networks...)
			sort.Strings(portgroupNames)
			sort.Strings(wantPortgroups)
			if !reflect.DeepEqual(portgroupNames, wantPortgroups) {
				t.Errorf("portgroups = %v, want %v", portgroupNames, wantPortgroups)
			}
			for _, name := range fd.Datastores {
				ds, err := f.Datastore(ctx, name)
				if err != nil {
					t.Fatal(err)
				}
				var datastore mo.Datastore
				if err := ds.Properties(ctx, ds.Reference(), []string{"host"}, &datastore); err != nil {
					t.Fatal(err)
				}
				if len(datastore.Host) != 3 {
					t.Errorf("datastore %s mounted on %d hosts, want 3", name, len(datastore.Host))
				}
			}
		})
	}
}

func TestStagedHostVersionsAreInstanceLocal(t *testing.T) {
	ctx := context.Background()
	var clients []*govmomi.Client
	for _, version := range []string{"8.0.2", "9.0.0"} {
		sc, err := scenario.Parse([]byte(fmt.Sprintf("vcenter:\n  version: %q\nfailureDomains: [{}]\n", version)))
		if err != nil {
			t.Fatal(err)
		}
		s, err := New(sc, Options{ListenAddr: "127.0.0.1:0"})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = s.Close() })
		clients = append(clients, loopbackClient(t, s))
	}
	// Both instances are live: constructing B must not mutate A's host products.
	for i, c := range clients {
		version := []string{"8.0.2", "9.0.0"}[i]
		f := find.NewFinder(c.Client)
		dc, err := f.DefaultDatacenter(ctx)
		if err != nil {
			t.Fatal(err)
		}
		hosts, err := f.SetDatacenter(dc).HostSystemList(ctx, "*")
		if err != nil {
			t.Fatal(err)
		}
		if len(hosts) != 3 {
			t.Fatalf("instance %d has %d hosts, want 3", i, len(hosts))
		}
		for _, host := range hosts {
			var h mo.HostSystem
			if err := host.Properties(ctx, host.Reference(), []string{"config.product", "summary.config.product"}, &h); err != nil {
				t.Fatal(err)
			}
			if h.Config.Product.Version != version || h.Summary.Config.Product == nil || h.Summary.Config.Product.Version != version {
				t.Errorf("instance %d host %s: config = %+v, summary = %+v, want %s", i, host.Name(), h.Config.Product, h.Summary.Config.Product, version)
			}
		}
	}
}

func TestPrivilegeList(t *testing.T) {
	s := newTestServer(t)
	c := loopbackClient(t, s)
	ctx := context.Background()
	var authManagerMo mo.AuthorizationManager
	err := object.NewAuthorizationManager(c.Client).Properties(ctx, *c.Client.ServiceContent.AuthorizationManager, []string{"privilegeList"}, &authManagerMo)
	if err != nil {
		t.Fatal(err)
	}
	available := make(map[string]bool, len(authManagerMo.PrivilegeList))
	for _, privilege := range authManagerMo.PrivilegeList {
		available[privilege.PrivId] = true
	}
	if len(available) == 0 {
		t.Fatal("AuthorizationManager advertises an empty privilege list")
	}
	roleGroups, ok := privsets.Groups("openshift-installer")
	if !ok {
		t.Fatal("openshift-installer role missing")
	}
	for group, privileges := range roleGroups {
		for _, id := range privileges {
			if !available[id] {
				t.Errorf("privilege %s in group %s missing from the advertised catalog", id, group)
			}
		}
	}
}
