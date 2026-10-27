// Copyright 2026 The fake-center Authors.
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"context"
	"fmt"
	"os"

	"github.com/openshift-splat-team/vcenter-test-profiles/internal/scenario"
	"github.com/vmware/govmomi/object"
	"github.com/vmware/govmomi/simulator"
	"github.com/vmware/govmomi/vim25"
	"github.com/vmware/govmomi/vim25/types"
)

func (s *Server) stageTopology(ctx context.Context, c *vim25.Client, sc *scenario.Scenario) error {
	// Keep datastore backing directories inside the model's temporary directory
	// so Model.Remove owns their cleanup, including when staging fails.
	options, err := object.NewOptionManager(c, *c.ServiceContent.Setting).Query(ctx, "vcsim.home")
	if err != nil {
		return fmt.Errorf("query simulator home: %w", err)
	}
	home := options[0].GetOptionValue().Value.(string)
	root := object.NewRootFolder(c)
	for _, fd := range sc.FailureDomains {
		if err := s.stageFailureDomain(ctx, c, root, home, fd); err != nil {
			return fmt.Errorf("failure domain %s: %w", fd.Name, err)
		}
	}
	return nil
}

func (s *Server) stageFailureDomain(ctx context.Context, c *vim25.Client, root *object.Folder, home string, fd scenario.FailureDomain) error {
	dc, err := root.CreateDatacenter(ctx, fd.Datacenter)
	if err != nil {
		return fmt.Errorf("create datacenter %s: %w", fd.Datacenter, err)
	}
	folders, err := dc.Folders(ctx)
	if err != nil {
		return fmt.Errorf("get datacenter folders: %w", err)
	}
	if _, err := folders.VmFolder.CreateFolder(ctx, fd.VMFolder); err != nil {
		return fmt.Errorf("create VM folder %s: %w", fd.VMFolder, err)
	}
	// The object layer's CreateCluster calls CreateClusterEx.
	cluster, err := folders.HostFolder.CreateCluster(ctx, fd.Cluster, types.ClusterConfigSpecEx{})
	if err != nil {
		return fmt.Errorf("create cluster %s: %w", fd.Cluster, err)
	}
	var hosts []*object.HostSystem
	var members []types.DistributedVirtualSwitchHostMemberConfigSpec
	for i := 0; i < 3; i++ {
		name := fmt.Sprintf("%s-host%d", fd.Name, i)
		task, err := cluster.AddHost(ctx, types.HostConnectSpec{HostName: name}, true, nil, nil)
		if err != nil {
			return fmt.Errorf("add host %s: %w", name, err)
		}
		info, err := task.WaitForResult(ctx, nil)
		if err != nil {
			return fmt.Errorf("wait for host %s: %w", name, err)
		}
		host := object.NewHostSystem(c, info.Result.(types.ManagedObjectReference))
		// Config is deep-copied by host initialization, but Summary's product
		// points at the process-global ESXi template. AboutInfo contains only
		// value fields, so copying it detaches the entire product from that template.
		simHost := s.model.Map().Get(host.Reference()).(*simulator.HostSystem)
		product := *simHost.Summary.Config.Product
		simHost.Summary.Config.Product = &product
		hosts = append(hosts, host)
		members = append(members, types.DistributedVirtualSwitchHostMemberConfigSpec{
			Operation: string(types.ConfigSpecOperationAdd),
			Host:      host.Reference(),
		})
	}
	name := fd.Name + "-dvs"
	task, err := folders.NetworkFolder.CreateDVS(ctx, types.DVSCreateSpec{
		ConfigSpec: &types.VMwareDVSConfigSpec{DVSConfigSpec: types.DVSConfigSpec{Name: name}},
	})
	if err != nil {
		return fmt.Errorf("create DVS %s: %w", name, err)
	}
	info, err := task.WaitForResult(ctx, nil)
	if err != nil {
		return fmt.Errorf("wait for DVS %s: %w", name, err)
	}
	dvs := object.NewDistributedVirtualSwitch(c, info.Result.(types.ManagedObjectReference))
	// Reconfigure narrows Config to DVSConfigInfo, so retain the uplink reference
	// from the VMware-specific configuration before attaching the hosts.
	simDVS := s.model.Map().Get(dvs.Reference()).(*simulator.VmwareDistributedVirtualSwitch)
	uplinks := simDVS.Config.(*types.VMwareDVSConfigInfo).UplinkPortgroup
	if len(uplinks) != 1 {
		return fmt.Errorf("DVS %s has %d uplink portgroups, want 1", name, len(uplinks))
	}
	task, err = dvs.Reconfigure(ctx, &types.DVSConfigSpec{Host: members})
	if err != nil {
		return fmt.Errorf("attach hosts to DVS %s: %w", name, err)
	}
	if err := task.Wait(ctx); err != nil {
		return fmt.Errorf("wait for DVS %s host attachment: %w", name, err)
	}
	for _, name := range fd.Networks {
		task, err := dvs.AddPortgroup(ctx, []types.DVPortgroupConfigSpec{{
			Name:     name,
			Type:     string(types.DistributedVirtualPortgroupPortgroupTypeEarlyBinding),
			NumPorts: 1,
		}})
		if err != nil {
			return fmt.Errorf("add portgroup %s: %w", name, err)
		}
		if err := task.Wait(ctx); err != nil {
			return fmt.Errorf("wait for portgroup %s: %w", name, err)
		}
	}
	// Per the topology review ruling, a realistic DVS's infrastructure uplink
	// portgroup is intentional (not scenario-declared), but its name must be
	// deterministic rather than include the simulator's generated object ID.
	task, err = object.NewDistributedVirtualPortgroup(c, uplinks[0]).Rename(ctx, name+"-DVUplinks")
	if err != nil {
		return fmt.Errorf("rename DVS %s uplink portgroup: %w", name, err)
	}
	if err := task.Wait(ctx); err != nil {
		return fmt.Errorf("wait for DVS %s uplink portgroup rename: %w", name, err)
	}
	for _, name := range fd.Datastores {
		dir, err := os.MkdirTemp(home, "datastore-")
		if err != nil {
			return fmt.Errorf("create datastore directory for %s: %w", name, err)
		}
		for _, host := range hosts {
			dss, err := host.ConfigManager().DatastoreSystem(ctx)
			if err != nil {
				return fmt.Errorf("get datastore system for %s: %w", host.Reference().Value, err)
			}
			if _, err := dss.CreateLocalDatastore(ctx, name, dir); err != nil {
				return fmt.Errorf("create datastore %s on %s: %w", name, host.Reference().Value, err)
			}
		}
	}
	return nil
}
