// Copyright 2026 The fake-center Authors.
// SPDX-License-Identifier: Apache-2.0

package scenario

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"

	"github.com/openshift-splat-team/fake-center/internal/privsets"
	"github.com/openshift-splat-team/fake-center/internal/release"
	"gopkg.in/yaml.v3"
)

// reservedAccountNames are simulator-owned principals whose built-in privileges
// would bypass the scenario's deliberately restricted roles.
var reservedAccountNames = []string{"root"}

// Load reads a scenario file, applies defaults, and validates it.
func Load(path string) (*Scenario, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return Parse(data)
}

// Parse strictly decodes a YAML scenario, applies defaults, and validates it.
func Parse(data []byte) (*Scenario, error) {
	var s Scenario
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	if err := decoder.Decode(&s); err != nil {
		return nil, fmt.Errorf("decode scenario: %w", err)
	}
	var trailing yaml.Node
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err != nil {
			return nil, fmt.Errorf("decode trailing scenario data: %w", err)
		}
		return nil, fmt.Errorf("scenario must contain exactly one YAML document")
	}
	s.applyDefaults()
	if err := s.validate(); err != nil {
		return nil, err
	}
	return &s, nil
}

func (s *Scenario) applyDefaults() {
	if s.VCenter.Version == "" {
		s.VCenter.Version = "8.0.2"
	}
	for i := range s.FailureDomains {
		fd := &s.FailureDomains[i]
		if fd.Name == "" {
			fd.Name = fmt.Sprintf("fd%d", i+1)
		}
		if fd.Datacenter == "" {
			fd.Datacenter = fd.Name
		}
		if fd.Cluster == "" {
			fd.Cluster = fd.Name + "-cluster"
		}
		if fd.VMFolder == "" {
			fd.VMFolder = fd.Name + "-vms"
		}
		if len(fd.Networks) == 0 {
			fd.Networks = []string{fd.Name + "-network"}
		}
		if len(fd.Datastores) == 0 {
			fd.Datastores = []string{fd.Name + "-datastore"}
		}
	}
}

func (s *Scenario) validate() error {
	vc := s.VCenter
	if vc.Version == "" {
		return fmt.Errorf("vcenter version must not be empty")
	}
	if !release.Known(vc.Version) && (vc.APIVersion == "" || vc.Build == "" || vc.HostBuild == "") {
		return fmt.Errorf("unknown vcenter version %q: apiVersion, build, and hostBuild overrides are required", vc.Version)
	}
	if len(s.FailureDomains) == 0 {
		return fmt.Errorf("at least one failure domain is required")
	}
	fdNames := make(map[string]bool, len(s.FailureDomains))
	datacenters := make(map[string]string, len(s.FailureDomains))
	for _, fd := range s.FailureDomains {
		if fdNames[fd.Name] {
			return fmt.Errorf("duplicate failure domain name %q", fd.Name)
		}
		fdNames[fd.Name] = true
		if previous, ok := datacenters[fd.Datacenter]; ok {
			return fmt.Errorf("failure domains %q and %q share datacenter %q: each failure domain requires a distinct datacenter in v1", previous, fd.Name, fd.Datacenter)
		}
		datacenters[fd.Datacenter] = fd.Name
	}
	accountNames := make(map[string]bool, len(s.Accounts))
	for _, account := range s.Accounts {
		if account.Name == "" {
			return fmt.Errorf("account name %q must not be empty; supply a login principal name", account.Name)
		}
		if slices.Contains(reservedAccountNames, account.Name) {
			return fmt.Errorf("account %q is reserved for the simulator's built-in administrator; choose a different account name", account.Name)
		}
		if accountNames[account.Name] {
			return fmt.Errorf("duplicate account name %q", account.Name)
		}
		accountNames[account.Name] = true
		targets := make(map[string]bool)
		for _, grant := range account.Grants {
			if _, ok := privsets.Groups(grant.Role); !ok {
				return fmt.Errorf("account %q: unknown role %q; supported role: openshift-installer", account.Name, grant.Role)
			}
			for _, group := range grant.Without {
				if !slices.Contains(privsets.GroupNames(), group) {
					return fmt.Errorf("account %q: unknown privilege group %q; valid groups: %s", account.Name, group, strings.Join(privsets.GroupNames(), ", "))
				}
			}
			for _, privilege := range grant.Missing {
				if strings.HasPrefix(privilege, "System.") {
					return fmt.Errorf("account %q: missing privilege %q cannot be gapped: the simulator's role baseline always includes System.* privileges", account.Name, privilege)
				}
			}
			grantTargets := make(map[string]bool)
			for _, name := range grant.On {
				// Repeating a target within one grant does not overlap roles.
				if grantTargets[name] {
					continue
				}
				if name != "*" && !fdNames[name] {
					return fmt.Errorf("account %q: grant references unknown failure domain %q", account.Name, name)
				}
				if targets[name] || targets["*"] || (name == "*" && len(targets) > 0) {
					return fmt.Errorf("account %q: overlapping grant target %q; use disjoint failure-domain targets and do not combine '*' with other targets", account.Name, name)
				}
				targets[name] = true
				grantTargets[name] = true
			}
		}
	}
	return nil
}
