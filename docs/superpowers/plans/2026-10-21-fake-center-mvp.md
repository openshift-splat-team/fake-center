# fake-center MVP Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build the fake-center harness: a containerized fake vCenter staged from immutable scenario YAML, and a stdlib-only Go helper that starts it from unit tests.

**Architecture:** A server binary in this repo links the forked govmomi simulator (`replace` → `openshift-splat-team/govmomi`), stages a scenario's failure domains and account permissions in-process, and serves the vSphere API on 443 behind a per-instance CA. A nested helper module launches the container via the podman/docker CLI (publishing 443 on a distinct 127.0.0.x loopback address per instance) and returns URL + accounts + CA bundle to the test.

**Tech Stack:** Go 1.25, two modules in one repo (server + stdlib-only helper), govmomi simulator (fork), podman/docker CLI, Docker/OCI image on Quay, GitHub Actions.

**Spec:** `docs/superpowers/specs/2026-10-21-fake-center-design.md` — the plan argues from the spec; executors read both.

## Global Constraints

- Go 1.25. Root module `github.com/openshift-splat-team/fake-center`; nested helper module `github.com/openshift-splat-team/fake-center/helper`.
- Helper module is **stdlib-only**: zero `require` entries in `helper/go.mod`.
- govmomi is consumed via `replace github.com/vmware/govmomi => github.com/openshift-splat-team/govmomi@<pseudo-version of dc14af853455>` (the fork keeps the upstream module path). Document the bump procedure in README.
- Apache-2.0 `LICENSE` and file headers matching the fork's style (`// SPDX-License-Identifier: Apache-2.0`).
- Image is `quay.io/ocp-splat/fake-center`, tags `sha-<12>`/`latest`/`v*`, mirroring the fork's `vcsim-image.yaml` workflow.
- The server answers on **443** in-container. Server cert SANs always include `127.0.0.1`, `::1`, `localhost`, plus extras from `-san`/`FAKE_CENTER_SANS`. A fresh CA is generated per instance.
- No testcontainers dependency; containers are launched by exec'ing the podman/docker CLI.
- Instances are immutable: no post-boot mutation API in v1.
- Non-goals from the spec: no VM workload fidelity, no performance testing, no multi-vCenter, no adopt mode in v1.

## Review Focus

Input classes the spec implies but tasks above don't fully exercise — each pinned to its owning task:

1. Grant `on:` referencing an undeclared failure domain must fail at scenario load with the name in the error (Task 3, `TestLoadErrors`).
2. Unknown role name must fail at load listing valid roles (Task 3, `TestLoadErrors`).
3. A `vcenter.version` absent from the release tables must fail at load unless `apiVersion`, `build`, and `hostBuild` are all set explicitly (Task 3, `TestLoadErrors`).
4. No container runtime on the machine must produce an actionable error telling the user to install podman or docker (Task 7, `TestStartNoRuntime`).
5. Container startup failure/timeout must surface the container's log tail in the error, and parallel `Start` calls must get distinct loopback IPs (Task 7, `TestStartTimeoutIncludesLogs`, `TestLoopbackAllocation`).

---

### Task 1: Release tables (`internal/release`)

Version/build inference ported from the fork's `vcsim/main.go` (unexported there, so copy the logic; it lives in `~/code/govmomi/vcsim/main.go`, functions `releaseBuild` and `releaseAPIVersions`, mapping tables 8.0.0–9.1.1 sourced from Broadcom KB 326316/316595).

**Files:**
- Create: `go.mod` (root module, with the govmomi `replace`), `LICENSE` (Apache-2.0), `.gitignore`, `internal/release/release.go`, `internal/release/release_test.go`

**Interfaces:**
- Consumes: nothing (first task; also creates the module scaffolding).
- Produces: `release.APIVersions(version string) (server, host string, ok bool)`, `release.Build(version string, esx bool) (string, bool)`, `release.Known(version string) bool` — used by Task 3 (validation) and Task 4 (masquerade).

