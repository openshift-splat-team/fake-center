// Copyright 2026 The fake-center Authors.
// SPDX-License-Identifier: Apache-2.0

// Package scenario defines and loads fake-center scenarios.
package scenario

type Scenario struct {
	VCenter        VCenter         `yaml:"vcenter"`
	FailureDomains []FailureDomain `yaml:"failureDomains"`
	Accounts       []Account       `yaml:"accounts"`
}

type VCenter struct {
	Version    string `yaml:"version"`    // Default "8.0.2".
	APIVersion string `yaml:"apiVersion"` // Optional override.
	Build      string `yaml:"build"`      // Optional override.
	HostBuild  string `yaml:"hostBuild"`  // Optional override.
}

type FailureDomain struct {
	Name       string   `yaml:"name"`       // Default "fd<N>".
	Datacenter string   `yaml:"datacenter"` // Default Name.
	Cluster    string   `yaml:"cluster"`    // Default Name + "-cluster".
	VMFolder   string   `yaml:"vmFolder"`   // Default Name + "-vms".
	Networks   []string `yaml:"networks"`   // Default [Name + "-network"].
	Datastores []string `yaml:"datastores"` // Default [Name + "-datastore"].
}

type Account struct {
	Name   string  `yaml:"name"`
	Grants []Grant `yaml:"grants"`
}

type Grant struct {
	Role    string   `yaml:"role"`    // v1: "openshift-installer".
	Missing []string `yaml:"missing"` // Privileges to subtract.
	Extra   []string `yaml:"extra"`   // Privileges to add.
	On      []string `yaml:"on"`      // FD names or ["*"].
}
