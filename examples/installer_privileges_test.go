// Copyright 2026 The fake-center Authors.
// SPDX-License-Identifier: Apache-2.0

package examples_test

import (
	"context"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"
	"time"

	fakecenter "github.com/openshift-splat-team/fake-center/helper"
	"github.com/vmware/govmomi/find"
	"github.com/vmware/govmomi/object"
	"github.com/vmware/govmomi/session"
	"github.com/vmware/govmomi/vim25"
	"github.com/vmware/govmomi/vim25/soap"
	"github.com/vmware/govmomi/vim25/types"
)

// TestInstallerPrivileges demonstrates the installer's CA-trusting client setup
// against an isolated container and proves a failure-domain-specific privilege gap.
func TestInstallerPrivileges(t *testing.T) {
	if os.Getenv("VCENTER_TEST_PROFILES_IMAGE") == "" {
		t.Skip("set VCENTER_TEST_PROFILES_IMAGE to run container integration tests")
	}
	available := false
	for _, runtime := range []string{"podman", "docker"} {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		err := exec.CommandContext(ctx, runtime, "--version").Run()
		cancel()
		if err == nil {
			available = true
			break
		}
	}
	if !available {
		t.Skip("podman or docker required for container integration tests")
	}
	vcenter, err := fakecenter.Start(t, "scenarios/insufficient-privs.yaml")
	if err != nil {
		t.Fatal(err)
	}
	account, ok := vcenter.Account("installer")
	if !ok {
		t.Fatal("installer account missing")
	}
	caPath := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(caPath, vcenter.CABundle, 0600); err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(vcenter.Server)
	if err != nil {
		t.Fatal(err)
	}
	u.Path = "/sdk"
	soapClient := soap.NewClient(u, false)
	t.Cleanup(soapClient.CloseIdleConnections)
	if err := soapClient.SetRootCAs(caPath); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	client, err := vim25.NewClient(ctx, soapClient)
	if err != nil {
		t.Fatal(err)
	}
	sessionMgr := session.NewManager(client)
	if err := sessionMgr.Login(ctx, url.UserPassword(account.Username, account.Password)); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := sessionMgr.Logout(ctx); err != nil {
			t.Error(err)
		}
	}()
	finder := find.NewFinder(client)
	auth := object.NewAuthorizationManager(client)
	for _, tc := range []struct {
		name string
		want bool
	}{
		{name: "fd2", want: false},
		{name: "fd1", want: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dc, err := finder.Datacenter(ctx, tc.name)
			if err != nil {
				t.Fatal(err)
			}
			folders, err := dc.Folders(ctx)
			if err != nil {
				t.Fatal(err)
			}
			// VirtualMachine.Inventory.Create is a folder-group privilege, staged
			// on the failure domain's declared VM folder.
			vmFolder, err := finder.Folder(ctx, folders.VmFolder.InventoryPath+"/"+tc.name+"-vms")
			if err != nil {
				t.Fatal(err)
			}
			entity := vmFolder.Reference()
			results, err := auth.FetchUserPrivilegeOnEntities(ctx, []types.ManagedObjectReference{entity}, account.Username)
			if err != nil {
				t.Fatal(err)
			}
			if len(results) != 1 || results[0].Entity != entity {
				t.Fatalf("privilege results = %+v, want one result for %s", results, entity)
			}
			const privilege = "VirtualMachine.Inventory.Create"
			if got := slices.Contains(results[0].Privileges, privilege); got != tc.want {
				t.Errorf("%s contains %s = %t, want %t", tc.name, privilege, got, tc.want)
			}
		})
	}
}
