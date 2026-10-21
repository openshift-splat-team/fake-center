// Copyright 2026 The fake-center Authors.
// SPDX-License-Identifier: Apache-2.0

package fakecenter

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestStartNoRuntime(t *testing.T) {
	t.Setenv("FAKE_CENTER_RUNTIME", "")
	t.Setenv("PATH", t.TempDir())
	_, err := Start(t, "testdata/minimal.yaml")
	if err == nil || !strings.Contains(err.Error(), "podman") || !strings.Contains(err.Error(), "docker") || !strings.Contains(err.Error(), "install") {
		t.Fatalf("expected actionable runtime installation error, got %v", err)
	}
}

type integrationTB struct {
	testing.TB
	fallback bool
}

func (tb *integrationTB) Log(args ...any) {
	if strings.Contains(fmt.Sprint(args...), "443 fidelity") {
		tb.fallback = true
	}
	tb.TB.Log(args...)
}

func TestStartIntegration(t *testing.T) {
	if os.Getenv("FAKE_CENTER_IMAGE") == "" {
		t.Skip("set FAKE_CENTER_IMAGE to run container integration tests")
	}
	if _, err := detectRuntime(); err != nil {
		t.Skip(err)
	}
	tb := &integrationTB{TB: t}
	v, err := Start(tb, "testdata/minimal.yaml")
	if err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(v.Server)
	if err != nil {
		t.Fatal(err)
	}
	if u.Scheme != "https" || !regexp.MustCompile(`^127\.0\.0\.\d+$`).MatchString(u.Hostname()) {
		t.Fatalf("Server must be an HTTPS loopback URL: %s", v.Server)
	}
	if err := strictPublicationError(u); err != nil {
		t.Fatal(err)
	}
	if tb.fallback {
		port, err := strconv.Atoi(u.Port())
		if err != nil || port < 1024 || port > 65535 {
			t.Fatalf("fallback Server must carry a high port: %s", v.Server)
		}
		t.Log("exercised fallback publication:", v.Server)
	} else {
		if u.Port() != "" || u.Host != u.Hostname() {
			t.Fatalf("primary Server must have no port suffix: %s", v.Server)
		}
		t.Log("exercised primary port-443 publication:", v.Server)
	}
	if len(v.CABundle) == 0 {
		t.Fatal("CABundle is empty")
	}
	wantAccounts := []AccountRef{{Username: "installer", Password: "fake-center"}}
	if !reflect.DeepEqual(v.Accounts, wantAccounts) {
		t.Fatalf("Accounts = %#v, want %#v", v.Accounts, wantAccounts)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(v.CABundle) {
		t.Fatal("CABundle contains no certificates")
	}
	address := u.Host
	if u.Port() == "" {
		address = net.JoinHostPort(u.Hostname(), "443")
	}
	conn, err := tls.DialWithDialer(&net.Dialer{Timeout: 10 * time.Second}, "tcp", address, &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12})
	if err != nil {
		t.Fatalf("TLS verification with CABundle and loopback SAN: %v", err)
	}
	defer conn.Close()
	if len(conn.ConnectionState().VerifiedChains) == 0 {
		t.Fatal("TLS connection has no verified certificate chain")
	}
}

func strictPublicationError(u *url.URL) error {
	if os.Getenv("FAKE_CENTER_STRICT") == "1" && u.Port() != "" {
		return fmt.Errorf("FAKE_CENTER_STRICT=1 requires strict port-443 publication without a port suffix; Server = %s", u)
	}
	return nil
}

func TestStrictPublication(t *testing.T) {
	for _, tc := range []struct {
		name    string
		strict  string
		server  string
		wantErr bool
	}{
		{name: "default primary", server: "https://127.0.0.2"},
		{name: "default fallback", server: "https://127.0.0.2:49152"},
		{name: "strict primary", strict: "1", server: "https://127.0.0.2"},
		{name: "strict fallback rejected", strict: "1", server: "https://127.0.0.2:49152", wantErr: true},
		{name: "strict explicit 443 rejected", strict: "1", server: "https://127.0.0.2:443", wantErr: true},
		{name: "only one enables strict", strict: "0", server: "https://127.0.0.2:49152"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("FAKE_CENTER_STRICT", tc.strict)
			u, err := url.Parse(tc.server)
			if err != nil {
				t.Fatal(err)
			}
			err = strictPublicationError(u)
			if (err != nil) != tc.wantErr {
				t.Fatalf("strict publication error = %v, want error %t", err, tc.wantErr)
			}
			if err != nil && (!strings.Contains(err.Error(), "FAKE_CENTER_STRICT=1") || !strings.Contains(err.Error(), tc.server)) {
				t.Fatalf("strict failure must identify the setting and Server: %v", err)
			}
		})
	}
}

