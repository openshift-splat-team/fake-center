// Copyright 2026 The fake-center Authors.
// SPDX-License-Identifier: Apache-2.0

package fakecenter

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strconv"
	"sync"
	"syscall"
	"time"
)

type runner interface {
	run(ctx context.Context, args ...string) (string, error)
}

type commandRunner struct{ executable string }

func (r commandRunner) run(ctx context.Context, args ...string) (string, error) {
	output, err := exec.CommandContext(ctx, r.executable, args...).CombinedOutput()
	if err != nil {
		return string(output), fmt.Errorf("%s %v: %w: %s", r.executable, args, err, output)
	}
	return string(output), nil
}

func detectRuntime() (runner, error) {
	for _, executable := range []string{"podman", "docker"} {
		r := commandRunner{executable: executable}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_, err := r.run(ctx, "--version")
		cancel()
		if err == nil {
			return r, nil
		}
	}
	return nil, errors.New("no container runtime available; install podman or docker and ensure it is on PATH")
}

func buildRunArgs(ip, name string, scenario []byte) []string {
	image := os.Getenv("FAKE_CENTER_IMAGE")
	if image == "" {
		image = "quay.io/ocp-splat/fake-center:latest"
	}
	return []string{
		"run", "--detach", "--name", name,
		"--publish", net.JoinHostPort(ip, "443") + ":443",
		"--env", "FAKE_CENTER_SCENARIO=" + string(scenario),
		"--env", "FAKE_CENTER_SANS=" + ip,
		image,
	}
}

var loopbacks = struct {
	sync.Mutex
	used map[string]bool
}{used: make(map[string]bool)}

func allocLoopback() (string, error) { return allocLoopbackPort("443") }

// Reservations prevent concurrent starts in this process from selecting the
// same IP even before their containers begin listening. Probing also avoids
// listeners owned by other processes; Start handles the remaining publish race.
func allocLoopbackPort(port string) (string, error) {
	loopbacks.Lock()
	defer loopbacks.Unlock()
	for octet := 2; octet <= 254; octet++ {
		ip := "127.0.0." + strconv.Itoa(octet)
		if loopbacks.used[ip] {
			continue
		}
		conn, err := net.DialTimeout("tcp", net.JoinHostPort(ip, port), 50*time.Millisecond)
		if err == nil {
			conn.Close()
			continue
		}
		// A timeout or routing error does not establish that the port is free.
		if !errors.Is(err, syscall.ECONNREFUSED) {
			continue
		}
		loopbacks.used[ip] = true
		return ip, nil
	}
	return "", fmt.Errorf("no free loopback address in 127.0.0.2–127.0.0.254 on port %s", port)
}

func releaseLoopback(ip string) {
	loopbacks.Lock()
	defer loopbacks.Unlock()
	delete(loopbacks.used, ip)
}
