// Copyright 2026 The fake-center Authors.
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/openshift-splat-team/fake-center/internal/privsets"
	"github.com/openshift-splat-team/fake-center/internal/scenario"
	"github.com/vmware/govmomi/find"
	"github.com/vmware/govmomi/object"
	"github.com/vmware/govmomi/simulator"
	"github.com/vmware/govmomi/vim25"
	"github.com/vmware/govmomi/vim25/types"
)

// stagePermissions stages each privilege group as its own role on the objects
// that group is scoped to, mirroring the per-object permission layout the
// installer documents for administrators. Roles do not propagate: consumers
// check privileges on the staged objects themselves, and propagation would let
// a broader group (for example the datacenter group) mask a deliberately
// skipped group on nested objects.
func (s *Server) stagePermissions(ctx context.Context, c *vim25.Client, sc *scenario.Scenario) error {
	if err := s.stagePrivilegeList(c); err != nil {
		return err
	}
	auth := object.NewAuthorizationManager(c)
	finder := find.NewFinder(c)
	roles := make(map[string]int32)
	// Canonical privilege sets let all accounts and scopes share identical roles.
	roleFor := func(set map[string]struct{}) (int32, error) {
		ids := make([]string, 0, len(set))
		for id := range set {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		key := strings.Join(ids, "\x00")
		if id, ok := roles[key]; ok {
			return id, nil
		}
		name := fmt.Sprintf("fc-%d", len(roles)+1)
		id, err := auth.AddRole(ctx, name, ids)
		if err != nil {
			return 0, fmt.Errorf("create role %s: %w", name, err)
		}
		roles[key] = id
		return id, nil
	}

	// entities maps each failure domain's privilege groups to the objects the
	// group's role is staged on, resolved once per failure domain.
	entities := make(map[string]map[string][]types.ManagedObjectReference, len(sc.FailureDomains))
	entitiesFor := func(fd scenario.FailureDomain) (map[string][]types.ManagedObjectReference, error) {
		if e, ok := entities[fd.Name]; ok {
			return e, nil
		}
		e, err := fdEntities(ctx, finder, fd)
		if err != nil {
			return nil, err
		}
		entities[fd.Name] = e
		return e, nil
	}

	for _, account := range sc.Accounts {
		rootSet := make(map[string]struct{})
		for _, grant := range account.Grants {
			groups, ok := privsets.Groups(grant.Role)
			if !ok {
				return fmt.Errorf("account %s: unknown role %q", account.Name, grant.Role)
			}
			if !slices.Contains(grant.Without, "vcenter") {
				for id := range grantPrivileges(groups, privsets.VCenterGroups, grant) {
					rootSet[id] = struct{}{}
				}
			}
			for _, fd := range sc.FailureDomains {
				if !slices.Contains(grant.On, "*") && !slices.Contains(grant.On, fd.Name) {
					continue
				}
				fdObjects, err := entitiesFor(fd)
				if err != nil {
					return err
				}
				for _, group := range privsets.FDGroups {
					if slices.Contains(grant.Without, group) {
						continue
					}
					roleID, err := roleFor(grantPrivileges(groups, []string{group}, grant))
					if err != nil {
						return err
					}
					for _, entity := range fdObjects[group] {
						if err := auth.SetEntityPermissions(ctx, entity, []types.Permission{{
							Principal: account.Name, Group: false, RoleId: roleID, Propagate: false,
						}}); err != nil {
							return fmt.Errorf("account %s group %s on failure domain %s: %w", account.Name, group, fd.Name, err)
						}
					}
				}
			}
		}
		if len(rootSet) == 0 {
			continue
		}
		roleID, err := roleFor(rootSet)
		if err != nil {
			return err
		}
		if err := auth.SetEntityPermissions(ctx, c.ServiceContent.RootFolder, []types.Permission{{
			Principal: account.Name, Group: false, RoleId: roleID, Propagate: false,
		}}); err != nil {
			return fmt.Errorf("account %s on root folder: %w", account.Name, err)
		}
	}
	return nil
}

// stagePrivilegeList publishes the simulator's known-privilege catalog on the
// AuthorizationManager, mirroring real vCenter: clients such as the installer
// prune their required privileges against the advertised list.
func (s *Server) stagePrivilegeList(c *vim25.Client) error {
	ref := *c.ServiceContent.AuthorizationManager
	am, ok := s.model.Map().Get(ref).(*simulator.AuthorizationManager)
	if !ok {
		return fmt.Errorf("resolve AuthorizationManager %s", ref)
	}
	admin := object.AuthorizationRoleList(am.RoleList).ByName("Admin")
	am.PrivilegeList = make([]types.AuthorizationPrivilege, len(admin.Privilege))
	for i, id := range admin.Privilege {
		am.PrivilegeList[i] = types.AuthorizationPrivilege{PrivId: id}
	}
	return nil
}

// fdEntities resolves a failure domain's inventory objects per privilege group.
func fdEntities(ctx context.Context, finder *find.Finder, fd scenario.FailureDomain) (map[string][]types.ManagedObjectReference, error) {
	dc, err := finder.Datacenter(ctx, fd.Datacenter)
	if err != nil {
		return nil, fmt.Errorf("find datacenter for failure domain %s: %w", fd.Name, err)
	}
	folders, err := dc.Folders(ctx)
	if err != nil {
		return nil, fmt.Errorf("get folders for failure domain %s: %w", fd.Name, err)
	}
	vmFolder, err := finder.Folder(ctx, folders.VmFolder.InventoryPath+"/"+fd.VMFolder)
	if err != nil {
		return nil, fmt.Errorf("find VM folder for failure domain %s: %w", fd.Name, err)
	}
	clusterPath := "/" + fd.Datacenter + "/host/" + fd.Cluster
	cluster, err := finder.ClusterComputeResource(ctx, clusterPath)
	if err != nil {
		return nil, fmt.Errorf("find cluster for failure domain %s: %w", fd.Name, err)
	}
	rp, err := cluster.ResourcePool(ctx)
	if err != nil {
		return nil, fmt.Errorf("find root resource pool for failure domain %s: %w", fd.Name, err)
	}
	entities := map[string][]types.ManagedObjectReference{
		"datacenter":   {dc.Reference()},
		"cluster":      {cluster.Reference()},
		"resourcepool": {rp.Reference()},
		"folder":       {vmFolder.Reference()},
	}
	for _, name := range fd.Networks {
		network, err := finder.Network(ctx, "/"+fd.Datacenter+"/network/"+name)
		if err != nil {
			return nil, fmt.Errorf("find network %s for failure domain %s: %w", name, fd.Name, err)
		}
		entities["portgroup"] = append(entities["portgroup"], network.Reference())
	}
	for _, name := range fd.Datastores {
		datastore, err := finder.Datastore(ctx, "/"+fd.Datacenter+"/datastore/"+name)
		if err != nil {
			return nil, fmt.Errorf("find datastore %s for failure domain %s: %w", name, fd.Name, err)
		}
		entities["datastore"] = append(entities["datastore"], datastore.Reference())
	}
	return entities, nil
}

// Missing is applied last, so it also removes privileges named in Extra.
func grantPrivileges(groups map[string][]string, names []string, grant scenario.Grant) map[string]struct{} {
	set := make(map[string]struct{})
	for _, name := range names {
		for _, id := range groups[name] {
			set[id] = struct{}{}
		}
	}
	for _, id := range grant.Extra {
		set[id] = struct{}{}
	}
	for _, id := range grant.Missing {
		delete(set, id)
	}
	return set
}
