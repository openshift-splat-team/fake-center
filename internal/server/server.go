// Copyright 2026 The fake-center Authors.
// SPDX-License-Identifier: Apache-2.0

// Package server runs a TLS-enabled vSphere simulator for a scenario.
package server

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/openshift-splat-team/fake-center/internal/release"
	"github.com/openshift-splat-team/fake-center/internal/scenario"
	"github.com/vmware/govmomi"
	"github.com/vmware/govmomi/simulator"
	"github.com/vmware/govmomi/simulator/esx"

	// Register the simulator's REST/vAPI endpoints.
	_ "github.com/vmware/govmomi/vapi/simulator"
)

type Options struct {
	ListenAddr string   // Default "0.0.0.0:443"; tests use "127.0.0.1:0".
	ExtraSANs  []string // Additional certificate SANs.
}

type Server struct {
	model     *simulator.Model
	srv       *simulator.Server
	caPEM     []byte
	ready     atomic.Bool
	closeOnce sync.Once
}

// The fork's simulator uses a process-global ESXi product template. Serialize
// its mutation and all model creation/staging that consumes that template.
var hostTemplateMu sync.Mutex

// New starts the simulator and returns only after scenario staging is complete.
func New(sc *scenario.Scenario, opts Options) (*Server, error) {
	if sc == nil {
		return nil, fmt.Errorf("scenario is required")
	}
	if opts.ListenAddr == "" {
		opts.ListenAddr = "0.0.0.0:443"
	}
	model := simulator.VPX()
	model.Datacenter, model.Cluster, model.ClusterHost = 0, 0, 0
	model.Portgroup, model.PortgroupNSX, model.OpaqueNetwork = 0, 0, 0
	model.Host, model.Datastore, model.Machine = 0, 0, 0
	model.Pool, model.App, model.Pod, model.Folder = 0, 0, 0, 0
	model.Autostart = false
	hostTemplateMu.Lock()
	defer hostTemplateMu.Unlock()
	masquerade(model, sc.VCenter)
	if err := model.Create(); err != nil {
		return nil, fmt.Errorf("create simulator: %w", err)
	}
	s := &Server{model: model}
	cert, caPEM, err := certificates(opts.ExtraSANs)
	if err != nil {
		model.Remove()
		return nil, fmt.Errorf("generate certificates: %w", err)
	}
	s.caPEM = caPEM
	model.Service.RegisterEndpoints = true
	model.Service.TLS = &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}
	model.Service.Listen = &url.URL{Host: opts.ListenAddr}
	model.Service.ServeMux = http.NewServeMux()
	model.Service.ServeMux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		if !s.ready.Load() {
			http.Error(w, "not ready", http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	})
	model.Service.ServeMux.HandleFunc("/ca.pem", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-pem-file")
		_, _ = w.Write(s.caPEM)
	})
	type account struct {
		Name string `json:"name"`
	}
	accounts := make([]account, 0, len(sc.Accounts))
	for _, a := range sc.Accounts {
		accounts = append(accounts, account{Name: a.Name})
	}
	model.Service.ServeMux.HandleFunc("/accounts.json", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(accounts)
	})
	s.srv, err = listen(model.Service)
	if err != nil {
		_ = s.Close()
		return nil, err
	}
	ctx := context.Background()
	c, err := govmomi.NewClient(ctx, s.srv.URL, true)
	if err != nil {
		_ = s.Close()
		return nil, fmt.Errorf("connect staging client: %w", err)
	}
	defer c.Logout(ctx)
	if err := s.stageTopology(ctx, c.Client, sc); err != nil {
		_ = s.Close()
		return nil, fmt.Errorf("stage topology: %w", err)
	}
	if err := s.stagePermissions(ctx, c.Client, sc); err != nil {
		_ = s.Close()
		return nil, fmt.Errorf("stage permissions: %w", err)
	}
	s.ready.Store(true)
	return s, nil
}

