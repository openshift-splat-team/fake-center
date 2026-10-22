# Adopt fake-center in component tests

## 1. Describe the world

Keep scenario YAML beside your component tests, usually under `testdata/`.
Start with the [pinned privilege-gap scenario](../examples/scenarios/insufficient-privs.yaml)
and change only what your assertion needs. The [schema reference](scenario-reference.md)
lists every field and default. Supply at least one failure domain and an account
with `openshift-installer` grants on the intended domains. A scenario is an
immutable startup input; use a separate instance for another configuration.

## 2. Launch from your test

Add the independent, stdlib-only helper module to your component's Go module:

```sh
go get github.com/openshift-splat-team/fake-center/helper@latest
```

Commit your module's dependency pin. Import it as
`fakecenter "github.com/openshift-splat-team/fake-center/helper"` and call:

```go
vcenter, err := fakecenter.Start(t, "testdata/insufficient-privs.yaml")
if err != nil {
    t.Fatal(err)
}
account, ok := vcenter.Account("installer")
if !ok {
    t.Fatal("installer account missing")
}
// Inject vcenter.Server, vcenter.CABundle, and account credentials below.
```

Paths are relative to the test package's working directory. `Start` returns
`(*VCenter, error)`; `VCenter` has `Server` (HTTPS base URL), `CABundle` (PEM
bytes), and `Accounts` (`Username`/`Password` pairs). `Account(name)` returns a
pair plus a found boolean. Each call launches an isolated container, waits for
readiness, and registers teardown with `t.Cleanup`. Podman is preferred over
Docker when both are installed. Failed tests log container output before cleanup;
startup errors include a log tail.

| Helper environment variable | Default | Purpose |
| --- | --- | --- |
| `FAKE_CENTER_IMAGE` | `quay.io/ocp-splat/fake-center:latest` | Image/tag/digest to launch; use `fake-center:dev` after a local build. |
| `FAKE_CENTER_RUNTIME` | Autodetect Podman, then Docker | Force `podman` or `docker`; errors if the selected binary is unavailable. |
| `FAKE_CENTER_STARTUP_TIMEOUT` | `120s` | Positive Go duration, including image pull time; for example `3m`. |

The helper launches containers only; it does not adopt pre-provisioned services.

## 3. Inject URL, credentials, and trust

Use [`examples/installer_privileges_test.go`](../examples/installer_privileges_test.go)
as the canonical, executable client setup rather than disabling certificate
verification. It writes `CABundle` to a temporary CA file, parses `Server`, sets
the SOAP path to `/sdk`, creates `soap.NewClient(u, false)`, calls
`SetRootCAs(caPath)`, creates `vim25.NewClient`, and logs in through
`session.NewManager(...).Login` with `url.UserPassword(account.Username,
account.Password)`. The scenario account name is the username and its password
is `fake-center`.

Feed these same values into **your component's real connection path**: its server
URL setting, credentials, and custom CA file/pool setting. Do not hard-code a
host, strip a fallback port, change system trust, or set an insecure TLS flag.
If the component cannot accept a custom CA or server URL, add that injection
point in the consuming component before integrating the test. Assert the
component's validation result (including the affected failure domain), not just
the simulator's response. The worked example demonstrates the underlying
privilege query; it is not a full installer preflight invocation.

Beyond that explicit wiring, `Start` installs the instance CA into process
trust: it seeds a combined bundle from the current `SSL_CERT_FILE` (or the
system bundle) plus the instance CA and points `SSL_CERT_FILE` at it.
Components and subprocesses that use Go's default TLS trust — for example an
`openshift-install` binary the test executes — then trust the instance with no
code changes. Trust is only added, never replaced; the bundle intentionally
outlives the instance because Go caches system roots after first use.

### Testing your component's validation logic

The harness proves the **vCenter-side truth** (for example, `fd2` lacks
`VirtualMachine.Inventory.Create`). A test in the **consumer repository** proves
that its real validation consumes that truth correctly. Keep installer/component
imports there, not in fake-center. The following sketch uses the same trusted
client wiring as [`examples/installer_privileges_test.go`](../examples/installer_privileges_test.go);
replace `validation.ValidateVCenter` and its error sentinel with your component's
actual entry point and expected error. Standard-library and govmomi imports are
as in that example, plus your own validation package and `errors`/`strings`.

