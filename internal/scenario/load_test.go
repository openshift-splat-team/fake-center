// Copyright 2026 The fake-center Authors.
// SPDX-License-Identifier: Apache-2.0

package scenario

import (
	"os"
	"reflect"
	"strings"
	"testing"
)

func TestLoadDefaults(t *testing.T) {
	s, err := Load("testdata/insufficient-privs.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if len(s.FailureDomains) != 3 || len(s.Accounts) != 1 {
		t.Fatalf("unexpected scenario: %+v", s)
	}
	wantFD := FailureDomain{
		Name: "fd2", Datacenter: "fd2", Cluster: "fd2-cluster", VMFolder: "fd2-vms",
		Networks: []string{"fd2-network"}, Datastores: []string{"fd2-datastore"},
	}
	if got := s.FailureDomains[1]; !reflect.DeepEqual(got, wantFD) {
		t.Errorf("fd2 = %+v, want %+v", got, wantFD)
	}
	wantGrants := []Grant{
		{Role: "openshift-installer", On: []string{"fd1", "fd3"}},
		{Role: "openshift-installer", Missing: []string{"VirtualMachine.Inventory.Create"}, On: []string{"fd2"}},
	}
	if got := s.Accounts[0]; got.Name != "installer" || !reflect.DeepEqual(got.Grants, wantGrants) {
		t.Errorf("account = %+v, want installer with grants %+v", got, wantGrants)
	}

	t.Run("omitted version and names", func(t *testing.T) {
		s, err := Parse([]byte("failureDomains: [{}, {}]\n"))
		if err != nil {
			t.Fatal(err)
		}
		if s.VCenter.Version != "8.0.2" {
			t.Errorf("Version = %q, want 8.0.2", s.VCenter.Version)
		}
		if len(s.FailureDomains) != 2 {
			t.Fatalf("got %d failure domains, want 2", len(s.FailureDomains))
		}
		if s.FailureDomains[0].Name != "fd1" || !reflect.DeepEqual(s.FailureDomains[1], wantFD) {
			t.Errorf("default failure domains = %+v", s.FailureDomains)
		}
	})

	t.Run("explicit values preserved", func(t *testing.T) {
		s, err := Parse([]byte(`vcenter:
  version: "9.0.0"
  apiVersion: "9.0"
  build: "123"
  hostBuild: "456"
failureDomains:
- name: west
  datacenter: dc
  cluster: compute
  vmFolder: guests
  networks: [net-a, net-b]
  datastores: [store-a, store-b]
accounts:
- name: installer
  grants:
  - role: openshift-installer
    extra: [Custom.Privilege]
    on: ['*']
`))
		if err != nil {
			t.Fatal(err)
		}
		wantVC := VCenter{Version: "9.0.0", APIVersion: "9.0", Build: "123", HostBuild: "456"}
		if s.VCenter != wantVC {
			t.Errorf("VCenter = %+v, want %+v", s.VCenter, wantVC)
		}
		want := FailureDomain{
			Name: "west", Datacenter: "dc", Cluster: "compute", VMFolder: "guests",
			Networks: []string{"net-a", "net-b"}, Datastores: []string{"store-a", "store-b"},
		}
		if !reflect.DeepEqual(s.FailureDomains, []FailureDomain{want}) {
			t.Errorf("FailureDomains = %+v, want %+v", s.FailureDomains, want)
		}
		if got := s.Accounts[0].Grants[0].Extra; !reflect.DeepEqual(got, []string{"Custom.Privilege"}) {
			t.Errorf("Extra = %v, want [Custom.Privilege]", got)
		}
	})
}

func TestLoadErrors(t *testing.T) {
	tests := []struct {
		file string
		want string
	}{
		{file: "unknown-fd.yaml", want: "fd9"},
		{file: "unknown-role.yaml", want: "openshift-installer"},
		{file: "unknown-version.yaml", want: "7.0.0"},
		{file: "bad-field.yaml", want: "field typo not found"},
	}
	for _, test := range tests {
		t.Run(test.file, func(t *testing.T) {
			_, err := Load("testdata/" + test.file)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Load error = %v, want error containing %q", err, test.want)
			}
		})
	}

	t.Run("unknown version with overrides", func(t *testing.T) {
		data, err := os.ReadFile("testdata/unknown-version.yaml")
		if err != nil {
			t.Fatal(err)
		}
		for _, test := range []struct {
			overrides string
			wantOK    bool
		}{
			{},
			{overrides: "  apiVersion: '7.0'\n  build: '123'\n"},
			{overrides: "  apiVersion: '7.0'\n  hostBuild: '456'\n"},
			{overrides: "  build: '123'\n  hostBuild: '456'\n"},
			{overrides: "  apiVersion: '7.0'\n  build: '123'\n  hostBuild: '456'\n", wantOK: true},
		} {
			yaml := strings.Replace(string(data), "failureDomains:", test.overrides+"failureDomains:", 1)
			_, err := Parse([]byte(yaml))
			if (err == nil) != test.wantOK {
				t.Errorf("overrides %q: error = %v, want success %t", test.overrides, err, test.wantOK)
			}
		}
	})

	t.Run("missing file", func(t *testing.T) {
		if _, err := Load("testdata/does-not-exist.yaml"); !os.IsNotExist(err) {
			t.Fatalf("Load error = %v, want missing file", err)
		}
	})
}

func TestValidate(t *testing.T) {
	tests := []struct {
		name    string
		data    string
		want    string
		wantErr bool
	}{
		{name: "duplicate failure domains", data: "failureDomains: [{name: fd1}, {name: fd1}]", want: "fd1"},
		{name: "default name collision", data: "failureDomains: [{name: fd2}, {}]", want: "fd2"},
		{name: "duplicate accounts", data: "failureDomains: [{}]\naccounts: [{name: installer}, {name: installer}]", want: "installer"},
		{name: "wildcard", data: "failureDomains: [{}]\naccounts: [{name: installer, grants: [{role: openshift-installer, on: ['*']}]}]"},
		{name: "no failure domains", data: "failureDomains: []", want: "failure domain"},
		{name: "empty document", data: "", wantErr: true},
		{name: "zero accounts", data: "failureDomains: [{}]\naccounts: []"},
		{name: "unknown field at root", data: "failureDomains: [{}]\nunexpected: true", want: "field unexpected not found"},
		{name: "unknown grant field", data: "failureDomains: [{}]\naccounts: [{name: installer, grants: [{role: openshift-installer, typo: true}]}]", want: "field typo not found"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := Parse([]byte(test.data))
			wantErr := test.want != "" || test.wantErr
			if wantErr {
				if err == nil || !strings.Contains(err.Error(), test.want) {
					t.Fatalf("Parse error = %v, want error containing %q", err, test.want)
				}
			} else if err != nil {
				t.Fatal(err)
			}
		})
	}
}
