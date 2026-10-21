// Copyright 2026 The fake-center Authors.
// SPDX-License-Identifier: Apache-2.0

// Package fakecenter launches isolated fake-center containers for tests.
package fakecenter

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
)

// AccountRef contains credentials for a scenario account.
type AccountRef struct{ Username, Password string }

// VCenter describes a running fake-center instance. Server has no port suffix:
// the HTTPS endpoint always uses port 443.
type VCenter struct {
	Server   string
	CABundle []byte
	Accounts []AccountRef
}

// Account looks up credentials by account name.
func (v *VCenter) Account(name string) (AccountRef, bool) {
	for _, account := range v.Accounts {
		if account.Username == name {
			return account, true
		}
	}
	return AccountRef{}, false
}

// Start launches a container from scenarioPath and registers its cleanup with
// tb. FAKE_CENTER_IMAGE overrides the default image; FAKE_CENTER_STARTUP_TIMEOUT
// accepts a Go duration (default 120s), including time spent pulling the image.
func Start(tb testing.TB, scenarioPath string) (*VCenter, error) {
	tb.Helper()
	runtime, err := detectRuntime()
	if err != nil {
		return nil, err
	}
	client := newHTTPClient()
	tb.Cleanup(client.CloseIdleConnections)
	return startWithRuntime(tb, scenarioPath, runtime, client)
}

func newHTTPClient() *http.Client {
	return &http.Client{Transport: &http.Transport{
		// The instance CA is not known until /ca.pem has been fetched.
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec
	}, Timeout: 5 * time.Second}
}

var invalidNameChars = regexp.MustCompile(`[^a-z0-9-]+`)

func containerName(testName string) (string, error) {
	var suffix [2]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		return "", fmt.Errorf("generate container name: %w", err)
	}
	name := strings.Trim(invalidNameChars.ReplaceAllString(strings.ToLower(testName), "-"), "-")
	return "fake-center-" + name + "-" + hex.EncodeToString(suffix[:]), nil
}

func startWithRuntime(tb testing.TB, scenarioPath string, runtime runner, client *http.Client) (*VCenter, error) {
	scenario, err := os.ReadFile(scenarioPath)
	if err != nil {
		return nil, fmt.Errorf("read scenario: %w", err)
	}
	timeout := 120 * time.Second
	if value := os.Getenv("FAKE_CENTER_STARTUP_TIMEOUT"); value != "" {
		timeout, err = time.ParseDuration(value)
		if err != nil || timeout <= 0 {
			return nil, fmt.Errorf("FAKE_CENTER_STARTUP_TIMEOUT must be a positive Go duration, got %q", value)
		}
	}
	ip, err := allocLoopback()
	if err != nil {
		return nil, err
	}
	for attempt := 0; attempt < 2; attempt++ {
		name, err := containerName(tb.Name())
		if err != nil {
			releaseLoopback(ip)
			return nil, err
		}
		// Capture this attempt, not the next IP selected after a collision.
		containerIP := ip
		var once sync.Once
		cleanup := func() {
			once.Do(func() {
				if tb.Failed() {
					logs, logErr := containerLogs(runtime, name)
					tb.Log(logs)
					if logErr != nil {
						tb.Log(logErr)
					}
				}
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				if _, err := runtime.run(ctx, "rm", "-f", name); err != nil {
					tb.Log("remove fake-center container: ", err)
				}
				releaseLoopback(containerIP)
			})
		}
		tb.Cleanup(cleanup)
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		_, err = runtime.run(ctx, buildRunArgs(ip, name, scenario)...)
		var v *VCenter
		if err == nil {
			v, err = waitForVCenter(ctx, client, "https://"+ip)
		}
		if err == nil {
			cancel()
			return v, nil
		}
		// exec errors need not wrap the context error when the deadline kills
		// the runtime (for example, while pulling an image).
		timedOut := errors.Is(err, context.DeadlineExceeded) || ctx.Err() == context.DeadlineExceeded
		cancel()
		logs, logErr := containerLogs(runtime, name)
		publishCollision := strings.Contains(strings.ToLower(logs), "address already in use")
		const maxTail = 16 * 1024
		if len(logs) > maxTail {
			logs = logs[len(logs)-maxTail:]
		}
		startupErr := fmt.Errorf("start fake-center %s: %w; container log tail:\n%s", name, err, logs)
		if logErr != nil {
			startupErr = fmt.Errorf("%w\nread container logs: %v", startupErr, logErr)
		}
		if attempt == 0 && timedOut && publishCollision {
			// Reserve the next address while the failed one is still held.
			nextIP, allocErr := allocLoopback()
			cleanup()
			if allocErr != nil {
				return nil, fmt.Errorf("%w; retry allocation: %v", startupErr, allocErr)
			}
			ip = nextIP
			continue
		}
		cleanup()
		return nil, startupErr
	}
	panic("unreachable")
}

func containerLogs(runtime runner, name string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return runtime.run(ctx, "logs", name)
}

func waitForVCenter(ctx context.Context, client *http.Client, server string) (*VCenter, error) {
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		if _, err := get(ctx, client, server+"/healthz"); err == nil {
			break
		}
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("wait for /healthz: %w", ctx.Err())
		case <-ticker.C:
		}
	}
	ca, err := get(ctx, client, server+"/ca.pem")
	if err != nil {
		return nil, err
	}
	data, err := get(ctx, client, server+"/accounts.json")
	if err != nil {
		return nil, err
	}
	var accounts []struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(data, &accounts); err != nil {
		return nil, fmt.Errorf("decode /accounts.json: %w", err)
	}
	v := &VCenter{Server: server, CABundle: ca}
	for _, account := range accounts {
		v.Accounts = append(v.Accounts, AccountRef{Username: account.Name, Password: "fake-center"})
	}
	return v, nil
}

func get(ctx context.Context, client *http.Client, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: %s", url, resp.Status)
	}
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("GET %s: %w", url, err)
	}
	return data, nil
}
