# Per-Group Staging and Installer Privilege-Test Migration — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the installer's gomock-based vSphere privilege tests with scenario-backed fake-center tests, extending fake-center with per-group privilege staging and `without:` grants.

**Architecture:** Phase 1 extends fake-center (schema field, per-group role staging on group-appropriate objects, example/docs updates, tag `v0.1.0`). Phase 2 pins the helper in the installer, adds one scenario file covering all 13 `TestPermissionValidate` cases, then deletes the gomock scaffolding while keeping `validation_test.go` on vcsim with a flat grant-all stub.

**Tech Stack:** Go 1.25/1.26, govmomi vcsim (in-process), gomock (remaining vcsim tests only), Podman for container-backed tests.

**Spec:** `docs/superpowers/specs/2026-10-23-per-group-staging-and-installer-privilege-tests-design.md` (same repo)

## Global Constraints

- Repos/branches: Phase 1 in `/home/rvanderp/code/fake-center` on branch `per-group-staging`; Phase 2 in `/home/rvanderp/code/installer/.worktrees/fake-center-testing` on branch `fake-center-testing`.
- fake-center scenario schema stays strict-decoding; unknown fields are errors.
- Grants remain disjoint per account (existing loader rule); `without` only narrows.
- `System.*` privileges cannot be gapped (existing rule).
- Root (vCenter) group stays a per-account union across grants on the root folder, `Propagate: false`; skip the root permission when the account's root set is empty.
- Helper stays stdlib-only.
- Installer commits: vendored dependency changes in their own commit, separate from functional changes; test-only commits use the `unit tests:` subsystem prefix.
- New installer tests skip unless `FAKE_CENTER_IMAGE` is set and podman/docker is on PATH.
- fake-center commit style: conventional commits (`feat:`, `docs:`, `test:`).

## Review Focus

- `pruneToAvailablePermissions` runs against fake-center's advertised privilege catalog; if vcsim's catalog lacked a required privilege ID, gap cases would fail loudly — the new test asserts each permission group is still non-empty after pruning to catch silent shrinkage.
- An account whose every grant has `without: [vcenter]` must get no root-folder permission at all (empty role skipped, not an empty-role permission).
- `extra` must add to every staged role and `missing` must subtract from every staged role (union semantics preserved per grant, now applied per group).
- Unknown `without` group names must be rejected at scenario load (strict decoding promise).
- Installer tests must SKIP (not fail) with no `FAKE_CENTER_IMAGE` / no container runtime — verified by a plain `go test` run without the env set.

---

### Task 1: `without` grant field in the scenario schema

**Files:**
- Modify: `internal/scenario/types.go`
- Modify: `internal/scenario/load.go`
- Modify: `internal/privsets/installer.go`
- Test: `internal/scenario/load_test.go`

**Interfaces:**
- Consumes: existing `Grant` struct, existing loader validation pattern.
- Produces: `Grant.Without []string` (yaml `without`); `privsets.GroupNames() []string` returning `append(VCenterGroups, FDGroups...)` — the seven names `vcenter, datacenter, cluster, portgroup, folder, datastore, resourcepool`.

- [ ] **Step 1: Write the failing tests in `internal/scenario/load_test.go`**