- [ ] **Step 1: Initialize the module**

```bash
go mod init github.com/openshift-splat-team/fake-center
go mod edit -replace github.com/vmware/govmomi=github.com/openshift-splat-team/govmomi@dc14af853455
```
Add `.gitignore` (Go defaults) and the Apache-2.0 `LICENSE`.

- [ ] **Step 2: Write the failing test**

`internal/release/release_test.go`, mirroring the fork's `vcsim/main_test.go` cases:

```go
func TestAPIVersions(t *testing.T) // "9.0.0" → ("9.0","9.0.0.0",true); "8.0.2" → ("8.0","8.0.2.0",true); "9.0.0.0100" → ("9.0","9.0.0.0",true); "invalid" → ok=false
func TestBuild(t *testing.T)      // ("9.0.0", false) → "24755230"; ("9.0.0", true) → "24755229"; ("9.0.2.0", true) → "25148076"; ("99.0.0", _) → ok=false
func TestKnown(t *testing.T)      // "8.0.3" true; "7.0.0" false
```

- [ ] **Step 3: Run test to verify it fails**

Run: `go test ./internal/release/`
Expected: FAIL (package undefined)

- [ ] **Step 4: Implement `internal/release/release.go`**

Copy `releaseBuild`/`releaseAPIVersions` from the fork's `vcsim/main.go` (both build maps and the version-splitting logic) behind the three exported wrappers. No behavior changes.

- [ ] **Step 5: Run test to verify it passes**

Run: `go test ./internal/release/`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add go.mod LICENSE .gitignore internal/release/
git commit -m "feat: add vCenter release version/build inference tables"
```

---

### Task 2: Installer privilege sets (`internal/privsets`)

**Files:**
- Create: `internal/privsets/installer.go`, `internal/privsets/installer_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces: `privsets.Groups(role string) (map[string][]string, bool)` plus `privsets.VCenterGroups` and `privsets.FDGroups` (string slices) — used by Task 3 (role validation) and Task 6 (permission staging).

- [ ] **Step 1: Write the failing test**

