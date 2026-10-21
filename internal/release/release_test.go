// Copyright 2026 The fake-center Authors.
// SPDX-License-Identifier: Apache-2.0

package release

import "testing"

func TestAPIVersions(t *testing.T) {
	tests := []struct {
		version string
		server  string
		host    string
		ok      bool
	}{
		{version: "9.0.0", server: "9.0", host: "9.0.0.0", ok: true},
		{version: "8.0.2", server: "8.0", host: "8.0.2.0", ok: true},
		{version: "9.0.0.0100", server: "9.0", host: "9.0.0.0", ok: true},
		{version: "invalid", ok: false},
	}

	for _, test := range tests {
		t.Run(test.version, func(t *testing.T) {
			server, host, ok := APIVersions(test.version)
			if server != test.server || host != test.host || ok != test.ok {
				t.Fatalf("APIVersions(%q) = (%q, %q, %t), want (%q, %q, %t)", test.version, server, host, ok, test.server, test.host, test.ok)
			}
		})
	}
}

func TestBuild(t *testing.T) {
	tests := []struct {
		version string
		esx     bool
		build   string
		ok      bool
	}{
		{version: "9.0.0", build: "24755230", ok: true},
		{version: "9.0.0", esx: true, build: "24755229", ok: true},
		{version: "9.0.2.0", esx: true, build: "25148076", ok: true},
		{version: "99.0.0", ok: false},
	}

	for _, test := range tests {
		t.Run(test.version, func(t *testing.T) {
			build, ok := Build(test.version, test.esx)
			if build != test.build || ok != test.ok {
				t.Fatalf("Build(%q, %t) = (%q, %t), want (%q, %t)", test.version, test.esx, build, ok, test.build, test.ok)
			}
		})
	}
}

func TestKnown(t *testing.T) {
	tests := []struct {
		version string
		want    bool
	}{
		{version: "8.0.3", want: true},
		{version: "7.0.0", want: false},
	}

	for _, test := range tests {
		t.Run(test.version, func(t *testing.T) {
			if got := Known(test.version); got != test.want {
				t.Fatalf("Known(%q) = %t, want %t", test.version, got, test.want)
			}
		})
	}
}