Two cases following the existing table style: a grant with `without: [cluster, folder]` loads with `Without` populated; a grant with `without: [nonsense]` fails with an error matching `unknown privilege group "nonsense"` and listing valid names.

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/scenario/ -run TestGrantWithout -v`
Expected: FAIL (field unknown, no validation).

- [ ] **Step 3: Implement**

Add `Without []string` with yaml tag `without` and comment `// Privilege groups not staged for this grant.` to `Grant` in `internal/scenario/types.go`. Add `GroupNames()` to `internal/privsets/installer.go`. In `internal/scenario/load.go`, validate each `Without` entry against `privsets.GroupNames()` (import `internal/privsets`), mirroring the existing unknown-role error style.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/scenario/... ./internal/privsets/...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/scenario/types.go internal/scenario/load.go internal/privsets/installer.go internal/scenario/load_test.go
git commit -m "feat: add without field to scenario grants"
```

### Task 2: Per-group privilege staging

**Files:**
- Modify: `internal/server/stage_permissions.go`
- Test: `internal/server/server_test.go`

**Interfaces:**
- Consumes: `privsets.Groups(role) map[string][]string`, `privsets.VCenterGroups`/`FDGroups`, `Grant.Without` from Task 1; staged inventory from `stage_topology.go` (paths: `/<dc>`, `/<dc>/host/<cluster>`, `/<dc>/vm/<vmFolder>`, portgroups and datastores by name under the DC, cluster root pool `Resources`).
- Produces: staging semantics later tasks rely on — each FD group staged as its own role on its group-appropriate object with `Propagate: true` (root folder unchanged, `Propagate: false`); per grant, staged set per group = group privileges + `Extra` − `Missing`; `Without` skips that group; empty root set skips the root permission.

- [ ] **Step 1: Write the failing tests in `internal/server/server_test.go`**

Update `TestPrivilegeGap`: the `VirtualMachine.Inventory.Create` (folder-group) assertions move from the datacenter object to the VM folder object (resolve via `dc.Folders(ctx).VmFolder`); root check unchanged. Rework `TestPermissionStagingSets` for the new model: for a full-role FD, `FetchUserPrivilegeOnEntities` returns exactly the group's set on each object — cluster group on `finder.ClusterComputeResource(ctx, "/<dc>/host/<cluster>")`, resourcepool group on `cluster.ResourcePool(ctx)`, portgroup group on each network, datastore group on each datastore, folder group on the VM folder, datacenter group on the DC. Keep the distinct-role-count assertion but compute the expected count from the scenario's grants and groups. Add `TestWithoutGroup`: a scenario grant `without: [cluster]` on one FD yields no permission for the account on that FD's cluster object while the resourcepool object still returns the resourcepool group; and an account whose single grant is `without: [vcenter]` gets no root-folder permission (`RetrieveEntityPermissions` on root folder returns none for that principal).

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/server/ -run 'TestPrivilegeGap|TestPermissionStagingSets|TestWithoutGroup' -v`
Expected: FAIL (staging still union-on-DC).

- [ ] **Step 3: Rework `stagePermissions` in `internal/server/stage_permissions.go`**

Keep the account loop and `roleFor` dedup. Per grant: root set as today (vcenter groups + `Extra` − `Missing`, union across the account's grants), skipped entirely when empty. For each targeted FD, resolve objects via `find.NewFinder` (`finder.Datacenter`, `dc.Folders`, `finder.ClusterComputeResource`, `finder.Network`, `finder.Datastore`, `cluster.ResourcePool`) and stage each group in `privsets.FDGroups` not listed in `grant.Without` as its own role (`group privileges + Extra − Missing`) on its object with `Propagate: true`. All server tests are in-process (`New(sc, Options{ListenAddr: "127.0.0.1:0"})`); no container needed.

- [ ] **Step 4: Run the full server package to verify nothing else broke**

Run: `go test ./internal/... ./helper/...`
Expected: PASS (helper tests skip without runtime, that is fine).

- [ ] **Step 5: Commit**

```bash
git add internal/server/stage_permissions.go internal/server/server_test.go
git commit -m "feat: stage each privilege group on its own object"
```

### Task 3: Example test and scenario reference

**Files:**
- Modify: `examples/installer_privileges_test.go`
- Modify: `docs/scenario-reference.md`

**Interfaces:**
- Consumes: Task 2 staging semantics.
- Produces: documentation of `without` and the per-group model for consumers.

- [ ] **Step 1: Update the example assertion**

In `TestInstallerPrivileges`, resolve the VM folder (`dc.Folders(ctx).VmFolder` or `finder.Folder`) instead of the datacenter as the checked entity; keep privilege `VirtualMachine.Inventory.Create` and the fd1/fd2 expectations.

- [ ] **Step 2: Update `docs/scenario-reference.md`**

Add `accounts[].grants[].without` to the field table (list of privilege group names whose staging is skipped; default empty). Replace the "Privilege-gap pattern" paragraph's union-staging description with the per-group model: each group is staged as its own role on its object (vcenter on root folder, datacenter on the datacenter, cluster on the cluster, portgroup on each network, folder on the VM folder, datastore on each datastore, resourcepool on the cluster's root resource pool); `missing` subtracts from every staged role of that grant, `extra` adds to every staged role, `without` skips a group.

