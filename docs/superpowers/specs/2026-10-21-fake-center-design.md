# fake-center: a fake vCenter test harness for OpenShift components

Status: draft for review
Date: 2026-10-21

## Intent and why

OpenShift components that talk to vCenter — the installer's preflight
validations, machine-api's vSphere provider, the storage operator, and others —
contain logic to detect misconfigurations on the vCenter side: insufficient
account privileges, unsupported API versions, missing or malformed inventory
(networks, datastores, failure domains). That logic is currently tested at two
extremes:

- **Unit tests** with mocks, which don't exercise the vSphere API surface at
  all. The validation code paths most likely to break against a real vCenter —
  parsing API responses, resolving privileges, checking versions — go untested.
- **e2e tests** against real vCenter hardware (the `vsphere-elastic` cluster
  profile): expensive, slow, shared infrastructure, gated behind
  `always_run: false`.

fake-center fills the gap between: a fake vCenter API, realistic enough to
exercise real validation code paths, cheap enough to start from an ordinary
`go test`, running identically on a developer laptop and in prow.

The measure of success: **component validation logic — the code that detects
vCenter-side misconfigurations — is tested against a vSphere API without real
hardware.**

The first proof point: an installer preflight validation correctly detects an
account missing `VirtualMachine.Inventory.Create` on one failure domain (but
not the others) and reports the right error.

## Goals

- Components run their **real** vCenter-connection and validation code against
  the fake from ordinary unit tests (`go test ./pkg/...`).
- **Scenarios are declared by consuming components** as small config files
  describing high-level shape (failure domains, accounts, privileges,
  versions). fake-center expands them into a fully staged, believable
  inventory with sensible defaults, so component authors never hand-assemble
  inventory.
- **Instances are immutable**: a scenario fully determines the world. Same
  config in, same world out. A failing test's environment is completely
  described by the scenario file it named. Parallelism is just N instances.
- **Serves on 443 with a TLS cert tests can trust.** Components do not
  consistently honor configured ports, so a fake on an ephemeral port silently
  skips the code paths under test. The fake must behave like production:
  default port, real TLS, verifiable chain.
- **Adoption is self-service.** Documentation is a first-class deliverable,
  written for test authors in any component (installer first, then
  machine-api, storage operator, and the rest), good enough to integrate the
  harness — scenario, helper, CI job — without talking to fake-center's
  authors. See "Documentation and adoption" below.
- **Works locally (podman/docker) and in prow (nested-podman).** All 13 build
  farm clusters carry the `nested-podman` capability; the installer repo
  already runs jobs with it. See "prow integration" below.

## Non-goals

- Simulating actual VM workloads or power-state choreography beyond what vcsim
  provides natively. If a component test needs "create a VM and watch it
  exist," that works because vcsim works, but fidelity of VM behavior is not a
  goal.
- Performance or scale testing.
- Full `openshift-install create` runs against the fake. Real installs remain
  the job of e2e against real vCenter. (This may come later; it is not part of
  the first cut.)
- Replacing the real-vCenter e2e jobs (`vsphere-elastic` remains the source of
  truth for hardware behavior).
- Mutating a running instance mid-test. Immutable by design; a different
  world is a new instance.
- Multiple vCenters per scenario in v1. The installer supports failure
  domains spread across multiple vCenters (there is even a
  `e2e-vsphere-multi-vcenter-ovn` CI job), but v1 is single-vCenter. The
  schema is designed so a vCenter dimension can be added later without a
  breaking migration.

## Anti-goals (failure modes)

- Flaky or slow container startup in prow. Startup budget is seconds.
- Scenario files so verbose that authors hand-assemble inventory instead of
  using the baked-in defaults — that means the defaults failed.
- The helper module dragging heavy dependencies into component repos.
- Scope creep toward workload simulation before the validation layer is solid.

## Current state (inputs)

The `openshift-splat-team/govmomi` fork (branch `nightly-upstream-sync`,
nightly-synced off upstream `main`) already provides:

- **Real privilege resolution** in the simulator's AuthorizationManager:
  role assignments honored, `Propagate` respected, `NoAccess` overriding,
  resolution against the session's user. "Insufficient privileges" is a
  simulatable condition, not a hard-coded `true`.
- **Version masquerade**: `-version 9.0.2` infers server and host API versions
  and build numbers from release mapping tables (8.0.0–9.1.1), overridable via
  `-api-version`, `-build`, `-host-build`.
