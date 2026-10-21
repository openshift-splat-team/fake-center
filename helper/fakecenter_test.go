// Copyright 2026 The fake-center Authors.
// SPDX-License-Identifier: Apache-2.0

package fakecenter

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
)

func TestStartNoRuntime(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	_, err := Start(t, "testdata/minimal.yaml")
	if err == nil || !strings.Contains(err.Error(), "podman") || !strings.Contains(err.Error(), "docker") || !strings.Contains(err.Error(), "install") {
		t.Fatalf("expected actionable runtime installation error, got %v", err)
	}
}

func TestDetectRuntime(t *testing.T) {
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