- [ ] **Step 3: Run the example against a locally built image**

```bash
podman build -t fake-center:dev .
FAKE_CENTER_IMAGE=fake-center:dev go test -count=1 -v ./examples -run '^TestInstallerPrivileges$'
```

Expected: PASS for fd1 and fd2 subtests. If no container runtime is available, note it and rely on the in-process `TestPrivilegeGap` from Task 2 plus a follow-up local run before merge.

- [ ] **Step 4: Commit**

```bash
git add examples/installer_privileges_test.go docs/scenario-reference.md
git commit -m "docs: document per-group staging and without grants"
```

### Task 4: Merge and tag v0.1.0

**Files:** none (release mechanics)

- [ ] **Step 1: Push and open the PR**

```bash
git push -u origin per-group-staging
```

Open PR `per-group-staging` → `main` in openshift-splat-team/fake-center. After review/merge (human gate):

- [ ] **Step 2: Tag on main**

```bash
git checkout main && git pull
git tag -a v0.1.0 -m "scenario-backed fake vCenter with per-group privilege staging"
git push origin v0.1.0
```

Phase 2 starts only after this tag exists on the remote.

### Task 5: Vendor the fake-center helper in the installer

**Files:**
- Modify: `go.mod`, `go.sum`, `vendor/` (module `github.com/openshift-splat-team/fake-center/helper`)

**Interfaces:**
- Consumes: tag `v0.1.0` from Task 4.
- Produces: importable `fakecenter "github.com/openshift-splat-team/fake-center/helper"` with `fakecenter.Start(tb testing.TB, scenarioPath string) (*fakecenter.VCenter, error)`, `VCenter.Server string`, `VCenter.CABundle []byte`, `VCenter.Accounts []AccountRef`, `(*VCenter).Account(name string) (AccountRef, bool)`.

- [ ] **Step 1: Add and vendor the dependency**

```bash
cd /home/rvanderp/code/installer/.worktrees/fake-center-testing
go get github.com/openshift-splat-team/fake-center/helper@v0.1.0
go mod tidy && go mod vendor
```

- [ ] **Step 2: Verify the build**

Run: `go build ./...`
Expected: success.

- [ ] **Step 3: Commit (vendored dependency change, own commit)**

```bash
git add go.mod go.sum vendor/
git commit -m "dependencies: vendor fake-center helper for scenario-backed tests"
```

### Task 6: Scenario file and scenario-backed privilege tests

**Files:**
- Create: `pkg/asset/installconfig/vsphere/testdata/privileges.yaml`
- Create: `pkg/asset/installconfig/vsphere/permission_fakecenter_test.go`

**Interfaces:**
- Consumes: `fakecenter.Start` (Task 5); package-internal `validateFailureDomain(*validationContext, *vsphere.FailureDomain, bool) field.ErrorList`, `validationContext{AuthManager, Finder, Client}`, `pruneToAvailablePermissions(ctx, AuthManager)`, `permissions` map; govmomi `object.NewAuthorizationManager` (already satisfies the `AuthManager` interface per production `newAuthManager`).
- Produces: nothing consumed by later tasks.

- [ ] **Step 1: Write `testdata/privileges.yaml` exactly as specified**