- ESXi 9.0 support in vim25 types.
- A minimal scratch vcsim container image published to `quay.io/ocp-splat/vcsim`.

The fork stays **upstream-first**: simulator behavior fixes belong there (and
ideally upstream). Only fake-center's server build consumes the fork as a Go
module; consuming components never depend on it.

The `fake-center` repository is currently empty and is the home for everything
described here.

## Architecture

Three parts:

### 1. Server binary + container image

A Go binary in this repo (`cmd/fake-center` or similar) that:

- imports the forked govmomi simulator (server-side only),
- reads a scenario config (YAML) at boot,
- stages the entire world **in-process** before serving: datacenters,
  clusters/hosts, folders, port groups, datastores — sized per the scenario's
  failure domains — plus accounts (principals, roles, per-entity grants and
  deliberate gaps) applied through the simulator's AuthorizationManager, plus
  version/build masquerade,
- serves the vSphere API (vim25 SOAP + vAPI endpoints vcsim already exposes)
  on **443**,
- generates a fresh CA and server certificate at boot, with SANs covering the
  address tests will dial,
- publishes to Quay (e.g. `quay.io/ocp-splat/fake-center`).

Staging happens server-side (in-process against the simulator), not by driving
the vSphere API from outside. It is faster, deterministic, and can express
things the public API cannot (session principals, boot-time version settings).

### 2. Scenario schema

Component-owned YAML files describing high-level shape, roughly:

- vCenter version (string; server/host API versions and builds inferred),
- failure domains: count, or explicit per-domain topology (datacenter,
  cluster, networks, datastores, resource pools),
- accounts: principals and their grants — including **gaps**, expressed as
  "role X on entity Y minus privilege Z" or as explicit insufficient roles,
- escape hatch for explicit inventory where defaults aren't enough.

fake-center expands defaults: hosts per cluster, folder names, port groups,
datastore names, tag categories, etc., so that common scenarios are a few
lines. The schema starts minimal, derived from what the installer's preflight
validations actually check (privileges, failure-domain topology, versions),
and grows as machine-api and the storage operator adopt it.

### 3. Helper module

A dependency-light Go package (what components import):

```go
vcenter, err := fakecenter.Start(t, "scenarios/insufficient-privs.yaml")
// vcenter.Server   — https://<addr> (no port: 443)
// vcenter.Username — principal with the deliberate gap
// vcenter.Password
// vcenter.CABundle — PEM of this instance's CA
```

Responsibilities:

- Launch the container by exec'ing `podman run` / `docker run` (image, port
  strategy, scenario file passed through), **or adopt** a pre-provisioned
  instance via env vars (`FAKE_CENTER_URL`, `FAKE_CENTER_USERNAME`, ...) when
  the environment can't run nested containers.
- Poll readiness.
- Fetch the CA bundle.
- Register teardown with `t.Cleanup`.
- Be parallel-safe: each `Start` gets its own container and network.

The helper **must not** import govmomi (forked or upstream) — components
already vendor upstream govmomi and must never be pushed onto the fork. Zero
non-stdlib dependencies is the target.

### Key mechanics

- **443 reachability.** The server binds 443 inside its container (user
  namespaces make privileged ports bindable under rootless podman). The
  helper either publishes 443 into the test's network namespace or dials the
  container IP directly; the URL handed to component code carries no port.
  Which pattern works best in rootless podman is a spike (below).
- **Cert trust.** Per-instance CA generated at boot; SANs cover the address
  the test will dial. The helper fetches the CA PEM and returns it; the test
  injects it into the component's TLS configuration. Tests must not need to
  touch system trust stores.
- **prow integration.** A component's unit-test job gets `nested_podman: true`
  and `capabilities: [nested-podman]`, and its test image is based on
  `ci/nested-podman` (has podman + catatonit). For the installer this means
  rebasing the `test-bin` image or adding a new test target alongside `unit`.
  Local dev requires podman or docker and nothing else.
- **Adopt mode.** Env-var adoption is a first-class mode, not a fallback hack:
  it works in every environment and is the migration path for components
  whose CI can't run nested containers yet.

## Constraints

- Helper module imports: stdlib only.
- The govmomi fork remains upstream-first; fake-center consumes it as a module
  at build time and never re-vendors simulator changes.
- Scenario files live in consuming repos; this repo ships the schema,
  defaults, server, helper, and docs — not a scenario library.
