// Copyright 2026 The fake-center Authors.
// SPDX-License-Identifier: Apache-2.0

package scenario

import (
	"fmt"
	"os"
	"path/filepath"
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

func TestLoadDocumentBoundary(t *testing.T) {
	tests := []struct {
		name    string
		data    string
		wantErr bool
	}{
		{name: "single document", data: "failureDomains: [{}]\n"},
		{name: "trailing comments", data: "failureDomains: [{}]\n# end of scenario\n\n"},
		{name: "unknown key in second document", data: "failureDomains: [{}]\n---\nunexpected: true\n", wantErr: true},
		{name: "valid second document", data: "failureDomains: [{}]\n---\nfailureDomains: [{}]\n", wantErr: true},
		{name: "empty second document", data: "failureDomains: [{}]\n---\n", wantErr: true},
		{name: "trailing malformed YAML", data: "failureDomains: [{}]\n---\naccounts: [\n", wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "scenario.yaml")
			if err := os.WriteFile(path, []byte(test.data), 0600); err != nil {
				t.Fatal(err)
			}
			s, err := Load(path)
			if (err != nil) != test.wantErr {
				t.Fatalf("Load error = %v, want error %t", err, test.wantErr)
			}
			if test.wantErr {
				if s != nil {
					t.Fatalf("Load returned scenario on error: %+v", s)
				}
			} else if len(s.FailureDomains) != 1 || s.FailureDomains[0].Name != "fd1" {
				t.Fatalf("unexpected loaded scenario: %+v", s)
			}
		})
	}
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

func TestValidateGrantTargets(t *testing.T) {
	tests := []struct {
		name   string
		grants string
		want   []string
	}{
		{name: "disjoint proof point", grants: "[{role: openshift-installer, on: [fd1, fd3]}, {role: openshift-installer, on: [fd2]}]"},
		{name: "single wildcard", grants: "[{role: openshift-installer, on: ['*']}]"},
		{name: "repeated domain within one grant", grants: "[{role: openshift-installer, on: [fd1, fd1]}]"},
		{name: "wildcard then explicit", grants: "[{role: openshift-installer, on: ['*']}, {role: openshift-installer, on: [fd1]}]", want: []string{"installer", "fd1", "overlapping"}},
		{name: "explicit then wildcard", grants: "[{role: openshift-installer, on: [fd1]}, {role: openshift-installer, on: ['*']}]", want: []string{"installer", "*", "overlapping"}},
		{name: "duplicate domain", grants: "[{role: openshift-installer, on: [fd2]}, {role: openshift-installer, on: [fd2]}]", want: []string{"installer", "fd2", "overlapping"}},
		{name: "duplicate wildcard", grants: "[{role: openshift-installer, on: ['*']}, {role: openshift-installer, on: ['*']}]", want: []string{"installer", "*", "overlapping"}},
		{name: "wildcard mixed with explicit", grants: "[{role: openshift-installer, on: ['*', fd1]}]", want: []string{"installer", "fd1", "overlapping"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := Parse([]byte("failureDomains: [{name: fd1}, {name: fd2}, {name: fd3}]\naccounts: [{name: installer, grants: " + test.grants + "}]"))
			assertValidationError(t, err, test.want)
		})
	}
	t.Run("different accounts may share targets", func(t *testing.T) {
		_, err := Parse([]byte("failureDomains: [{}]\naccounts: [{name: a, grants: [{role: openshift-installer, on: [fd1]}]}, {name: b, grants: [{role: openshift-installer, on: [fd1]}]}]"))
		assertValidationError(t, err, nil)
	})
}

func TestValidateAccountNames(t *testing.T) {
	for _, test := range []struct {
		name string
		want []string
	}{
		{name: "root", want: []string{`account "root"`, "reserved", "choose"}},
		{name: "", want: []string{`account name ""`, "must not be empty"}},
		{name: "installer"},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := Parse([]byte(fmt.Sprintf("failureDomains: [{}]\naccounts: [{name: %q}]", test.name)))
			assertValidationError(t, err, test.want)
		})
	}
}

func TestValidateSystemPrivilegeGaps(t *testing.T) {
	for _, test := range []struct {
		name  string
		field string
		id    string
		want  []string
	}{
		{name: "baseline read", field: "missing", id: "System.Read", want: []string{"installer", "System.Read", "simulator's role baseline always includes System.* privileges", "cannot be gapped"}},
		{name: "baseline prefix", field: "missing", id: "System.Custom", want: []string{"System.Custom", "cannot be gapped"}},
		{name: "inventory gap", field: "missing", id: "VirtualMachine.Inventory.Create"},
		{name: "baseline extra allowed", field: "extra", id: "System.Read"},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := Parse([]byte(fmt.Sprintf("failureDomains: [{}]\naccounts: [{name: installer, grants: [{role: openshift-installer, on: [fd1], %s: [%s]}]}]", test.field, test.id)))
			assertValidationError(t, err, test.want)
		})
	}
}

func TestValidateDistinctDatacenters(t *testing.T) {
	for _, test := range []struct {
		name string
		fds  string
		want []string
	}{
		{name: "distinct", fds: "[{name: fd1, datacenter: dc1}, {name: fd2, datacenter: dc2}]"},
		{name: "shared", fds: "[{name: fd1, datacenter: dc}, {name: fd2, datacenter: dc}]", want: []string{"dc", "fd1", "fd2", "each failure domain requires a distinct datacenter in v1"}},
		{name: "default collision", fds: "[{name: fd1, datacenter: fd2}, {name: fd2}]", want: []string{"fd2", "each failure domain requires a distinct datacenter in v1"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := Parse([]byte("failureDomains: " + test.fds))
			assertValidationError(t, err, test.want)
		})
	}
}

func assertValidationError(t *testing.T, err error, want []string) {
	t.Helper()
	if len(want) == 0 {
		if err != nil {
			t.Fatal(err)
		}
		return
	}
	for _, fragment := range want {
		if err == nil || !strings.Contains(err.Error(), fragment) {
			t.Fatalf("validation error = %v, want error containing %q", err, fragment)
		}
	}
}
