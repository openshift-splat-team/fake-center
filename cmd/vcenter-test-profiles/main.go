// Copyright 2026 The fake-center Authors.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/openshift-splat-team/vcenter-test-profiles/internal/scenario"
	"github.com/openshift-splat-team/vcenter-test-profiles/internal/server"
)

type sans []string

func (s *sans) String() string { return strings.Join(*s, ",") }
func (s *sans) Set(value string) error {
	*s = append(*s, value)
	return nil
}

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() error {
	path := flag.String("scenario", "", "Path to scenario YAML")
	listen := flag.String("listen", "0.0.0.0:443", "Listen address")
	var extraSANs sans
	flag.Var(&extraSANs, "san", "Additional certificate SAN (repeatable)")
	flag.Parse()
	for _, san := range strings.Split(os.Getenv("VCENTER_TEST_PROFILES_SANS"), ",") {
		if san = strings.TrimSpace(san); san != "" {
			extraSANs = append(extraSANs, san)
		}
	}
	var sc *scenario.Scenario
	var err error
	if content := os.Getenv("VCENTER_TEST_PROFILES_SCENARIO"); content != "" {
		sc, err = scenario.Parse([]byte(content))
	} else if *path != "" {
		sc, err = scenario.Load(*path)
	} else {
		return fmt.Errorf("-scenario or VCENTER_TEST_PROFILES_SCENARIO is required")
	}
	if err != nil {
		return fmt.Errorf("load scenario: %w", err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	s, err := server.New(sc, server.Options{ListenAddr: *listen, ExtraSANs: extraSANs})
	if err != nil {
		return err
	}
	log.Printf("fake-center ready at %s", s.URL())
	<-ctx.Done()
	return s.Close()
}
