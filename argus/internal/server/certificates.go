// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package server

import (
	"context"
	"net/http"
	"sort"
	"time"
)

// Settings > Certificates: every certificate the monitoring runs on and when it ends. The core's come
// from core-certs.json, which argus-cert-renew writes into the update dir at every run (the monitoring
// CA, the https front, the Zabbix server); the probes' from their minute reports (when their own
// certificate ends) and from Argus's own renewals of them (probecert.go). Read-only: the renewals run
// by themselves, and the page says which ones do.

const (
	coreCertsFile = "core-certs.json"
	// probeCertRenewSince is the first probe image that renews its own certificate.
	probeCertRenewSince = "7.0.32-r8"
)

type coreCert struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	File    string `json:"file"`
	Subject string `json:"subject"`
	Ends    int64  `json:"ends"`
	ByCA    bool   `json:"by_ca"` // the monitoring CA signed it (the CA itself: true)
	Auto    bool   `json:"auto"`  // the core renews it by itself
}

type probeCert struct {
	Name      string `json:"name"`
	Site      string `json:"site"`
	Ends      int64  `json:"ends"`       // 0 = not known (a probe that doesn't report it, enrolled before Argus kept it)
	RenewedAt int64  `json:"renewed_at"` // when Argus last signed it a new one, 0 = never
	Auto      bool   `json:"auto"`       // its image renews it by itself
	Version   string `json:"version"`
}

type certificatesView struct {
	// Core is what the core VM's argus-cert-renew last reported (At = 0: no report, e.g. Argus on a
	// machine without the host tools).
	Core struct {
		At    int64      `json:"at"`
		Certs []coreCert `json:"certs"`
	} `json:"core"`
	// CA is the monitoring CA Argus signs probe certificates with (null: none mounted).
	CA *struct {
		Subject   string `json:"subject"`
		Ends      int64  `json:"ends"`
		SHA256    string `json:"sha256"`
		RenewedAt int64  `json:"renewed_at"` // the last renewal Argus saw, 0 = none
	} `json:"ca"`
	Probes []probeCert `json:"probes"`
	// Days before the end a certificate shows as a warning, and as an error.
	WarnDays  int `json:"warn_days"`
	ErrorDays int `json:"error_days"`
}

func (s *Server) handleCertificates(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()
	var v certificatesView
	v.WarnDays, v.ErrorDays = 21, 7
	v.Core.Certs = []coreCert{}
	if s.cfg.UpdateDir != "" {
		var rep struct {
			At    int64      `json:"at"`
			Certs []coreCert `json:"certs"`
		}
		if ok, err := readUpdateJSON(s.updatePath(coreCertsFile), &rep); ok && err == nil {
			v.Core.At = rep.At
			if rep.Certs != nil {
				v.Core.Certs = rep.Certs
			}
		}
	}
	if s.ca != nil {
		v.CA = &struct {
			Subject   string `json:"subject"`
			Ends      int64  `json:"ends"`
			SHA256    string `json:"sha256"`
			RenewedAt int64  `json:"renewed_at"`
		}{Subject: "CN=" + s.ca.SubjectCN(), Ends: s.ca.NotAfter().Unix(), SHA256: s.ca.Fingerprint()}
		if fp, _, ok := s.metaCA(ctx, caRenewedKey); ok && fp == v.CA.SHA256 {
			if raw, ok, _ := s.st.MetaGet(ctx, caRenewedAtKey); ok {
				v.CA.RenewedAt = atoi64(raw)
			}
		}
	}
	v.Probes = []probeCert{}
	if agents, err := s.st.ProbeAgents(ctx); err == nil {
		for name, ag := range agents {
			v.Probes = append(v.Probes, probeCert{Name: name, Site: probeSite(name), Ends: ag.CertEnd, RenewedAt: ag.CertRenewedAt,
				Version: ag.Version, Auto: ag.Version != "" && !probeVersionLess(ag.Version, probeCertRenewSince)})
		}
	}
	sort.Slice(v.Probes, func(i, j int) bool { return v.Probes[i].Site < v.Probes[j].Site })
	writeJSON(w, http.StatusOK, v)
}