```go
func TestInstallerGroups(t *testing.T)
```
Asserts, for `role = "openshift-installer"`:
- seven groups present: `vcenter`, `cluster`, `portgroup`, `folder`, `datacenter`, `datastore`, `resourcepool`
- sentinels: `vcenter` contains `Cns.Searchable` and `Sessions.ValidateSession`; `folder` contains `VirtualMachine.Inventory.Create`; `datastore` contains `Datastore.AllocateSpace`; `portgroup` equals `["Network.Assign"]`
- structural invariants: `datacenter` = `folder` ∪ {`Folder.Create`, `Folder.Delete`}; every list non-empty and deduplicated
- `Groups("nope")` returns ok=false

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/privsets/`
Expected: FAIL

- [ ] **Step 3: Implement `internal/privsets/installer.go`**

Copy the seven privilege lists **verbatim** from the installer's `pkg/asset/installconfig/vsphere/permissions.go` (`permissions` map, groups `permissionVcenter`…`permissionResourcePool`; local checkout `~/code/installer`, or upstream `github.com/openshift/installer`). One package-level map `roles = map[string]map[string][]string{"openshift-installer": {...}}`. Do not retype from memory — copy from the source file.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/privsets/`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/privsets/
git commit -m "feat: add openshift-installer privilege set from installer validation matrix"
```

---

### Task 3: Scenario schema (`internal/scenario`)

**Files:**
- Create: `internal/scenario/types.go`, `internal/scenario/load.go`, `internal/scenario/load_test.go`, `internal/scenario/testdata/{insufficient-privs.yaml,unknown-fd.yaml,unknown-role.yaml,unknown-version.yaml,bad-field.yaml}`

**Interfaces:**
- Consumes: `release.Known` (Task 1), `privsets.Groups` (Task 2).
- Produces: the scenario types and loaders every later task uses:

```go
type Scenario struct {
    VCenter        VCenter         `yaml:"vcenter"`
    FailureDomains []FailureDomain `yaml:"failureDomains"`
    Accounts       []Account       `yaml:"accounts"`
}
type VCenter struct {
    Version    string `yaml:"version"`    // default "8.0.2"
    APIVersion string `yaml:"apiVersion"` // optional override
    Build      string `yaml:"build"`      // optional override
    HostBuild  string `yaml:"hostBuild"`  // optional override
}
type FailureDomain struct {
    Name       string   `yaml:"name"`       // default "fd<N>"
    Datacenter string   `yaml:"datacenter"` // default Name
    Cluster    string   `yaml:"cluster"`    // default Name + "-cluster"
    VMFolder   string   `yaml:"vmFolder"`   // default Name + "-vms"
    Networks   []string `yaml:"networks"`   // default [Name + "-network"]
    Datastores []string `yaml:"datastores"` // default [Name + "-datastore"]
}
type Account struct {
    Name   string  `yaml:"name"`
    Grants []Grant `yaml:"grants"`
}
type Grant struct {
    Role    string   `yaml:"role"`    // v1: "openshift-installer"
    Missing []string `yaml:"missing"` // privileges to subtract
    Extra   []string `yaml:"extra"`   // privileges to add
    On      []string `yaml:"on"`      // FD names or ["*"]
}
func Load(path string) (*Scenario, error)
func Parse(data []byte) (*Scenario, error)
```

- [ ] **Step 1: Write the failing tests**

`testdata/insufficient-privs.yaml` (pinned; this is the proof-point scenario):

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

Tests:
- `TestLoadDefaults` — load the file above; assert fd2 defaults to `Datacenter "fd2"`, `Cluster "fd2-cluster"`, `VMFolder "fd2-vms"`, `Networks ["fd2-network"]`, `Datastores ["fd2-datastore"]`; `VCenter.Version == "8.0.2"` when omitted; grant `On` stays as written.
- `TestLoadErrors` — table test over `unknown-fd.yaml` (grant `on: [fd9]` → error contains `fd9`), `unknown-role.yaml` (`role: admin` → error contains `openshift-installer`), `unknown-version.yaml` (`version: "7.0.0"` with no overrides → error; same file plus all three overrides set → passes), `bad-field.yaml` (unknown YAML key → strict-decode error).
- `TestValidate` — duplicate FD names and duplicate account names rejected; `*` is valid in `On`; scenario with zero failure domains rejected; zero accounts allowed.

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/scenario/`
Expected: FAIL

- [ ] **Step 3: Implement `types.go` and `load.go`**

`Parse`: strict YAML decode (`yaml.Decoder` with `KnownFields(true)`, `gopkg.in/yaml.v3`), then apply defaults, then validate. Validation: FD/account name uniqueness; every `On` entry is a declared FD name or `*`; `Role` known via `privsets.Groups`; `release.Known(VCenter.Version)` or all three overrides set; `Version` non-empty. `Load` reads the file and calls `Parse`.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/scenario/`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/scenario/
git commit -m "feat: add scenario schema with defaults and validation"
```

---

### Task 4: Server core + binary (`internal/server`, `cmd/fake-center`)

**Files:**
- Create: `internal/server/server.go`, `internal/server/server_test.go`, `cmd/fake-center/main.go`

**Interfaces:**
- Consumes: `scenario.Scenario` (Task 3), `release` (Task 1), `simulator.VPX/Model.Create/NewServer` from the fork.
- Produces:

