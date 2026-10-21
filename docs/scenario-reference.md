# Scenario reference

A scenario is exactly one YAML document describing one vCenter. Unknown fields
are errors (strict decoding); extra YAML documents are rejected. Quote version
and build strings. Scenarios are immutable boot-time inputs: start another
instance to test a different world.

## Fields and defaults

Paths below use `[]` for a list entry. Empty scalar strings receive the same
defaults as omitted values where a default exists. Empty network/datastore
lists also receive defaults.

| Field | Type | Default / meaning |
| --- | --- | --- |
| `vcenter` | object | Optional; applies the version defaults below. |
| `vcenter.version` | string | `"8.0.2"`; product version to advertise. |
| `vcenter.apiVersion` | string | Inferred vCenter API version from `version` (`"8.0"` for the default); optional override. |
| `vcenter.build` | string | Release-mapped vCenter build (`"22385739"` for the default); optional override. |
| `vcenter.hostBuild` | string | Release-mapped ESXi build (`"22380479"` for the default); optional override. |
| `failureDomains` | list of objects | No default; at least one entry is required. |
| `failureDomains[].name` | string | `fd<N>` where N is the 1-based list position; must be unique. |
| `failureDomains[].datacenter` | string | The failure-domain name. |
| `failureDomains[].cluster` | string | `<name>-cluster`. |
| `failureDomains[].vmFolder` | string | `<name>-vms`. |
| `failureDomains[].networks` | list of strings | `[<name>-network]`; network names to stage. |
| `failureDomains[].datastores` | list of strings | `[<name>-datastore]`; datastore names to stage. |
| `accounts` | list of objects | Empty list; no scenario accounts. |
| `accounts[].name` | string | No generated default; supply a login principal name. Names must be unique. |
| `accounts[].grants` | list of objects | Empty list; no role grants. |
| `accounts[].grants[].role` | string | No default; each grant must specify `openshift-installer`, the only v1 role. |
| `accounts[].grants[].missing` | list of strings | Empty list; privilege IDs to subtract from the role. |
| `accounts[].grants[].extra` | list of strings | Empty list; privilege IDs to add to the role. |
| `accounts[].grants[].on` | list of strings | Empty list (no failure-domain targets); use failure-domain **names** or `["*"]` for all domains. |

Each account's password is `fake-center`; there is no password schema field.
The helper's `Account(name)` returns the credentials. These are test credentials,
not an access-control boundary: do not expose this service publicly.

Known version mappings live in [`internal/release/release.go`](../internal/release/release.go).
An unknown product version is accepted only when **all three** `apiVersion`,
`build`, and `hostBuild` overrides are supplied. Host API versions are inferred
from the product version (default `"8.0.2.0"`); there is no separate host API
override field.

## Pinned example: one failure-domain privilege gap

This is [`examples/scenarios/insufficient-privs.yaml`](../examples/scenarios/insufficient-privs.yaml),
used by the [worked test](../examples/installer_privileges_test.go):

```yaml
vcenter:
  version: "8.0.2"
failureDomains:
- name: fd1
- name: fd2
- name: fd3
accounts:
- name: installer
  grants:
  - role: openshift-installer
    on: [fd1, fd3]
  - role: openshift-installer
    missing: [VirtualMachine.Inventory.Create]
    on: [fd2]
```

The default topology supplies each domain's datacenter, cluster, VM folder,
network, and datastore. The installer gets its full role on `fd1` and `fd3`, but
not `VirtualMachine.Inventory.Create` on `fd2`.

## Privilege-gap pattern

Start from the supported role, subtract the precise privilege under test with
`missing`, and target only the affected domains. Keep full-role and reduced-role
targets disjoint as above; do not grant a full role on `"*"` and expect a later
grant to act as a deny rule. `missing` changes that grant's role, not every grant
for the user. If an ID appears in both `extra` and `missing`, subtraction wins.

The role is divided into vCenter-root and failure-domain privilege groups, not
an administrator role. Root grants do not propagate; domain grants propagate
through their staged inventory. `on` selects domains, not arbitrary entity
paths. See the [role definition](../internal/privsets/installer.go) for the
privilege groups. Use real vSphere privilege IDs; `extra` is for augmenting a
role, not for declaring a new named role.