```yaml
vcenter:
  version: "8.0.2"
failureDomains:
- name: fd-ok
- name: fd-portgroup
- name: fd-cluster
- name: fd-resourcepool
- name: fd-datastore
- name: fd-datacenter
- name: fd-folder
- name: fd-objectattachable
- name: fd-no-cluster
- name: fd-no-datacenter
- name: fd-no-folder
accounts:
- name: installer
  grants:
  - role: openshift-installer
    on: [fd-ok]
  - role: openshift-installer
    missing: [Network.Assign]
    on: [fd-portgroup]
  - role: openshift-installer
    missing: [VApp.AssignResourcePool]
    on: [fd-cluster]
  - role: openshift-installer
    missing: [VApp.AssignResourcePool]
    on: [fd-resourcepool]
  - role: openshift-installer
    missing: [Datastore.AllocateSpace]
    on: [fd-datastore]
  - role: openshift-installer
    missing: [Folder.Delete]
    on: [fd-datacenter]
  - role: openshift-installer
    missing: [VirtualMachine.Provisioning.DeployTemplate]
    on: [fd-folder]
  - role: openshift-installer
    missing: [InventoryService.Tagging.ObjectAttachable]
    on: [fd-objectattachable]
  - role: openshift-installer
    without: [cluster]
    on: [fd-no-cluster]
  - role: openshift-installer
    without: [datacenter]
    on: [fd-no-datacenter]
  - role: openshift-installer
    without: [folder]
    on: [fd-no-folder]
- name: no-storageprofile
  grants:
  - role: openshift-installer
    missing: [StorageProfile.View]
    on: ["*"]
```

- [ ] **Step 2: Write `permission_fakecenter_test.go`**