```go
type Options struct {
    ListenAddr string   // default "0.0.0.0:443"; tests use "127.0.0.1:0"
    ExtraSANs  []string // additional cert SANs
}
type Server struct{ /* simulator server, CA PEM, ready flag */ }
func New(sc *scenario.Scenario, opts Options) (*Server, error)
func (s *Server) URL() string  // simulator's URL (scheme://host:port/sdk, user = DefaultLogin)
func (s *Server) CAPEM() []byte
func (s *Server) Close() error
```
Tasks 5 and 6 add unexported staging methods on `*Server`; this task defines the struct and calls `s.stageTopology(...)` / `s.stagePermissions(...)` (added next tasks — until then, `New` calls them if non-nil via small indirection, or Tasks 5–6 wire them in; simplest: declare the methods in this task as no-op stubs that Tasks 5 and 6 replace).

- [ ] **Step 1: Write the failing tests**

`server_test.go`, using `Options{ListenAddr: "127.0.0.1:0"}` and `scenario.Parse` of the Task 3 YAML:
- `TestNewServesTLSWithCAMaterial` — `New` returns; `CAPEM()` parses as PEM; a TLS dial to the server's host with that CA in the pool verifies (no hostname error against `127.0.0.1`).
- `TestHealthAndAccountsEndpoints` — `GET /healthz` → 200 after `New` returns; `GET /ca.pem` → body equals `CAPEM()`; `GET /accounts.json` → `[{"name":"installer"}]`.
- `TestVersionMasquerade` — govmomi client against `s.URL()` (Login with the URL's embedded `user:pass`), `RetrieveServiceContent`: `About.Version == "8.0.2"`, `About.ApiVersion` == inferred `release.APIVersions("8.0.2")` server value, `About.Build` == `release.Build("8.0.2", false)`; one host's `Config.Product.Version` matches the host-side inference.

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/server/`
Expected: FAIL

- [ ] **Step 3: Implement `internal/server/server.go`**

`New` flow (mirror the fork's `vcsim/main.go` and the installer's `mock/vsphere_sim.go` patterns):
1. `model := simulator.VPX()`; zero all inventory counts (`Datacenter, Cluster, ClusterHost, Portgroup, PortgroupNSX, OpaqueNetwork, Host, Datastore, Machine, Pool, App, Pod, Folder = 0`), `Autostart = false`.
2. Masquerade: set `model.ServiceContent.About.{Version,ApiVersion,Build,FullName}` and `esx.HostConfigInfo.Product.{Version,ApiVersion,Build,LicenseProductVersion,FullName}` from the scenario, inferring from `internal/release` exactly as the fork's `-version` handling does.
3. `model.Create()`; `model.Service.RegisterEndpoints = true`.
4. Generate a P-256 CA and a server cert signed by it with SANs `127.0.0.1, ::1, localhost` + `opts.ExtraSANs`; set `model.Service.TLS = &tls.Config{Certificates: ...}` and `model.Service.Listen = &url.URL{Host: opts.ListenAddr}`.
5. Register on `model.Service.ServeMux`: `/healthz` (503 until ready, then 200), `/ca.pem`, `/accounts.json` (account names from the scenario, `encoding/json`).
6. `srv := model.Service.NewServer()`; build a govmomi client against `srv.URL`; call `stageTopology` then `stagePermissions` (stubs in this task); set ready; return.

`cmd/fake-center/main.go`: flags `-scenario PATH`, `-listen` (default `0.0.0.0:443`), `-san` (repeatable). Env `FAKE_CENTER_SCENARIO` (YAML content, takes precedence over `-scenario` — used by the helper) and `FAKE_CENTER_SANS` (comma-separated, merged with `-san`). Load scenario, run server, block on signal, close.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/server/ ./cmd/...`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/server/ cmd/
git commit -m "feat: add fake-center server core with TLS CA and health endpoints"
```

---

### Task 5: Topology staging (`internal/server`)

**Files:**
- Create: `internal/server/stage_topology.go`
- Modify: `internal/server/server.go` (replace the stub), `internal/server/server_test.go` (add tests)

**Interfaces:**
- Consumes: `*Server` (Task 4), `scenario.Scenario`, govmomi object layer (`find`, `object`).
- Produces: `func (s *Server) stageTopology(ctx context.Context, c *vim25.Client, sc *scenario.Scenario) error` — called by `New`; replaces the Task 4 stub.

- [ ] **Step 1: Write the failing tests**

`TestStagedTopology` (extend `server_test.go`, `Options{ListenAddr: "127.0.0.1:0"}`): after `New`, via a `find.Finder` on a loopback client:
- datacenters are exactly the FDs' `Datacenter` names
- each DC's host folder contains a cluster named `fd.Cluster` with 3 hosts
- each DC's VM folder contains folder `fd.VMFolder`
- each DC's network folder contains one port group per `fd.Networks` entry
- each DC exposes a datastore findable by each `fd.Datastores` name

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/server/ -run TestStagedTopology`
Expected: FAIL

- [ ] **Step 3: Implement `stage_topology.go`**

Per FD, drive the object layer against the in-process client (mirror `Model.CreateInfrastructure` in `~/code/govmomi/simulator/model.go` for the exact call sequence): `CreateDatacenter(Datacenter)`; in the DC's VM folder `CreateFolder(VMFolder)`; in the host folder `CreateClusterEx(Cluster)` then `AddHost` ×3 (names `<fd.Name>-host0..2`); in the network folder create a DVS `<fd.Name>-dvs` and `AddPortgroup` per network name; per datastore name call `CreateLocalDatastore` on each host of the FD (same name per host; assertion contract is "findable by name in the DC", mirroring how the installer's validation locates datastores).

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/server/`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/server/
git commit -m "feat: stage named failure-domain topology from scenario"
```

