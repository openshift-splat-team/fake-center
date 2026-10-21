// Copyright 2026 The fake-center Authors.
// SPDX-License-Identifier: Apache-2.0

package scenario

import (
	"bytes"
	"fmt"
	"os"

	"github.com/openshift-splat-team/fake-center/internal/privsets"
	"github.com/openshift-splat-team/fake-center/internal/release"
	"gopkg.in/yaml.v3"
)

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
	for _, fd := range s.FailureDomains {
		if fdNames[fd.Name] {
			return fmt.Errorf("duplicate failure domain name %q", fd.Name)
		}
		fdNames[fd.Name] = true
	}
	accountNames := make(map[string]bool, len(s.Accounts))
	for _, account := range s.Accounts {
		if accountNames[account.Name] {
			return fmt.Errorf("duplicate account name %q", account.Name)
		}
		accountNames[account.Name] = true
		for _, grant := range account.Grants {
			if _, ok := privsets.Groups(grant.Role); !ok {
				return fmt.Errorf("account %q: unknown role %q; supported role: openshift-installer", account.Name, grant.Role)
			}
			for _, name := range grant.On {
				if name != "*" && !fdNames[name] {
					return fmt.Errorf("account %q: grant references unknown failure domain %q", account.Name, name)
				}
			}
		}
	}
	return nil
}
