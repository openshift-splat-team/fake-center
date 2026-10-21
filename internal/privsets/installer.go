// Copyright 2026 The fake-center Authors.
// SPDX-License-Identifier: Apache-2.0

// Package privsets provides privilege groups used by vCenter roles.
package privsets

// VCenterGroups are the privilege groups staged on the vCenter root folder.
var VCenterGroups = []string{"vcenter"}

// FDGroups are the privilege groups staged on each failure-domain datacenter.
var FDGroups = []string{"datacenter", "cluster", "portgroup", "folder", "datastore", "resourcepool"}

var roles = map[string]map[string][]string{
	"openshift-installer": {
		"vcenter": {
			"Cns.Searchable",
			"InventoryService.Tagging.AttachTag",
			"InventoryService.Tagging.CreateCategory",
			"InventoryService.Tagging.CreateTag",
			"InventoryService.Tagging.DeleteCategory",
			"InventoryService.Tagging.DeleteTag",
			"InventoryService.Tagging.EditCategory",
			"InventoryService.Tagging.EditTag",
			"Sessions.ValidateSession",
			"StorageProfile.Update",
			"StorageProfile.View",
		},
		"cluster": {
			"Resource.AssignVMToPool",
			"VApp.AssignResourcePool",
			"VApp.Import",
			"VirtualMachine.Config.AddNewDisk",
		},
		"portgroup": {
			"Network.Assign",
		},
		"folder": {
			"Resource.AssignVMToPool",
			"VApp.Import",
			"VirtualMachine.Config.AddExistingDisk",
			"VirtualMachine.Config.AddNewDisk",
			"VirtualMachine.Config.AddRemoveDevice",
			"VirtualMachine.Config.AdvancedConfig",
			"VirtualMachine.Config.Annotation",
			"VirtualMachine.Config.CPUCount",
			"VirtualMachine.Config.DiskExtend",
			"VirtualMachine.Config.DiskLease",
			"VirtualMachine.Config.EditDevice",
			"VirtualMachine.Config.Memory",
			"VirtualMachine.Config.RemoveDisk",
			"VirtualMachine.Config.Rename",
			"VirtualMachine.Config.ResetGuestInfo",
			"VirtualMachine.Config.Resource",
			"VirtualMachine.Config.Settings",
			"VirtualMachine.Config.UpgradeVirtualHardware",
			"VirtualMachine.Interact.GuestControl",
			"VirtualMachine.Interact.PowerOff",
			"VirtualMachine.Interact.PowerOn",
			"VirtualMachine.Interact.Reset",
			"VirtualMachine.Inventory.Create",
			"VirtualMachine.Inventory.CreateFromExisting",
			"VirtualMachine.Inventory.Delete",
			"VirtualMachine.Provisioning.Clone",
			"VirtualMachine.Provisioning.MarkAsTemplate",
			"VirtualMachine.Provisioning.DeployTemplate",
			"InventoryService.Tagging.ObjectAttachable",
		},
		"datacenter": {
			"Resource.AssignVMToPool",
			"VApp.Import",
			"VirtualMachine.Config.AddExistingDisk",
			"VirtualMachine.Config.AddNewDisk",
			"VirtualMachine.Config.AddRemoveDevice",
			"VirtualMachine.Config.AdvancedConfig",
			"VirtualMachine.Config.Annotation",
			"VirtualMachine.Config.CPUCount",
			"VirtualMachine.Config.DiskExtend",
			"VirtualMachine.Config.DiskLease",
			"VirtualMachine.Config.EditDevice",
			"VirtualMachine.Config.Memory",
			"VirtualMachine.Config.RemoveDisk",
			"VirtualMachine.Config.Rename",
			"VirtualMachine.Config.ResetGuestInfo",
			"VirtualMachine.Config.Resource",
			"VirtualMachine.Config.Settings",
			"VirtualMachine.Config.UpgradeVirtualHardware",
			"VirtualMachine.Interact.GuestControl",
			"VirtualMachine.Interact.PowerOff",
			"VirtualMachine.Interact.PowerOn",
			"VirtualMachine.Interact.Reset",
			"VirtualMachine.Inventory.Create",
			"VirtualMachine.Inventory.CreateFromExisting",
			"VirtualMachine.Inventory.Delete",
			"VirtualMachine.Provisioning.Clone",
			"VirtualMachine.Provisioning.DeployTemplate",
			"VirtualMachine.Provisioning.MarkAsTemplate",
			"Folder.Create",
			"Folder.Delete",
			"InventoryService.Tagging.ObjectAttachable",
		},
		"datastore": {
			"Datastore.AllocateSpace",
			"Datastore.Browse",
			"Datastore.FileManagement",
			"InventoryService.Tagging.ObjectAttachable",
		},
		"resourcepool": {
			"Resource.AssignVMToPool",
			"VApp.AssignResourcePool",
			"VApp.Import",
			"VirtualMachine.Config.AddNewDisk",
		},
	},
}

// Groups returns the privilege groups for role, if it is known.
func Groups(role string) (map[string][]string, bool) {
	groups, ok := roles[role]
	return groups, ok
}
