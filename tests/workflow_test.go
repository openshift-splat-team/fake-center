// Copyright 2026 The fake-center Authors.
// SPDX-License-Identifier: Apache-2.0

package tests

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestCIImagePolicy(t *testing.T) {
	data, err := os.ReadFile("../.github/workflows/ci.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var workflow struct {
		Jobs map[string]struct {
			Env   map[string]string `yaml:"env"`
			Steps []struct {
				Name string `yaml:"name"`
				Run  string `yaml:"run"`
			} `yaml:"steps"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(data, &workflow); err != nil {
		t.Fatal(err)
	}
	t.Run("integration requires strict 443", func(t *testing.T) {
		if got := workflow.Jobs["integration"].Env["VCENTER_TEST_PROFILES_STRICT"]; got != "1" {
			t.Fatalf("integration VCENTER_TEST_PROFILES_STRICT = %q, want 1", got)
		}
	})
	var script string
	for _, step := range workflow.Jobs["publish"].Steps {
		if step.Name == "Build and push image" {
			script = step.Run
		}
	}
	if script == "" {
		t.Fatal("image publish script not found")
	}
	for _, tc := range []struct {
		name    string
		refType string
		ref     string
		refName string
		want    []string
	}{
		{name: "main publishes latest", refType: "branch", ref: "refs/heads/main", refName: "main", want: []string{"sha-0123456789ab", "latest"}},
		{name: "release does not overwrite latest", refType: "tag", ref: "refs/tags/v1.2.3", refName: "v1.2.3", want: []string{"sha-0123456789ab", "v1.2.3"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			// Execute the actual workflow script, replacing only docker's side effects.
			if err := os.WriteFile(filepath.Join(dir, "docker"), []byte("#!/bin/sh\nif [ \"$1\" = push ]; then echo \"${2#example/fake-center:}\"; fi\n"), 0700); err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command("bash", "-c", script)
			cmd.Env = append(os.Environ(), "PATH="+dir+":"+os.Getenv("PATH"), "IMAGE=example/fake-center", "GITHUB_SHA=0123456789abcdef0123456789abcdef01234567", "GITHUB_REF_TYPE="+tc.refType, "GITHUB_REF="+tc.ref, "GITHUB_REF_NAME="+tc.refName)
			output, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("publish script: %v\n%s", err, output)
			}
			if got := strings.Fields(string(output)); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("pushed tags = %v, want %v", got, tc.want)
			}
		})
	}
}
