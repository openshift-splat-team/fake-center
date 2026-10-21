// Copyright 2026 The fake-center Authors.
// SPDX-License-Identifier: Apache-2.0

package release

import (
	"strconv"
	"strings"
)

// APIVersions returns the inferred vCenter and ESXi API versions.
func APIVersions(version string) (server, host string, ok bool) {
	return releaseAPIVersions(version)
}

// Build returns the commonly released build number for a product version.
func Build(version string, esx bool) (string, bool) {
	return releaseBuild(version, esx)
}

// Known reports whether a release build is known for the version.
func Known(version string) bool {
	_, ok := releaseBuild(version, false)
	return ok
}

// releaseBuild returns the GA or commonly released build number for a product
// version. Patch builds can share a product version, so callers can override
// these values with -build or -host-build. The mappings are based on Broadcom
// KB 326316 (vCenter) and KB 316595 (ESXi).
func releaseBuild(version string, esx bool) (string, bool) {
	var builds map[string]string
	if esx {
		builds = map[string]string{
			"8.0.0":      "20513097",
			"8.0.1":      "21495797",
			"8.0.2":      "22380479",
			"8.0.3":      "24022510",
			"9.0.0":      "24755229",
			"9.0.0.0":    "24755229",
			"9.0.0.0100": "24813472",
			"9.0.1":      "24957456",
			"9.0.1.0":    "24957456",
			"9.0.2":      "25148076",
			"9.0.2.0":    "25148076",
			"9.0.2.0100": "25595025",
			"9.1":        "25370933",
			"9.1.0":      "25370933",
			"9.1.1.0":    "25714478",
		}
	} else {
		builds = map[string]string{
			"8.0.0":      "20519528",
			"8.0.1":      "21560480",
			"8.0.2":      "22385739",
			"8.0.3":      "24022515",
			"9.0.0":      "24755230",
			"9.0.0.0":    "24755230",
			"9.0.1":      "24957454",
			"9.0.1.0":    "24957454",
			"9.0.2":      "25148086",
			"9.0.2.0":    "25148086",
			"9.0.2.0100": "25629525",
			"9.1":        "25370922",
			"9.1.0":      "25370922",
			"9.1.1.0":    "25712839",
		}
	}
	build, ok := builds[version]
	return build, ok
}

func releaseAPIVersions(version string) (server, host string, ok bool) {
	parts := strings.Split(version, ".")
	if len(parts) < 2 {
		return "", "", false
	}

	for _, part := range parts[:min(len(parts), 3)] {
		if _, err := strconv.Atoi(part); err != nil {
			return "", "", false
		}
	}

	server = strings.Join(parts[:2], ".")
	hostParts := parts[:min(len(parts), 3)]
	if len(hostParts) < 3 {
		hostParts = append(hostParts, "0")
	}
	host = strings.Join(hostParts, ".") + ".0"
	return server, host, true
}
