# Per-group privilege staging and installer privilege-test migration

Date: 2026-10-23
Status: approved approach, pending implementation
Repos: openshift-splat-team/fake-center (this repo), openshift/installer

## Intent and why

The installer's vSphere privilege-validation tests
(`pkg/asset/installconfig/vsphere/permission_test.go`) are hard to maintain:
vcsim provides inventory but not per-user privileges, so the tests hand-build a
gomock `AuthManager` (~120 lines of scaffolding with test-only knobs), mutate
the production `permissions` map around every case (`pushPrivileges`/
`popPrivileges`), and couple to vcsim's fixed inventory layout. fake-center
exists to replace exactly this pattern, but its v1 staging model — one union
role per grant, propagated from the datacenter — cannot express per-object
privilege gaps the installer's tests (and the installer's documented permission
layout) rely on.

Two changes: (1) fake-center stages each privilege group as its own role on
the appropriate object, with a `without:` grant field to drop groups entirely;
(2) the installer migrates all `TestPermissionValidate` cases onto one
scenario file and deletes the gomock apparatus.

## Goals

- fake-center: stage each FD privilege group as its own role on its
  group-appropriate object (cluster group on the cluster, portgroup on each
  network, folder on the VM folder, datastore on each datastore, resourcepool
  on the cluster's root resource pool, datacenter on the datacenter). This
  matches how the installer's docs tell admins to lay out permissions.
- fake-center: `accounts[].grants[].without` — list of privilege group names
  whose staging is skipped for that grant. Expresses "read-only cluster",
  "read-only datacenter", and "folder group absent" scenarios.
- fake-center: existing scenarios keep working unchanged (backward compatible;
  default is all groups staged).
- installer: one `testdata/privileges.yaml` scenario covering all
  `TestPermissionValidate` cases; tests exercise the real SOAP privilege fetch
  (`FetchUserPrivilegeOnEntities`) instead of a mock of it.
- installer: delete `buildAuthManagerClient`, `buildPermissionGroup`, the
  override/removal knobs, and `pushPrivileges`/`popPrivileges`.
- installer: `validation_test.go` keeps its inventory/tag cases on vcsim but
  swaps the gomock builder for a flat "grant everything" stub (~20 lines).
- Tag fake-center `v0.1.0` after the extension lands; installer pins
  `helper@v0.1.0` (vendored, separate commit per installer convention).

## Non-goals

- Tag categories (`openshift-region`/`openshift-zone`) in scenarios — separate
  future extension.
- Migrating version-gate, conversion, or destroy tests (vcsim/gomog fine).
- Full-stack `Validate()` / client-creation coverage — the next PR after this
  one, building on the same wiring.
- `openshift/release` CI wiring (nested-podman job) — parallel PR using the
  fragment in `docs/ci-integration.md`; documented here only.
- Changing installer production code, including the
  `pruneToAvailablePermissions` global-mutation smell.

## Anti-goals

- Keeping both old and new privilege tests after migration.
- Reintroducing gomock AuthManager mocks for privilege-gap simulation.

## Constraints

- Scenario schema stays strict-decoding; unknown fields are errors.
- Grants remain disjoint per account; `without` only narrows a grant.
- `System.*` privileges remain un-gappable (existing rule).
- Root (vCenter) group staging stays a per-account union across grants on the
  root folder, `Propagate: false`; `without: [vcenter]` on every grant of an
  account skips the root permission entirely.
- Helper stays stdlib-only, go 1.25 (installer is go 1.26).
- New installer tests skip unless `FAKE_CENTER_IMAGE` is set and a container
  runtime is present (same pattern as `examples/installer_privileges_test.go`).
- With per-group staging, the existing example test's assertion moves from the
  datacenter object to the VM folder object (folder-group privileges now live
  on the folder).

## Approaches considered

- **Chosen — per-group staging + `without:` grants, then migrate all cases.**
  Small, backward-compatible schema addition that is also a fidelity
  improvement; makes every existing test case expressible with precise
  single-error assertions.
- *Rejected — migrate now onto v1 union staging.* Two cases ("read-only
  cluster with resource pool provided", "read-only datacenter with folder
  provided") are inexpressible — cluster and resourcepool groups share the
  same four privilege IDs, distinguished only by grant object — and several
  more degrade to regexp-matching one error inside an aggregate.
- *Rejected — scenario-per-test-case.* N container startups, N near-duplicate
  YAML files, no advantage over one shared world.
- *Rejected — full-stack through exported `Validate()`.* Drags DNS/version/LB
  checks into the path; next PR instead.
- *Rejected — staging permissions into in-process vcsim inside installer
  tests.* Rebuilds fake-center in the wrong repo; no shared scenarios across
  components.

## The how, as decided

### fake-center changes

1. `internal/scenario/types.go`: `Grant.Without []string` (privilege group
   names). `internal/scenario/load.go`: validate entries against the known
   group names (`vcenter`, `datacenter`, `cluster`, `portgroup`, `folder`,
   `datastore`, `resourcepool`).
2. `internal/server/stage_permissions.go`: replace the single `fdSet` role on
   the datacenter with per-group roles on group-appropriate objects. `missing`
   still subtracts privilege IDs from every group the grant stages (global to
   the grant); `without` skips a group's staging for that grant. The root
   folder permission is skipped when the account's root set is empty.
3. `internal/privsets`: unchanged group definitions; groups now double as
   staging targets.
4. `examples/installer_privileges_test.go`: assert the folder-group privilege
   on the VM folder object instead of the datacenter.
5. `docs/scenario-reference.md`: document `without` and the per-group staging
   model.
6. Server tests: privilege present on the right object and absent on others;
   `without` semantics; an existing scenario (insufficient-privs.yaml)
   unchanged still produces the fd2 gap.

### installer changes (second PR, after fake-center lands and is tagged)

1. `go get github.com/openshift-splat-team/fake-center/helper@v0.1.0`,
   `go mod tidy`, `go mod vendor` — own commit.
2. `pkg/asset/installconfig/vsphere/testdata/privileges.yaml`: one scenario,
   ~9 failure domains and two accounts:
   - `fd-ok` — full role (the valid case; doubles as a drift guard: if
     fake-center's role ever loses a required privilege, this case fails).
   - `fd-portgroup` — `missing: [Network.Assign]` (unique to portgroup).
   - `fd-cluster` — `missing: [VApp.AssignResourcePool]` (in cluster and
     resourcepool groups only; tested with `resourcePool: ""` so only the
     cluster check fires).
   - `fd-resourcepool` — `missing: [VApp.AssignResourcePool]`, tested with
     the resource pool set (cluster tolerated read-only when a resource pool
     is provided).
   - `fd-datastore` — `missing: [Datastore.AllocateSpace]` (unique to
     datastore).
   - `fd-datacenter` — `missing: [Folder.Delete]` (unique to datacenter),
     tested with `folder: ""` so the datacenter check fires.
   - `fd-folder` — `missing: [VirtualMachine.Provisioning.DeployTemplate]`
     (folder and datacenter groups), tested with the folder set.
   - `fd-no-cluster` — `without: [cluster]` (read-only cluster case, resource
     pool provided, expects no error).
   - `fd-no-datacenter` — `without: [datacenter]` (read-only datacenter case,
     folder provided, expects no error).
   - `fd-no-folder` — `without: [folder]`, tested twice: folder set (folder
     error expected) and folder empty (no error — datacenter role intact).
   - Account `installer`: one grant per failure domain carrying its gap.
   - Account `no-storageprofile`: single `on: ["*"]` grant with
     `missing: [StorageProfile.View]` (vCenter-root group; needs its own
     account because the root set is a union across grants).
3. Rewrite `permission_test.go`: connection helper mirroring
   `examples/installer_privileges_test.go` (fakecenter.Start, CA to temp file,
   `soap.NewClient` + `SetRootCAs`, `vim25.NewClient`, session login as the
   case's account), real `AuthManager` via `object.NewAuthorizationManager`,
   real finder, table of cases `(account, failure domain, topology mutations,
   expectErr)`. Topology paths generated from the FD name:
   `/fdX/host/fdX-cluster`, `/fdX/host/fdX-cluster/Resources`,
   `/fdX/vm/fdX-vms`, network `fdX-network`, `/fdX/datastore/fdX-datastore`.
   `pruneToAvailablePermissions` runs once against the real auth manager.
   Case mapping (current → new) preserves all 13 cases; the "invalid
   datacenter" inventory case stays as a plain table row; the
   `InventoryService.Tagging.ObjectAttachable` datacenter case keeps its
   semantics but asserts via regexp on the aggregate (the privilege spans
   folder/datacenter/datastore groups, so the datastore check fires too).
4. Trim `validation_test.go`'s `simulatorHelper` to use a flat grant-all
   gomock stub for the privilege path.

## What's left to the builder

- Exact scenario YAML contents and final table wording; verify at
  implementation time that each "unique privilege" claim above still holds
  against `permissions.go` (checked 2026-10-23: `VApp.AssignResourcePool`
  only in cluster/resourcepool; `Network.Assign` only portgroup;
  `Datastore.AllocateSpace`/`Browse`/`FileManagement` only datastore;
  `Folder.Delete` only datacenter; `StorageProfile.View` only vcenter).
- Whether vcsim's `CreateCluster` root resource pool is reachable at
  `/fdX/host/fdX-cluster/Resources` (vcsim docs say every cluster has a root
  pool named `Resources`; confirm in the staged world).
- Fake-center release mechanics: tag `v0.1.0` on main after merge; installer
  PR pins it.
- openshift/release PR for the nested-podman unit job (fragment exists); not
  blocking the installer PR but should be rehearsed before the old tests'
  CI coverage is considered fully replaced.