func TestDetectRuntime(t *testing.T) {
	t.Setenv("FAKE_CENTER_RUNTIME", "")
	for _, podmanWorks := range []bool{true, false} {
		t.Run(fmt.Sprint(podmanWorks), func(t *testing.T) {
			dir := t.TempDir()
			trace := filepath.Join(dir, "trace")
			for _, runtime := range []string{"podman", "docker"} {
				code := "0"
				if runtime == "podman" && !podmanWorks {
					code = "1"
				}
				script := fmt.Sprintf("#!/bin/sh\necho '%s' >> '%s'\n[ \"$1\" = --version ] || exit 2\nexit %s\n", runtime, trace, code)
				if err := os.WriteFile(filepath.Join(dir, runtime), []byte(script), 0700); err != nil {
					t.Fatal(err)
				}
			}
			t.Setenv("PATH", dir)
			if _, err := detectRuntime(); err != nil {
				t.Fatal(err)
			}
			got, err := os.ReadFile(trace)
			if err != nil {
				t.Fatal(err)
			}
			want := "podman\n"
			if !podmanWorks {
				want += "docker\n"
			}
			if string(got) != want {
				t.Fatalf("runtime detection order = %q, want %q", got, want)
			}
		})
	}
}

func TestDetectRuntimeOverride(t *testing.T) {
	for _, forced := range []string{"podman", "docker"} {
		for _, available := range []bool{true, false} {
			t.Run(fmt.Sprintf("%s/available=%t", forced, available), func(t *testing.T) {
				dir := t.TempDir()
				for _, executable := range []string{"podman", "docker"} {
					if executable == forced && !available {
						continue
					}
					if err := os.WriteFile(filepath.Join(dir, executable), []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
						t.Fatal(err)
					}
				}
				t.Setenv("PATH", dir)
				t.Setenv("FAKE_CENTER_RUNTIME", forced)
				runtime, err := detectRuntime()
				if !available {
					if err == nil || !strings.Contains(err.Error(), "FAKE_CENTER_RUNTIME") || !strings.Contains(err.Error(), forced) {
						t.Fatalf("expected clear forced-runtime error without fallback, got runtime=%v, err=%v", runtime, err)
					}
					return
				}
				if err != nil {
					t.Fatal(err)
				}
				if got := runtime.(commandRunner).executable; got != forced {
					t.Fatalf("selected runtime = %q, want %q", got, forced)
				}
			})
		}
	}
	t.Run("invalid", func(t *testing.T) {
		t.Setenv("FAKE_CENTER_RUNTIME", "other")
		_, err := detectRuntime()
		if err == nil || !strings.Contains(err.Error(), "FAKE_CENTER_RUNTIME") || !strings.Contains(err.Error(), "podman") || !strings.Contains(err.Error(), "docker") {
			t.Fatalf("expected supported-runtime validation error, got %v", err)
		}
	})
}

func TestLoopbackAllocation(t *testing.T) {
	first, err := allocLoopback()
	if err != nil {
		t.Fatal(err)
	}
	defer releaseLoopback(first)
	second, err := allocLoopback()
	if err != nil {
		t.Fatal(err)
	}
	defer releaseLoopback(second)
	for _, ip := range []string{first, second} {
		parsed := net.ParseIP(ip).To4()
		if parsed == nil || parsed[0] != 127 || parsed[1] != 0 || parsed[2] != 0 || parsed[3] < 2 || parsed[3] > 254 {
			t.Fatalf("invalid loopback IP %q", ip)
		}
	}
	if first == second {
		t.Fatalf("allocated %s twice despite reservation", first)
	}

	listener, err := net.Listen("tcp", "127.0.0.2:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	_, port, _ := net.SplitHostPort(listener.Addr().String())
	// Release the earlier reservations: the live listener must exclude .2.
	releaseLoopback(first)
	releaseLoopback(second)
	available, err := allocLoopbackPort(port)
	if err != nil {
		t.Fatal(err)
	}
	defer releaseLoopback(available)
	if available != "127.0.0.3" {
		t.Fatalf("allocation with .2 occupied = %s, want 127.0.0.3", available)
	}
}

func TestBuildRunArgs(t *testing.T) {
	for _, image := range []string{"", "example.com/custom:v1"} {
		t.Run(image, func(t *testing.T) {
			t.Setenv("FAKE_CENTER_IMAGE", image)
			wantImage := image
			if image == "" {
				wantImage = "quay.io/ocp-splat/fake-center:latest"
			}
			want := []string{"run", "--detach", "--name", "fake-center-test-abcd", "--publish", "127.0.0.7:443:443", "--env", "FAKE_CENTER_SCENARIO=accounts:\n- name: installer\n", "--env", "FAKE_CENTER_SANS=127.0.0.7", wantImage}
			got := buildRunArgs("127.0.0.7", "fake-center-test-abcd", []byte("accounts:\n- name: installer\n"))
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("args = %#v, want %#v", got, want)
			}
		})
	}
}