- All component platforms are Linux CI (prow) plus developer laptops (Linux
  and macOS). The launch path must work on both; where macOS differs (e.g.
  container IP not reachable), publish-mode is the answer.

## Decisions made with the sponsor

- Standalone container service + thin Go helper, not an in-process library —
  so consumers never depend on the forked govmomi.
- Configured instances are immutable.
- 443 is mandatory; components' inconsistent port handling is precisely a
  thing the harness must not paper over.
- The Go helper module ships as part of this project.
- First test: installer preflight privilege validation on a failure domain.
- Multi-vCenter scenarios are deferred out of v1; the schema keeps room for a
  vCenter dimension later.
- The project is delivered as a **clean handoff** even though the sponsor owns
  installer CI: every integration step (writing scenarios, wiring the helper
  into unit tests, adding the ci-operator job) is documented for a test author
  in any component, and the installer-side work is driven from those docs like
  anyone else's would be.

## Documentation and adoption (first-class deliverable)

The harness succeeds when test authors adopt it without a conversation. Ship,
in this repo:

- **A quickstart** — from zero to a first scenario-backed unit test in one
  page, runnable locally with podman/docker.
- **An adoption guide for test authors** — how to write a scenario file, how
  to call the helper from `go test`, how to inject the CA and URL into the
  component under test, and how to debug a scenario locally (inspect the
  staged world, read the fake's logs).
- **A CI integration guide** — the exact ci-operator changes a component
  needs: `nested_podman: true`, `capabilities: [nested-podman]`, basing the
  test image on `ci/nested-podman`, with a copy-pasteable config fragment.
- **A scenario schema reference** — every field, its defaults, and worked
  examples including the first proof-point scenario (privilege gap on one
  failure domain).
- Where it helps, wrap the guide as a reusable skill or template so test
  authors get a checklist rather than a document to read.

## Builder's judgment calls (flagged for review)

- **Hand-rolled launcher** (exec `podman`/`docker` CLI) instead of
  testcontainers-go: zero dependency weight, works in nested-podman
  environments, trivially debuggable. Revisit if lifecycle needs grow.
- **Server-side staging** of scenarios (in-process against the simulator)
  rather than helper-side via the vSphere API.
- **Per-instance CA** generated at boot, fetched by the helper, rather than a
  shared long-lived CA baked into the image.
- The scenario file reaches the container via env var or mounted file, with
  the helper owning the plumbing.
- **v1 ships launch-only.** Since nested-podman is confirmed on every build
  cluster and locally, container launching covers all first-cut consumers.
  Adopt mode (env-var pre-provisioned instance) is a later addition, and when
  it lands the helper will **verify** the adopted instance matches the
  requested scenario rather than silently returning a different world.
- **Multi-arch and laptop details** (macOS port publishing, deterministic
  default naming, image-tag override via env) are settled during the build
  without changing the architecture.

## Spikes and decisions left to the builder

1. **443 pattern in rootless podman** (publish 443 vs dial container IP):
   half-day spike, decides the helper's port strategy and the cert SAN
   strategy with it.
2. **Cert SAN approach**: fixed instance hostname mapped via hosts trick vs
   dynamic SANs for the dialed address.
3. **Scenario schema v1 field list**: derive from the installer's preflight
   validation code (privileges checked, failure-domain topology, version
   gates) — enumerate what the validations read, make those expressible.
   While enumerating, also record the **injection points** the installer's
   validation code offers (custom CA pool, server URL), since the first test
   must construct the component's client against the fake; if an injection
   point is missing, that's an installer-side change to note in the adoption
   guide.
4. **Readiness signal**: dedicated health endpoint on a sidecar port.
5. **Image relationship**: new `quay.io/ocp-splat/fake-center` image built
   from this repo (consuming the fork as a module) vs extending the existing
   vcsim image.
6. **Module layout**: single module vs separate server/helper modules
   (helper-imports-stay-light argues for either; decide when the repo is
   initialized).
7. **Read vcsim capabilities** needed by scenario staging (folders, port
   groups, tag categories, distributed switches) — enumerate gaps between
   what the simulator can stage today and what installer validations read,
   since each gap is either fork work or a schema deferral.

## Out of scope reminders

Not doing: VM workload fidelity, performance testing, full installer e2e,
mid-test mutation, replacing real-hardware e2e.