---

### Task 6: Permission staging — the proof point (`internal/server`)

**Files:**
- Create: `internal/server/stage_permissions.go`
- Modify: `internal/server/server.go` (replace the stub), `internal/server/server_test.go`

**Interfaces:**
- Consumes: `*Server`, `scenario.Scenario`, `privsets` (Task 2), govmomi `object.AuthorizationManager` (`AddRole`, `SetEntityPermissions`, `FetchUserPrivilegeOnEntities`).
- Produces: `func (s *Server) stagePermissions(ctx context.Context, c *vim25.Client, sc *scenario.Scenario) error`.

- [ ] **Step 1: Write the failing test**

`TestPrivilegeGap` — the harness-level proof point. `New` with the Task 3 scenario; build a client **logged in as `installer`** (wire CA and login exactly like the installer's `mock/vsphere_sim.go` `GetClient`, but `sessionMgr.Login(ctx, url.UserPassword("installer", "fake-center"))`); then via `object.AuthorizationManager.FetchUserPrivilegeOnEntities`:
- on fd2's datacenter: privileges do **not** contain `VirtualMachine.Inventory.Create`
- on fd1's datacenter: privileges **do** contain it
- on the root folder: privileges contain `Cns.Searchable` (vCenter-group placement)
Also `TestNoAccountsScenario`: scenario with zero accounts stages without error (no permissions placed).

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/server/ -run TestPrivilegeGap`
Expected: FAIL

- [ ] **Step 3: Implement `stage_permissions.go`**

Staging rules (pin these semantics):
- Per grant, compute two sets: `vcenterSet = privsets group "vcenter" + Extra − Missing`; `fdSet = union(privsets.FDGroups' lists) + Extra − Missing`.
- Per **account**, place one root-folder permission: role = union of `vcenterSet` across the account's grants; `Propagate: false`.
- Per (grant, FD) where `On` is `*` (all FDs) or contains the FD name: place a permission on that FD's **datacenter** with `fdSet`, `Propagate: true`.
- Create roles with `AddRole` (name `fc-<n>`, deduplicated by identical privilege sets); apply with `SetEntityPermissions` using `types.Permission{Principal: account name, Group: false, RoleId, Propagate}`. Ancestor+propagate resolution is already implemented by the fork's AuthorizationManager.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/server/`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/server/
git commit -m "feat: stage account permissions with deliberate privilege gaps"
```

---

### Task 7: Helper module (unit level) (`helper/`)

**Files:**
- Create: `helper/go.mod` (module `github.com/openshift-splat-team/fake-center/helper`, zero requires), `helper/fakecenter.go`, `helper/container.go`, `helper/fakecenter_test.go`, `helper/testdata/minimal.yaml`
- Modify: root `go.mod` (add `require` + `replace` for the helper module → `./helper`)

**Interfaces:**
- Consumes: nothing from this repo's Go code (talks to the container's HTTP endpoints only).
- Produces:

```go
type AccountRef struct{ Username, Password string } // Password is always "fake-center"
type VCenter struct {
    Server   string // "https://127.0.0.x" — no port (443)
    CABundle []byte
    Accounts []AccountRef
}
func (v *VCenter) Account(name string) (AccountRef, bool)
func Start(tb testing.TB, scenarioPath string) (*VCenter, error)
```
Internal (for tests): `type runner interface { run(ctx context.Context, args ...string) (string, error) }`, `func detectRuntime() (runner, error)`, `func allocLoopback() (string, error)`.

- [ ] **Step 1: Write the failing tests** (stdlib-only; no containers needed)

- `TestStartNoRuntime` — `detectRuntime` with `PATH` emptied → `Start` returns error containing "podman" and "docker".
- `TestLoopbackAllocation` — `allocLoopback` returns distinct IPs in `127.0.0.2–127.0.0.254` across calls when a previous IP's port 443 is dialed-refused; a candidate whose 443 accepts a TCP dial is skipped (simulate with `net.Listen` on a high port is not possible for 443 — use the dial-probe function against a local listener on `127.0.0.x:<highport>` by parameterizing the port in the probe function).
- `TestStartTimeoutIncludesLogs` — inject a fake `runner` whose container "runs" but whose health endpoint never becomes ready (serve a TLS server that always 503s), stub the startup timeout to 200ms → `Start` errors and the error contains a log-tail marker produced by the fake runner's `logs` path.
- `TestBuildRunArgs` — given IP `127.0.0.7`, name, and scenario bytes, the constructed args equal: `run --detach --name <name> --publish 127.0.0.7:443:443 --env FAKE_CENTER_SCENARIO=<yaml> --env FAKE_CENTER_SANS=127.0.0.7 <image>`; image defaults to `quay.io/ocp-splat/fake-center:latest`, overridden by `FAKE_CENTER_IMAGE`.

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd helper && go test ./...`
Expected: FAIL

- [ ] **Step 3: Implement `fakecenter.go` and `container.go`**

`Start` flow:
1. `detectRuntime()` — try `podman --version` then `docker --version`; else Review-Focus error.
2. Read scenario file; `allocLoopback()` — iterate `127.0.0.2..254`, dial-probe `:443`, take the first refused (retry once with the next IP if the subsequent `run` fails to become ready within timeout and logs contain "address already in use").
3. `run` the container with the pinned args; unique name `fake-center-<sanitized tb.Name()>-<4 hex>`.
4. Poll `GET https://<ip>/healthz` with an `InsecureSkipVerify` client (default timeout `FAKE_CENTER_STARTUP_TIMEOUT`, 120s — first-run image pull happens inside `run`).
5. `GET /ca.pem`; `GET /accounts.json` → `Accounts`.
6. `tb.Cleanup`: if `tb.Failed()`, write `<runtime> logs <name>` to `tb.Log`; then `<runtime> rm -f <name>`.

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd helper && go test ./...`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add helper/ go.mod go.sum
git commit -m "feat: add stdlib-only helper to launch fake-center containers from tests"
```

---

### Task 8: Container image (`Dockerfile`)

**Files:**
- Create: `Dockerfile`

**Interfaces:**
- Consumes: the server binary (Task 4–6).
- Produces: an OCI image whose default entrypoint serves on `0.0.0.0:443` and accepts `FAKE_CENTER_SCENARIO`/`FAKE_CENTER_SANS` env.

- [ ] **Step 1: Write the Dockerfile**

Multi-stage: `golang:1.25` builder (copy go.mod/go.sum, `go mod download`, copy source, `CGO_ENABLED=0 go build -trimpath -o /out/fake-center ./cmd/fake-center`); final stage `FROM scratch` with `/tmp` (mode 1777 — the simulator's datastore staging needs it) and the binary; `EXPOSE 443`; `ENTRYPOINT ["/fake-center"]`; `CMD ["-listen", "0.0.0.0:443"]`. No `USER` directive — in-container root binds 443 (user namespaces make this valid under rootless podman).

- [ ] **Step 2: Verify the image builds and serves**

Run:
```bash
podman build -t fake-center:dev .
podman run --rm -d --name fc-verify -p 127.0.0.1:443:443 \
  -e FAKE_CENTER_SCENARIO="$(cat internal/scenario/testdata/insufficient-privs.yaml)" \
  -e FAKE_CENTER_SANS=127.0.0.1 fake-center:dev
curl -sk --max-time 10 https://127.0.0.1/healthz   # → "ok" (200)
curl -sk --max-time 10 https://127.0.0.1/ca.pem    # → PEM
podman rm -f fc-verify
```
Expected: healthz 200 and PEM output (use `docker` if podman is absent). If the publish of 443 fails under rootless podman, record the failure mode — the helper's fallback (dial container IP, SAN passed via `FAKE_CENTER_SANS`) is the documented contingency in `docs/adoption.md`.

- [ ] **Step 3: Commit**

```bash
git add Dockerfile
git commit -m "build: add fake-center container image"
```

---

### Task 9: Integration test + adoption example (`examples/`, `helper/`)

**Files:**
- Create: `examples/scenarios/insufficient-privs.yaml` (copy of Task 3 testdata), `examples/installer_privileges_test.go`
- Modify: `helper/fakecenter_test.go` (add a runtime-gated integration test)

**Interfaces:**
- Consumes: `helper.Start` (Task 7), image (Task 8), govmomi (root module only).
- Produces: the worked example the adoption docs point to; `helper` integration coverage.

- [ ] **Step 1: Write the failing tests**

`helper/fakecenter_test.go` — `TestStartIntegration`, skipped unless a runtime **and** `FAKE_CENTER_IMAGE` are available (stdlib-only assertions): `Start(t, "testdata/minimal.yaml")` → `Server` has no port suffix (`https://127.0.0.N`), `CABundle` non-empty, `Accounts` matches the scenario; `x509` verify of a TLS dial to the server using the CA pool succeeds.

`examples/installer_privileges_test.go` (root module; imports helper + govmomi; skipped without runtime/image): `fakecenter.Start(t, "scenarios/insufficient-privs.yaml")`; fetch `Account("installer")`; build a client exactly like the installer's `mock/vsphere_sim.go GetClient` (write `CABundle` to a temp file, `soap.NewClient`, `SetRootCAs`, `vim25.NewClient`, `session.NewManager(...).Login(ctx, url.UserPassword("installer", "fake-center"))`); assert via `FetchUserPrivilegeOnEntities` that `VirtualMachine.Inventory.Create` is absent on fd2's datacenter and present on fd1's. This is the spec's first proof point, end to end.

- [ ] **Step 2: Run tests to verify they fail**

Run: `podman build -t fake-center:dev . && FAKE_CENTER_IMAGE=localhost/fake-center:dev go test ./examples/ && (cd helper && FAKE_CENTER_IMAGE=localhost/fake-center:dev go test ./...)`
Expected: FAIL on the new assertions (the image exists and the runtime is present, so the tests run rather than skip)

- [ ] **Step 3: Make them pass**

Fix helper issues the integration surfaces (typical: env quoting, readiness timing, SAN handling). No scope additions.

- [ ] **Step 4: Run tests to verify they pass**

Same command as Step 2. Expected: PASS (both modules)

- [ ] **Step 5: Commit**

```bash
git add examples/ helper/
git commit -m "test: end-to-end privilege-gap proof point via helper and image"
```

---

### Task 10: Documentation (first-class deliverable)

**Files:**
- Create: `README.md`, `docs/scenario-reference.md`, `docs/adoption.md`, `docs/ci-integration.md`

**Interfaces:**
- Consumes: everything shipped so far.
- Produces: self-service adoption material (spec's Documentation section).

- [ ] **Step 1: Write the four documents**

- `README.md` — what/why (link the design doc), quickstart from zero to a first scenario-backed test in one page, the govmomi fork `replace` and bump procedure, image tags.
- `docs/scenario-reference.md` — every schema field with its default, plus the pinned example scenario and the privilege-gap pattern (`missing`).
- `docs/adoption.md` — for test authors: writing a scenario, calling `helper.Start`, injecting the CA/URL into component code (the `soap.NewClient`/`SetRootCAs` pattern from `examples/installer_privileges_test.go`), and debugging (`podman logs`, `curl -k /healthz`, `/ca.pem`, `/accounts.json`, `FAKE_CENTER_IMAGE` pinning, the container-IP fallback contingency).
- `docs/ci-integration.md` — the copy-pasteable ci-operator fragment for a component unit test: `nested_podman: true`, `capabilities: [nested-podman]`, basing the test image on `ci/nested-podman`, and referencing `quay.io/ocp-splat/fake-center:<tag>`.

- [ ] **Step 2: Verify docs against reality**

Check: every command in the quickstart runs as written against the built image; the ci-operator fragment matches the patterns in `openshift/release`'s nested-podman docs and the installer's ci-operator config; scenario-reference covers every field in `internal/scenario/types.go`.

- [ ] **Step 3: Commit**

```bash
git add README.md docs/
git commit -m "docs: quickstart, scenario reference, adoption and CI integration guides"
```

---

### Task 11: CI workflow (`.github/workflows/ci.yaml`)

**Files:**
- Create: `.github/workflows/ci.yaml`

**Interfaces:**
- Consumes: all tasks.
- Produces: CI that gates PRs and publishes the image.

- [ ] **Step 1: Write the workflow**

Three jobs (mirror the fork's `.github/workflows/vcsim-image.yaml` conventions):
- `unit` — Go 1.25, `go test ./...` at root and in `helper/` (skips runtime-gated tests naturally).
- `integration` — build the image, run root tests + helper tests with `FAKE_CENTER_IMAGE=localhost/fake-center:test` (GitHub Actions ubuntu runners ship docker).
- `publish` — on push to `main` and tags `v*`: build and push to `quay.io/ocp-splat/fake-center` with tags `sha-${GITHUB_SHA:0:12}`, `latest`, and `v*`; login `ocp-splat+splat_team_push` with the `REGISTRY_PASSWORD` secret.

- [ ] **Step 2: Verify**

Push to a branch on GitHub and confirm the `unit` and `integration` jobs are green; verify the workflow YAML with `actionlint` if available. `publish` correctness is confirmed on first merge to `main` (secret is repo-configured; note it in the PR description if missing).

- [ ] **Step 3: Commit**

```bash
git add .github/workflows/ci.yaml
git commit -m "ci: unit, integration, and Quay publish workflows"
```

---

## Post-plan notes for the executor

- The fork pin (`dc14af853455`) drifts nightly by design; if `go mod tidy` cannot resolve it, pin the current `nightly-upstream-sync` HEAD instead and note it in the PR.
- Task 8's 443-publish verification is the design's spike: if rootless podman cannot publish 443, implement the documented fallback (dial the container IP obtained via `podman inspect`, pass it in `FAKE_CENTER_SANS`) inside `helper/container.go` and record the decision in `docs/adoption.md`.
- Scope guard: anything tempting toward VM workload fidelity, multi-vCenter, or adopt mode is v2 — the spec defers it.