type runnerFunc func(context.Context, ...string) (string, error)

func (f runnerFunc) run(ctx context.Context, args ...string) (string, error) {
	return f(ctx, args...)
}

// recordedTB captures cleanup without marking the actual test as failed.
type recordedTB struct {
	testing.TB
	cleanups []func()
	failed   bool
	logs     []string
}

func (tb *recordedTB) Name() string     { return "Suite/Test with spaces!" }
func (tb *recordedTB) Failed() bool     { return tb.failed }
func (tb *recordedTB) Cleanup(f func()) { tb.cleanups = append(tb.cleanups, f) }
func (tb *recordedTB) Log(args ...any)  { tb.logs = append(tb.logs, fmt.Sprint(args...)) }
func (tb *recordedTB) cleanup() {
	for i := len(tb.cleanups) - 1; i >= 0; i-- {
		tb.cleanups[i]()
	}
}

// Redirect only the TCP connection; requests still target https://127.0.0.N
// and use the production insecure-TLS transport configuration.
func tlsClient(t *testing.T, handler http.HandlerFunc) *http.Client {
	t.Helper()
	server := httptest.NewTLSServer(handler)
	t.Cleanup(server.Close)
	client := newHTTPClient()
	transport := client.Transport.(*http.Transport)
	transport.DialContext = func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, server.Listener.Addr().String())
	}
	t.Cleanup(transport.CloseIdleConnections)
	return client
}

func TestStartTimeoutIncludesLogs(t *testing.T) {
	t.Setenv("FAKE_CENTER_STARTUP_TIMEOUT", "200ms")
	var requests atomic.Int32
	client := tlsClient(t, func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		http.Error(w, "not ready", http.StatusServiceUnavailable)
	})
	var commands []string
	runtime := runnerFunc(func(_ context.Context, args ...string) (string, error) {
		commands = append(commands, args[0])
		if args[0] == "logs" {
			return "log-tail-marker: server still starting", nil
		}
		return "", nil
	})
	tb := &recordedTB{TB: t}
	defer tb.cleanup()
	_, err := startWithRuntime(tb, "testdata/minimal.yaml", runtime, client)
	if err == nil || !strings.Contains(err.Error(), "log-tail-marker") {
		t.Fatalf("expected timeout with log tail, got %v", err)
	}
	if requests.Load() == 0 {
		t.Fatal("readiness never reached the TLS server")
	}
	if !reflect.DeepEqual(commands, []string{"run", "logs", "rm"}) {
		t.Fatalf("commands = %v; failed startup must remove the container", commands)
	}
}