Package `vsphere`. Skip guard first (skip unless `FAKE_CENTER_IMAGE` set and podman/docker runnable — copy the pattern from fake-center's `examples/installer_privileges_test.go`). Connection helper `connectVCenter(t, vcenter, account) (*vim25.Client, error)` mirroring that example (CA to `t.TempDir()`, `soap.NewClient` with `/sdk` path, `SetRootCAs`, `vim25.NewClient`, `session.NewManager(...).Login` with `url.UserPassword`). Topology helper `scenarioTopology(fd string) vsphere.Topology` generating: `Datacenter: fd`, `ComputeCluster: /<fd>/host/<fd>-cluster`, `ResourcePool: /<fd>/host/<fd>-cluster/Resources`, `Folder: /<fd>/vm/<fd>-vms`, `Networks: [<fd>-network]`, `Datastore: /<fd>/datastore/<fd>-datastore`. Table `TestPermissionValidateScenarios` with one `fakecenter.Start` per test (or `TestMain`-level start; one container suffices — the scenario is one world) and these cases:

| name | account | FD | topology change | expectErr (regexp) |
|---|---|---|---|---|
| valid | installer | fd-ok | — | `` |
| missing portgroup | installer | fd-portgroup | — | `privileges missing for vSphere Port Group: Network.Assign` |
| missing vCenter | no-storageprofile | fd-ok | — | `privileges missing for vSphere vCenter: StorageProfile.View` |
| missing cluster, no resource pool | installer | fd-cluster | `ResourcePool: ""` | `privileges missing for vSphere vCenter Cluster: VApp.AssignResourcePool` |
| read-only cluster with resource pool | installer | fd-no-cluster | — | `` |
| missing datacenter, no folder | installer | fd-datacenter | `Folder: ""` | `privileges missing for vSphere vCenter Datacenter: Folder.Delete` |
| read-only datacenter with folder | installer | fd-no-datacenter | — | `` |
| missing datastore | installer | fd-datastore | — | `privileges missing for vSphere vCenter Datastore: Datastore.AllocateSpace` |
| missing resource pool | installer | fd-resourcepool | — | `privileges missing for vSphere vCenter Resource Pool: VApp.AssignResourcePool` |
| folder group absent, no folder | installer | fd-no-folder | `Folder: ""` | `` |
| folder group absent | installer | fd-no-folder | — | `privileges missing for Pre-existing Virtual Machine Folder:` |
| datacenter missing ObjectAttachable | installer | fd-objectattachable | `Folder: ""` | `privileges missing for vSphere vCenter Datacenter: [^,]*InventoryService.Tagging.ObjectAttachable` (aggregate also contains the datastore error — regexp targets the datacenter one) |
| invalid datacenter | installer | fd-ok | `Datacenter: "invalid"` | `datacenter 'invalid' not found` |

Each case builds a `validationContext{AuthManager: object.NewAuthorizationManager(client), Finder: find.NewFinder(client), Client: client}` for the case's account (the connection carries the session; `comparePrivileges` derives the username from it). Call `fakecenter.Start` once in `TestPermissionValidateScenarios` before the table loop — the scenario is one immutable world shared by all cases — then `pruneToAvailablePermissions` once per account connection. After pruning, assert every entry in the `permissions` map is still non-empty (Review Focus item 1). Keep the `vsphere.FailureDomain{Name: fd, Topology: scenarioTopology(fd)}` shape from the current tests.

- [ ] **Step 3: Run the new tests against the local image**

```bash
cd /home/rvanderp/code/fake-center && podman build -t fake-center:dev .
cd /home/rvanderp/code/installer/.worktrees/fake-center-testing
FAKE_CENTER_IMAGE=fake-center:dev go test -count=1 -v ./pkg/asset/installconfig/vsphere/ -run TestPermissionValidateScenarios
```

Expected: PASS, all 13 subtests, no high-port warning (strict 443 preferred).

- [ ] **Step 4: Verify skip behavior without the env**

Run: `go test ./pkg/asset/installconfig/vsphere/ -run TestPermissionValidateScenarios -v`
Expected: SKIP.

- [ ] **Step 5: Commit**

```bash
git add pkg/asset/installconfig/vsphere/testdata/privileges.yaml pkg/asset/installconfig/vsphere/permission_fakecenter_test.go
git commit -m "unit tests: add scenario-backed vSphere privilege validation tests"
```

### Task 7: Delete the gomock privilege scaffolding

**Files:**
- Delete: `pkg/asset/installconfig/vsphere/permission_test.go`
- Modify: `pkg/asset/installconfig/vsphere/validation_test.go`

**Interfaces:**
- Consumes: Task 6's tests are in place (old tests redundant).
- Produces: nothing.

- [ ] **Step 1: Replace `buildAuthManagerClient` usage in `validation_test.go`**

In `simulatorHelper`, replace the `buildAuthManagerClient(...)` call with a local flat stub `grantAllAuthManager(t, ctrl) mock.AuthManager`: a `mock.NewMockAuthManager(ctrl)` whose `FetchUserPrivilegeOnEntities(gomock.Any(), gomock.Any(), gomock.Any())` returns one `UserPrivilegeResult` containing every privilege in the `permissions` map, `Reference()` returns a dummy moRef, and `Properties` populates `mo.AuthorizationManager.PrivilegeList` with the same set — all `.AnyTimes()`. The helper's `sessionMgr`/`username` logic exists only to feed the old builder — remove it.

- [ ] **Step 2: Delete `permission_test.go`**

All its contents (`buildPermissionGroup`, `buildAuthManagerClient`, `validIPIMultiZoneInstallConfig`, `TestPermissionValidate`, `pushPrivileges`/`popPrivileges`/`permissionsBackup`) are superseded. Verify nothing else references them: `grep -rn "buildAuthManagerClient\|pushPrivileges\|validIPIMultiZoneInstallConfig" pkg/` returns only `permission_test.go`.

- [ ] **Step 3: Run the package and formatting**

```bash
FAKE_CENTER_IMAGE=fake-center:dev go test -count=1 ./pkg/asset/installconfig/vsphere/...
hack/go-fmt.sh .
go build ./...
```

Expected: vcsim-based tests PASS, scenario tests PASS with the env set; no fmt diffs.

- [ ] **Step 4: Commit**

```bash
git add -A pkg/asset/installconfig/vsphere/
git commit -m "unit tests: replace gomock privilege scaffolding with fake-center scenarios"
```

---

## Out of scope (follow-ups, already designed)

- `openshift/release` PR adding the `unit-fake-center` nested-podman job (fragment in fake-center `docs/ci-integration.md`) — needed before default Prow CI runs the scenario tests.
- Full-stack `Validate()` / client-creation coverage on fake-center.
- Tag categories in the scenario schema (unlocks zonal/tag test migration).
