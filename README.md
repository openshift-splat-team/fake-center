# fake-center

A scenario-backed fake vCenter for OpenShift component tests. Exercise real
vSphere connection and validation code—privilege checks, version gates, and
inventory discovery—without shared hardware. Each container stages an isolated
world and serves HTTPS on port 443 with its own CA. This complements, not
replaces, real-vCenter e2e tests. See the [design](docs/superpowers/specs/2026-10-21-fake-center-design.md).

## Quickstart: first scenario-backed test

Prerequisites: Git, Go 1.25+, and a working Podman or Docker installation on PATH
(the helper prefers Podman). From a fresh checkout:

```sh
git clone https://github.com/openshift-splat-team/fake-center.git
cd fake-center
podman build -t fake-center:dev .
FAKE_CENTER_IMAGE=fake-center:dev go test -count=1 -v ./examples -run '^TestInstallerPrivileges$'
```

With Docker only, replace `podman build` with `docker build`. Already have a
checkout and `fake-center:dev`? Run just the last command from the repository root.

Expect `PASS` for both `fd1` and `fd2`. The test loads
[`examples/scenarios/insufficient-privs.yaml`](examples/scenarios/insufficient-privs.yaml),
starts the container, trusts its CA, logs in as `installer`, and verifies that
`VirtualMachine.Inventory.Create` is missing only on `fd2`. The helper removes the
container automatically. Some rootless hosts log a high-port fallback warning;
see [port modes](docs/adoption.md#port-modes-and-fidelity).

Next: put a scenario in your component repo, import the stdlib-only
`github.com/openshift-splat-team/fake-center/helper` module, and call
`fakecenter.Start(t, scenarioPath)`. Follow the [adoption guide](docs/adoption.md)
and the [canonical worked test](examples/installer_privileges_test.go) for CA and
URL injection into your real component client.

## Images and dependency updates

The canonical image is `quay.io/ocp-splat/fake-center`:

- `sha-<12>`: source commit's first 12 hexadecimal characters; pin for reproducibility.
- `latest`: moving main-branch image; the helper's default tag.
- `v*`: release tags, for example `v0.1.0` when released.

Set `FAKE_CENTER_IMAGE` to select an image or digest. Server-side simulator fixes
come from the nightly-upstream-synced govmomi fork. The root `go.mod` pins:

```go
replace github.com/vmware/govmomi => github.com/openshift-splat-team/govmomi v0.0.0-20261013151717-dc14af853455
```

To bump it, update that replacement version and `go.sum` in a PR, run
`go mod tidy`, `go test ./...`, and the quickstart against a rebuilt image, and
review the fork changes. Components importing only the helper do **not** need
this replacement or the fork: their govmomi client stays upstream.

## Guides

- [Scenario reference](docs/scenario-reference.md): fields, defaults, privilege gaps.
- [Adoption](docs/adoption.md): helper lifecycle, client trust, port fidelity, debugging.
- [CI integration](docs/ci-integration.md): component tests in Prow with nested Podman.