func TestStartSuccessAndCleanup(t *testing.T) {
	client := tlsClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/healthz":
			w.WriteHeader(http.StatusOK)
		case "/ca.pem":
			fmt.Fprint(w, "test CA bundle")
		case "/accounts.json":
			fmt.Fprint(w, `[{"name":"from-server"}]`)
		default:
			http.NotFound(w, r)
		}
	})
	var calls [][]string
	logOutput := "container-log-marker" + strings.Repeat("x", 20*1024)
	runtime := runnerFunc(func(_ context.Context, args ...string) (string, error) {
		calls = append(calls, append([]string(nil), args...))
		return logOutput, nil
	})
	tb := &recordedTB{TB: t, failed: true}
	defer tb.cleanup()
	v, err := startWithRuntime(tb, "testdata/minimal.yaml", runtime, client)
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`^https://127\.0\.0\.\d+$`).MatchString(v.Server) {
		t.Fatalf("Server must be a loopback URL without a port: %s", v.Server)
	}
	if string(v.CABundle) != "test CA bundle" {
		t.Fatalf("CA = %q", v.CABundle)
	}
	account, ok := v.Account("from-server")
	if !ok || account != (AccountRef{Username: "from-server", Password: "fake-center"}) || len(v.Accounts) != 1 {
		t.Fatalf("accounts must come from the endpoint: %#v", v.Accounts)
	}
	if _, ok := v.Account("installer"); ok {
		t.Fatal("account unexpectedly parsed from scenario instead of server")
	}
	name := calls[0][3]
	if !regexp.MustCompile(`^fake-center-suite-test-with-spaces-[0-9a-f]{4}$`).MatchString(name) {
		t.Fatalf("invalid container name %q", name)
	}
	tb.cleanup()
	tb.cleanups = nil
	if len(calls) != 3 || !reflect.DeepEqual(calls[1], []string{"logs", name}) || !reflect.DeepEqual(calls[2], []string{"rm", "-f", name}) {
		t.Fatalf("cleanup calls = %v", calls)
	}
	if len(tb.logs) != 1 || tb.logs[0] != logOutput {
		t.Fatal("failed-test cleanup must log the full container output")
	}
}

func TestStartRetriesPublishCollision(t *testing.T) {
	t.Setenv("FAKE_CENTER_STARTUP_TIMEOUT", "200ms")
	var runs atomic.Int32
	var published []string
	client := tlsClient(t, func(w http.ResponseWriter, r *http.Request) {
		if runs.Load() == 1 {
			http.Error(w, "not ready", http.StatusServiceUnavailable)
			return
		}
		if r.URL.Path == "/accounts.json" {
			fmt.Fprint(w, "[]")
		}
	})
	runtime := runnerFunc(func(_ context.Context, args ...string) (string, error) {
		switch args[0] {
		case "run":
			runs.Add(1)
			published = append(published, args[5])
		case "logs":
			return "bind: address already in use\n" + strings.Repeat("later log output\n", 1500), nil
		}
		return "", nil
	})
	tb := &recordedTB{TB: t}
	defer tb.cleanup()
	if _, err := startWithRuntime(tb, "testdata/minimal.yaml", runtime, client); err != nil {
		t.Fatal(err)
	}
	if len(published) != 2 || published[0] == published[1] {
		t.Fatalf("expected exactly one retry with a different IP, got %v", published)
	}
}

func TestStartRetriesImmediatePublishCollision(t *testing.T) {
	for _, tc := range []struct {
		name       string
		output     string
		launchErr  error
		persistent bool
		wantRuns   int
		wantError  bool
	}{
		{name: "command output", output: "bind: address already in use", launchErr: errors.New("exit status 125"), wantRuns: 2},
		{name: "command error", launchErr: errors.New("listen tcp: address already in use"), wantRuns: 2},
		{name: "equivalent bind failure", output: "Bind for 127.0.0.2:443 failed: port is already allocated", launchErr: errors.New("exit status 125"), wantRuns: 2},
		{name: "port in use", launchErr: errors.New("PORT IS ALREADY IN USE"), wantRuns: 2},
		{name: "retry bounded", launchErr: errors.New("address already in use"), persistent: true, wantRuns: 2, wantError: true},
		{name: "unrelated failure", launchErr: errors.New("image pull denied"), wantRuns: 1, wantError: true},
		{name: "unrelated permission failure", launchErr: errors.New("exec /entrypoint: permission denied"), wantRuns: 1, wantError: true},
		{name: "collision takes precedence", launchErr: errors.New("bind: address already in use; permission denied"), wantRuns: 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := tlsClient(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/accounts.json" {
					fmt.Fprint(w, "[]")
				}
			})
			var published, names, removed []string
			runtime := runnerFunc(func(_ context.Context, args ...string) (string, error) {
				switch args[0] {
				case "run":
					published = append(published, args[5])
					names = append(names, args[3])
					if len(published) == 1 || tc.persistent {
						return tc.output, tc.launchErr
					}
				case "logs":
					// The runtime rejected publication before creating a container.
					return "", errors.New("no such container")
				case "rm":
					removed = append(removed, args[2])
				}
				return "", nil
			})
			tb := &recordedTB{TB: t}
			defer tb.cleanup()
			v, err := startWithRuntime(tb, "testdata/minimal.yaml", runtime, client)
			if (err != nil) != tc.wantError {
				t.Fatalf("startup error = %v, wantError = %v", err, tc.wantError)
			}
			if len(published) != tc.wantRuns {
				t.Fatalf("launches = %d, want %d", len(published), tc.wantRuns)
			}
			if len(published) == 2 && published[0] == published[1] {
				t.Fatalf("retry reused failed publish address %s", published[0])
			}
			if !tc.wantError && v == nil {
				t.Fatal("retry did not return the ready instance")
			}
			tb.cleanup()
			tb.cleanups = nil
			if !reflect.DeepEqual(removed, names) {
				t.Fatalf("removed containers = %v, launched = %v", removed, names)
			}
		})
	}
}