```go
func TestValidationReportsFD2PrivilegeGap(t *testing.T) {
    v, err := fakecenter.Start(t, "testdata/insufficient-privs.yaml")
    if err != nil { t.Fatal(err) }
    account, ok := v.Account("installer")
    if !ok { t.Fatal("installer account missing") }
    caPath := filepath.Join(t.TempDir(), "ca.pem")
    if err := os.WriteFile(caPath, v.CABundle, 0600); err != nil { t.Fatal(err) }
    u, err := url.Parse(v.Server)
    if err != nil { t.Fatal(err) }
    u.Path = "/sdk"
    transport := soap.NewClient(u, false)
    t.Cleanup(transport.CloseIdleConnections)
    if err := transport.SetRootCAs(caPath); err != nil { t.Fatal(err) }
    client, err := vim25.NewClient(t.Context(), transport)
    if err != nil { t.Fatal(err) }
    sessionMgr := session.NewManager(client)
    if err := sessionMgr.Login(t.Context(), url.UserPassword(account.Username, account.Password)); err != nil { t.Fatal(err) }
    defer func() { if err := sessionMgr.Logout(t.Context()); err != nil { t.Error(err) } }()
    err = validation.ValidateVCenter(t.Context(), client)
    if !errors.Is(err, validation.ErrInsufficientPrivileges) || !strings.Contains(err.Error(), "fd2") {
        t.Fatalf("expected fd2 privilege misconfiguration, got %v", err)
    }
}
```

## Port modes and fidelity

The server always listens on **443 inside the container**. The helper has two
host-facing modes:

1. **Primary: strict 443.** It publishes 443 on an allocated loopback address;
   `Server` is `https://<addr>` with **no port**. This works with rootful Docker,
   Prow nested-podman, and rootless Linux hosts where
   `net.ipv4.ip_unprivileged_port_start` is at most 443.
2. **Fallback: ephemeral high port.** On a publish-permission or address-assignment failure,
   it publishes a high port, includes that port in `Server`, and logs a warning
   about degraded 443 fidelity. This is convenient for local functional tests,
   but component port-handling bugs are exercised **only in strict mode**. Run
   strict mode where that fidelity matters, especially CI.

For strict local operation, an administrator can make this one-time runtime
change on the Linux container host:

```sh
sudo sysctl -w net.ipv4.ip_unprivileged_port_start=0
```

This permits unprivileged low-port binds system-wide, not just fake-center;
follow your host security policy. The setting may reset after reboot. Rerun the
test and check that no fallback warning appears. The shipped helper uses port
publishing, **not** the early design's direct-container-IP fallback; do not rely
on container IP reachability (particularly across a container VM).

On macOS, strict mode requires an alias on `lo0` for **each** loopback address
used (the helper starts with `127.0.0.2`; parallel instances use subsequent
addresses):

```sh
sudo ifconfig lo0 alias 127.0.0.2 up
```

Repeat with `127.0.0.3`, etc., as needed, or rely on the high-port fallback when
the runtime reports `can't assign requested address`. This fallback still needs
the runtime to support publication on the selected address. Both modes are
**not yet verified on macOS**; the Linux `sysctl` command above does not apply.

This repository's helper integration test enforces no-port-suffix publication
when `FAKE_CENTER_STRICT=1` is set, as in CI; it fails rather than accepting a
fallback result. Without that setting, the test accepts either mode. This is a
test assertion, not a switch that disables fallback in `Start`.

## Debug locally

Run tests with `-v -count=1` and pin `FAKE_CENTER_IMAGE` to a `sha-<12>` tag or
digest when reproducing a failure. Increase the startup timeout for slow pulls,
not to hide a bad scenario. Containers disappear at test cleanup; to inspect one
while the test is paused, find it with `podman ps` and use `podman logs <name>`.
Use Docker equivalents when Docker is the selected runtime.

For a standalone instance that survives the test lifecycle, run from this repo's
root (build `fake-center:dev` using the README first):

```sh
podman run --detach --name fake-center-debug \
  --publish 127.0.0.1:8443:443 \
  --env FAKE_CENTER_SCENARIO="$(cat examples/scenarios/insufficient-privs.yaml)" \
  --env FAKE_CENTER_SANS=127.0.0.1 \
  fake-center:dev
podman logs fake-center-debug
curl -sk -o /dev/null -w '%{http_code}\n' https://127.0.0.1:8443/healthz
curl -sk https://127.0.0.1:8443/ca.pem -o /tmp/fake-center-debug-ca.pem
curl --cacert /tmp/fake-center-debug-ca.pem https://127.0.0.1:8443/accounts.json
podman rm -f fake-center-debug
```

Wait for the `ready` log before curling. `/healthz` returns 200 with an empty
body when ready. `/ca.pem` serves the instance CA; `/accounts.json` lists account
names, not passwords. Use the trusted govmomi client pattern in the example to
inspect staged inventory and effective privileges over `/sdk`.

For manual launches, `FAKE_CENTER_SCENARIO` is YAML **content**, not a filename;
`FAKE_CENTER_SANS` is a comma-separated list of additional DNS/IP certificate
SANs for addresses clients will dial. The helper supplies both automatically.
The `curl -k` commands above are bootstrap/debugging only; component tests must
verify TLS with the returned CA. Keep diagnostic endpoints on a local/private
test network. Delete the temporary CA file when finished.
