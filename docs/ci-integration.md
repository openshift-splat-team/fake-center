# Component unit tests in OpenShift CI

Run fake-center inside the unit-test pod with nested Podman; no vSphere cluster
profile or hardware is needed. All 13 build farm clusters carry the
`nested-podman` capability. Request it explicitly so scheduling remains correct.

## Copy-pasteable ci-operator fragment

Merge these stanzas into your component's existing ci-operator configuration in
`openshift/release`; retain its existing `build_root` and resource settings.
This example compiles a single Go test package in `test-bin`, then runs its test
binary in an image based on `ci/nested-podman`. The runtime image needs no Go
toolchain. Before copying, substitute:

- `./pkg/vsphere`: your package containing fake-center tests.
- `github.com/your-org/your-component/pkg/vsphere/testdata/.`: its scenario directory
  under the checked-out source path. Tests here use `testdata/<scenario>.yaml`.
- `<tag>`: an existing fake-center `sha-<12>` or release tag; do not leave the
  placeholder literal. Use a digest instead if desired.

```yaml
base_images:
  nested-podman:
    namespace: ci
    name: nested-podman
    tag: latest

test_binary_build_commands: |
  go test -c -o /tmp/vsphere.test ./pkg/vsphere

images:
  items:
  - to: fake-center-tests
    from: nested-podman
    dockerfile_literal: |
      FROM nested-podman
      COPY vsphere.test /opt/component-tests/vsphere.test
      COPY testdata/ /opt/component-tests/testdata/
      WORKDIR /opt/component-tests
    inputs:
      test-bin:
        paths:
        - source_path: /tmp/vsphere.test
          destination_dir: .
        - source_path: /go/src/github.com/your-org/your-component/pkg/vsphere/testdata/.
          destination_dir: testdata/

tests:
- as: unit-fake-center
  capabilities: [nested-podman]
  nested_podman: true
  commands: |
    export FAKE_CENTER_IMAGE=quay.io/ocp-splat/fake-center:<tag>
    /opt/component-tests/vsphere.test -test.v
  container:
    from: fake-center-tests
```

Append to an existing `test_binary_build_commands` rather than replacing it.
Add analogous binaries/data for additional packages and invoke each explicitly;
all tests in the compiled package run here. Preserve executable permissions on
the binary and keep scenario paths relative to `WORKDIR`. If your tests require
CGO libraries or external tools, add those to the derived image too.

Three pieces are required together:

1. `capabilities: [nested-podman]` on the test selects eligible build clusters.
2. `nested_podman: true` enables nested-container execution for the test pod.
3. The derived image's `from: nested-podman` uses the `ci/nested-podman:latest`
   base (Podman, catatonit, and its setup entrypoint). Do not replace its
   entrypoint or init setup with a generic Go image.

The helper pulls the pinned Quay image at runtime, registers container cleanup,
and logs container output on test failure. Ensure the job can pull that image;
the default startup budget is 120s, including the pull. Inspect verbose test
output for a high-port warning: CI should use [strict 443](adoption.md#port-modes-and-fidelity)
so component port-handling bugs are covered. Do not install the fake's CA in a
shared trust store; inject each instance's returned CA into the component client.

## References and validation

This wiring follows the OpenShift CI
[Run Podman in a Test Container](https://docs.ci.openshift.org/how-tos/nested-podman/)
guide (single-stage `tests[].nested_podman`, capability, base image, and image
input paths). The installer already uses the capability in
[`e2e-agent-two-node-fencing-ipv4`](https://github.com/openshift/release/blob/main/ci-operator/config/openshift/installer/openshift-installer-main.yaml)
and related jobs. Its e2e workflow is precedent for nested Podman, not a required
dependency for these unit tests.

After editing, use the `openshift/release` repository's config validation and
Prow job generation workflow, commit the generated job changes with the config,
and rehearse `unit-fake-center` on the PR. A local container test verifies the
scenario/client but does not replace a Prow rehearsal of nested-container setup.