func TestStartFallsBackOnPublishPermission(t *testing.T) {
	for _, tc := range []struct {
		name       string
		output     string
		launchErr  error
		persistent bool
	}{
		{name: "command output", output: "pasta: Failed to bind port 443: Permission denied", launchErr: errors.New("exit status 125")},
		{name: "command error", launchErr: errors.New("listen tcp 127.0.0.2:443: bind: permission denied")},
		{name: "operation not permitted", launchErr: errors.New("bind: operation not permitted")},
		{name: "rootlessport", output: "rootlessport cannot expose privileged port 443", launchErr: errors.New("exit status 125")},
		{name: "macOS address assignment output", output: "listen tcp 127.0.0.2:443: bind: can't assign requested address", launchErr: errors.New("exit status 125")},
		{name: "macOS address assignment case variant", launchErr: errors.New("bind: Can't Assign Requested Address")},
		{name: "fallback bounded", launchErr: errors.New("bind: permission denied"), persistent: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := tlsClient(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/accounts.json" {
					fmt.Fprint(w, "[]")
				}
			})
			var calls [][]string
			var runs int
			runtime := runnerFunc(func(_ context.Context, args ...string) (string, error) {
				calls = append(calls, append([]string(nil), args...))
				switch args[0] {
				case "run":
					runs++
					if runs == 1 || tc.persistent {
						return tc.output, tc.launchErr
					}
				case "port":
					ip := strings.Split(calls[0][5], ":")[0]
					return ip + ":49152\n", nil
				}
				return "", nil
			})
			tb := &recordedTB{TB: t}
			defer tb.cleanup()
			v, err := startWithRuntime(tb, "testdata/minimal.yaml", runtime, client)
			if (err != nil) != tc.persistent {
				t.Fatalf("startup error = %v, want error = %v", err, tc.persistent)
			}
			if runs != 2 {
				t.Fatalf("launches = %d, want exactly 2", runs)
			}
			ip := strings.Split(calls[0][5], ":")[0]
			if calls[0][5] != ip+":443:443" || calls[1][0] != "rm" || calls[2][0] != "run" || calls[2][5] != ip+"::443" || calls[2][9] != "FAKE_CENTER_SANS="+ip {
				t.Fatalf("expected primary publication, removal, then ephemeral publication with same SAN: %v", calls)
			}
			if !tc.persistent {
				if v.Server != "https://"+ip+":49152" {
					t.Fatalf("fallback Server = %s", v.Server)
				}
				if !reflect.DeepEqual(calls[3], []string{"port", calls[2][3], "443/tcp"}) {
					t.Fatalf("port resolution = %v", calls[3])
				}
			}
			if len(tb.logs) != 1 || !strings.Contains(tb.logs[0], "443 fidelity") || !strings.Contains(tb.logs[0], "port-handling bugs") || !strings.Contains(tb.logs[0], "sudo sysctl -w net.ipv4.ip_unprivileged_port_start=0") {
				t.Fatalf("missing fallback warning/remediation: %v", tb.logs)
			}
		})
	}
}

func TestPublishedServerRejectsInvalidMapping(t *testing.T) {
	for _, tc := range []struct {
		name   string
		output string
		err    error
	}{
		{name: "runtime error", err: errors.New("port lookup failed")},
		{name: "empty mapping"},
		{name: "wrong host", output: "0.0.0.0:49152\n"},
		{name: "privileged port", output: "127.0.0.7:443\n"},
		{name: "out of range", output: "127.0.0.7:65536\n"},
		{name: "non-numeric port", output: "127.0.0.7:invalid\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			runtime := runnerFunc(func(context.Context, ...string) (string, error) {
				return tc.output, tc.err
			})
			if server, err := publishedServer(context.Background(), runtime, "test-container", "127.0.0.7"); err == nil || server != "" {
				t.Fatalf("invalid mapping produced Server %q, error %v", server, err)
			}
		})
	}
}