// The simulator panics instead of returning socket bind errors. Translate only
// that known failure into New's error result; unrelated panics remain visible.
func listen(service *simulator.Service) (srv *simulator.Server, err error) {
	defer func() {
		if p := recover(); p != nil {
			if message, ok := p.(string); ok && strings.HasPrefix(message, "httptest: failed to listen on ") {
				err = fmt.Errorf("listen: %s", message)
			} else {
				panic(p)
			}
		}
	}()
	return service.NewServer(), nil
}

func masquerade(model *simulator.Model, vc scenario.VCenter) {
	about := &model.ServiceContent.About
	product := &esx.HostConfigInfo.Product
	about.Version = vc.Version
	product.Version = vc.Version
	product.LicenseProductVersion = vc.Version
	if serverAPI, hostAPI, ok := release.APIVersions(vc.Version); ok {
		about.ApiVersion = serverAPI
		product.ApiVersion = hostAPI
	}
	if vc.APIVersion != "" {
		about.ApiVersion = vc.APIVersion
	}
	if vc.Build != "" {
		about.Build = vc.Build
	} else if build, ok := release.Build(vc.Version, false); ok {
		about.Build = build
	}
	if vc.HostBuild != "" {
		product.Build = vc.HostBuild
	} else if build, ok := release.Build(vc.Version, true); ok {
		product.Build = build
	}
	product.FullName = fmt.Sprintf("%s %s build-%s", product.Name, product.Version, product.Build)
	about.FullName = fmt.Sprintf("%s %s build-%s (govmomi simulator)", about.Name, about.Version, about.Build)
}

// URL includes the simulator's default login and /sdk path.
func (s *Server) URL() string { return s.srv.URL.String() }

// CAPEM returns the per-instance CA certificate in PEM format.
func (s *Server) CAPEM() []byte { return append([]byte(nil), s.caPEM...) }

// Close stops serving and releases simulator resources. It is safe to repeat.
func (s *Server) Close() error {
	s.closeOnce.Do(func() {
		s.ready.Store(false)
		if s.srv != nil {
			s.srv.Close()
		}
		s.model.Remove()
	})
	return nil
}

func certificates(extraSANs []string) (tls.Certificate, []byte, error) {
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, nil, err
	}
	serverKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, nil, err
	}
	serialLimit := new(big.Int).Lsh(big.NewInt(1), 128)
	caSerial, err := rand.Int(rand.Reader, serialLimit)
	if err != nil {
		return tls.Certificate{}, nil, err
	}
	serverSerial, err := rand.Int(rand.Reader, serialLimit)
	if err != nil {
		return tls.Certificate{}, nil, err
	}
	now := time.Now()
	ca := &x509.Certificate{
		SerialNumber: caSerial,
		Subject:      pkix.Name{CommonName: "fake-center CA"},
		NotBefore:    now.Add(-time.Minute), NotAfter: now.AddDate(1, 0, 0),
		IsCA: true, BasicConstraintsValid: true,
		KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, ca, ca, &caKey.PublicKey, caKey)
	if err != nil {
		return tls.Certificate{}, nil, err
	}
	leaf := &x509.Certificate{
		SerialNumber: serverSerial,
		Subject:      pkix.Name{CommonName: "fake-center"},
		NotBefore:    ca.NotBefore, NotAfter: ca.NotAfter,
		KeyUsage:    x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IPAddresses: []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("::1")},
		DNSNames:    []string{"localhost"},
	}
	for _, san := range extraSANs {
		if ip := net.ParseIP(san); ip != nil {
			leaf.IPAddresses = append(leaf.IPAddresses, ip)
		} else {
			leaf.DNSNames = append(leaf.DNSNames, san)
		}
	}
	leafDER, err := x509.CreateCertificate(rand.Reader, leaf, ca, &serverKey.PublicKey, caKey)
	if err != nil {
		return tls.Certificate{}, nil, err
	}
	return tls.Certificate{Certificate: [][]byte{leafDER, caDER}, PrivateKey: serverKey},
		pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER}), nil
}
