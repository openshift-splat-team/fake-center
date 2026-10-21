// Copyright 2026 The fake-center Authors.
// SPDX-License-Identifier: Apache-2.0

package privsets

import (
	"slices"
	"testing"
)

func TestInstallerGroups(t *testing.T) {
	groups, ok := Groups("openshift-installer")
	if !ok {
		t.Fatal("Groups(\"openshift-installer\") returned false")
	}

	wantGroups := []string{"vcenter", "cluster", "portgroup", "folder", "datacenter", "datastore", "resourcepool"}
	if len(groups) != len(wantGroups) {
		t.Fatalf("got %d groups, want %d", len(groups), len(wantGroups))
	}
	for _, group := range wantGroups {
		privileges, found := groups[group]
		if !found {
			t.Errorf("missing group %q", group)
			continue
		}
		if len(privileges) == 0 {
			t.Errorf("group %q is empty", group)
		}
		seen := make(map[string]bool, len(privileges))
		for _, privilege := range privileges {
			if seen[privilege] {
				t.Errorf("group %q contains duplicate privilege %q", group, privilege)
			}
			seen[privilege] = true
		}
	}

	for _, privilege := range []string{"Cns.Searchable", "Sessions.ValidateSession"} {
		if !slices.Contains(groups["vcenter"], privilege) {
			t.Errorf("vcenter is missing %q", privilege)
		}
	}
	if !slices.Contains(groups["folder"], "VirtualMachine.Inventory.Create") {
		t.Error("folder is missing VirtualMachine.Inventory.Create")
	}
	if !slices.Contains(groups["datastore"], "Datastore.AllocateSpace") {
		t.Error("datastore is missing Datastore.AllocateSpace")
	}
	if !slices.Equal(groups["portgroup"], []string{"Network.Assign"}) {
		t.Errorf("portgroup = %v, want [Network.Assign]", groups["portgroup"])
	}

	datacenter := make(map[string]bool, len(groups["datacenter"]))
	for _, privilege := range groups["datacenter"] {
		datacenter[privilege] = true
	}
	for _, privilege := range groups["folder"] {
		if !datacenter[privilege] {
			t.Errorf("datacenter is missing folder privilege %q", privilege)
		}
		delete(datacenter, privilege)
	}
	if len(datacenter) != 2 || !datacenter["Folder.Create"] || !datacenter["Folder.Delete"] {
		t.Errorf("datacenter has unexpected difference from folder: %v", datacenter)
	}

	if _, ok := Groups("nope"); ok {
		t.Error("Groups(\"nope\") returned true, want false")
	}
}
