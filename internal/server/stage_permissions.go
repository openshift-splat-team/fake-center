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
	"github.com/vmware/govmomi/vim25"
	"github.com/vmware/govmomi/vim25/types"
)

func (s *Server) stagePermissions(ctx context.Context, c *vim25.Client, sc *scenario.Scenario) error {
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
	for _, account := range sc.Accounts {
		rootSet := make(map[string]struct{})
		for _, grant := range account.Grants {
			groups, ok := privsets.Groups(grant.Role)
			if !ok {
				return fmt.Errorf("account %s: unknown role %q", account.Name, grant.Role)
			}
			for id := range grantPrivileges(groups, privsets.VCenterGroups, grant) {
				rootSet[id] = struct{}{}
			}
			fdSet := grantPrivileges(groups, privsets.FDGroups, grant)
			for _, fd := range sc.FailureDomains {
				if !slices.Contains(grant.On, "*") && !slices.Contains(grant.On, fd.Name) {
					continue
				}
				dc, err := finder.Datacenter(ctx, fd.Datacenter)
				if err != nil {
					return fmt.Errorf("find datacenter for failure domain %s: %w", fd.Name, err)
				}
				roleID, err := roleFor(fdSet)
				if err != nil {
					return err
				}
				if err := auth.SetEntityPermissions(ctx, dc.Reference(), []types.Permission{{
					Principal: account.Name, Group: false, RoleId: roleID, Propagate: true,
				}}); err != nil {
					return fmt.Errorf("account %s on failure domain %s: %w", account.Name, fd.Name, err)
				}
			}
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
