// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"time"

	"argus/internal/auth"
	"argus/internal/netscan"
	"argus/internal/provision"
	"argus/internal/store"
	"argus/internal/unifi"
)

// Network auto-discovery (§B). Two job kinds share one pipeline: the universal subnet scan
// (kind "scan") and the UniFi controller sweep (kind "unifi", which asks a saved controller for
// its adopted devices). Both piggyback on the probe check-in channel: an admin queues a job here,
// handleProbeCheckin hands it out once (takeDiscoveryHandout), the probe's scanner/sweeper POSTs
// results to /api/probes/scan-results, and the core classifies them (provision.SuggestClass /
// SuggestUniFiClass) for the Discovery review screen, where results are adopted via the ordinary
// POST /api/hosts (discovery_result_id) or ignored. Core-sourced jobs skip the channel and run
// in-process (runCoreScan / runCoreSweep). Note the namespace: /api/discovery/* - the bare
// "discover" noun (discover.go) is the per-host LLD re-fire, a different thing.

// maxScanHosts caps a scan's subnet size (a /22). The scanner enforces the same cap.
const maxScanHosts = 1024

// scanJobPayload is the one-shot job handed to the probe inside the check-in response.
type scanJobPayload struct {
	ID   int64     `json:"id"`
	CIDR string    `json:"cidr"`
	SNMP *scanSNMP `json:"snmp,omitempty"` // omitted = scan without SNMP fingerprinting
}

type scanSNMP struct {
	Version   int    `json:"version"`
	Community string `json:"community"`
	Port      int    `json:"port"`
}

// sweepJobPayload is the one-shot UniFi sweep handed to the probe inside the check-in response.
// The API key is decrypted at handout time only (the check-in channel is the same trust boundary
// that already carries the SNMP community).
type sweepJobPayload struct {
	ID  int64  `json:"id"`
	URL string `json:"url"`
	Key string `json:"key"`
}

// takeDiscoveryHandout pops the probe's oldest pending discovery job for the check-in response
// and shapes it by kind (at most one of the returns is non-nil). A sweep job whose saved
// controller is gone completes as failed on the spot, freeing the queue for the next job.
func (s *Server) takeDiscoveryHandout(ctx context.Context, proxyName string) (*scanJobPayload, *sweepJobPayload) {
	for range [3]int{} { // bounded: each dead sweep job completes and frees the next
		job, err := s.st.TakeDiscoveryJob(ctx, proxyName)
		if err != nil {
			s.logger.Warn("discovery: could not take discovery job", "proxy", proxyName, "err", err)
			return nil, nil
		}
		if job == nil {
			return nil, nil
		}
		if job.Kind == "unifi" {
			ctl, err := s.st.UniFiControllerByID(ctx, job.ControllerID)
			if err != nil || ctl.APIKey == "" {
				_ = s.st.CompleteDiscoveryJob(ctx, job.ID, proxyName,
					"the saved controller no longer exists (or has no API key) - re-add it under Discovery", nil)
				continue
			}
			s.logger.Info("discovery: sweep job dispatched", "proxy", proxyName, "job", job.ID, "controller", ctl.Name)
			return nil, &sweepJobPayload{ID: job.ID, URL: ctl.URL, Key: ctl.APIKey}
		}
		p := &scanJobPayload{ID: job.ID, CIDR: job.CIDR}
		if job.SNMPCommunity != "" {
			p.SNMP = &scanSNMP{Version: job.SNMPVersion, Community: job.SNMPCommunity, Port: job.SNMPPort}
		}
		s.logger.Info("discovery: scan job dispatched", "proxy", proxyName, "job", job.ID, "cidr", job.CIDR)
		return p, nil
	}
	return nil, nil
}

// normalizeScanCIDR validates and canonicalises the requested scan range: IPv4 only, at most a /22
// (maxScanHosts addresses), a bare IP counting as a /32. Returns a user-facing error message.
func normalizeScanCIDR(raw string) (string, string) {
	c := strings.TrimSpace(raw)
	if c == "" {
		return "", "a subnet to scan is required (e.g. 10.0.0.0/24)"
	}
	if !strings.Contains(c, "/") {
		c += "/32"
	}
	p, err := netip.ParsePrefix(c)
	if err != nil || !p.Addr().Is4() {
		return "", "enter an IPv4 subnet in CIDR form, e.g. 10.0.0.0/24"
	}
	if p.Bits() < 22 {
		return "", "that range is too large - a /22 (1024 addresses) is the maximum per scan"
	}
	return p.Masked().String(), ""
}

// POST /api/discovery/jobs (admin) - queue a subnet scan (kind "scan", the default) or a UniFi
// controller sweep (kind "unifi" + controller_id). proxy_id "" (or "0") = the core server: there
// is no check-in channel to ride, so the job is dispatched immediately to an in-process runner
// (internal/netscan / internal/unifi) instead of waiting for a probe.
func (s *Server) handleCreateDiscoveryJob(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ProxyID      string   `json:"proxy_id"`
		Kind         string   `json:"kind"` // "" / "scan" | "unifi"
		ControllerID int64    `json:"controller_id"`
		CIDR         string   `json:"cidr"`
		SNMP         *snmpReq `json:"snmp"` // explicit override; default = the probe's SNMP default
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 2048)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}
	sweep := req.Kind == "unifi"
	cidr := ""
	if !sweep {
		var msg string
		cidr, msg = normalizeScanCIDR(req.CIDR)
		if msg != "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": msg})
			return
		}
	}
	ctx, cancel := context.WithTimeout(r.Context(), 12*time.Second)
	defer cancel()

	// Core jobs are keyed by the empty proxy name - no probe resolution, no capability gate.
	coreScan := req.ProxyID == "" || req.ProxyID == "0"
	proxyName := ""
	if !coreScan {
		if !s.zbx.Authenticated() {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "Zabbix API token not configured (set ARGUS_ZABBIX_API_TOKEN)"})
			return
		}
		// Resolve the proxy: jobs are keyed by proxy NAME (the probe token identity at check-in).
		proxies, err := s.zbx.Proxies(ctx)
		if err != nil {
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": "Zabbix: " + err.Error()})
			return
		}
		for _, p := range proxies {
			if p.ProxyID == req.ProxyID {
				proxyName = p.Name
				break
			}
		}
		if proxyName == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "unknown probe"})
			return
		}
		// Only a probe that has advertised the matching capability can run the job (older images
		// never will).
		ag, err := s.st.ProbeAgentByName(ctx, proxyName)
		switch {
		case err != nil || (!sweep && !ag.Scans):
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "this probe hasn't reported the network-scan capability - it needs the latest probe image (and check-in enabled)"})
			return
		case sweep && !ag.Sweeps:
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "this probe hasn't reported the UniFi-sweep capability - it needs the latest probe image (and check-in enabled)"})
			return
		}
	}

	job := store.DiscoveryJob{ProxyName: proxyName, CIDR: cidr, SNMPVersion: 2, SNMPPort: 161}
	if sweep {
		ctl, err := s.st.UniFiControllerByID(ctx, req.ControllerID)
		if errors.Is(err, store.ErrNotFound) {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "unknown controller - pick a saved one"})
			return
		}
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not load the controller"})
			return
		}
		if ctl.APIKey == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "this controller has no API key saved - edit it and add one"})
			return
		}
		job.Kind = "unifi"
		job.ControllerID = ctl.ID
		job.ControllerName = ctl.Name
	} else {
		switch {
		case req.SNMP != nil && strings.TrimSpace(req.SNMP.Community) != "":
			job.SNMPCommunity = strings.TrimSpace(req.SNMP.Community)
			if req.SNMP.Version == 1 {
				job.SNMPVersion = 1
			}
			if p, err := strconv.Atoi(strings.TrimSpace(req.SNMP.Port)); err == nil && p > 0 && p < 65536 {
				job.SNMPPort = p
			}
		default:
			// The common case: fingerprint with the collector's own SNMP default (v1/v2c only - the
			// scanner doesn't speak v3; a v3-only site just scans without SNMP). The core server's
			// default lives under proxy id "0" (set via Probes -> Core SNMP).
			defID := req.ProxyID
			if coreScan {
				defID = "0"
			}
			if def, ok, _ := s.st.SNMPDefaultFor(ctx, defID); ok && def.Community != "" && def.Version != 3 {
				job.SNMPCommunity = def.Community
				if def.Version == 1 {
					job.SNMPVersion = 1
				}
			}
		}
	}
	if u, ok := auth.UserFrom(r.Context()); ok {
		job.RequestedBy = u.Email
	}
	id, err := s.st.CreateDiscoveryJob(ctx, job)
	if errors.Is(err, store.ErrDiscoveryBusy) {
		who := "this probe"
		if coreScan {
			who = "the core server"
		}
		writeJSON(w, http.StatusConflict, map[string]string{"error": "too many discovery jobs queued for " + who + " - wait for one to finish"})
		return
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not queue the job"})
		return
	}
	if coreScan {
		// Dispatch in-process right away (TakeDiscoveryJob flips it to dispatched and decrypts
		// the community, exactly as a check-in would).
		s.dispatchNextCoreJob(ctx)
	}
	s.logger.Info("discovery: job queued", "proxy", proxyName, "core", coreScan, "kind", req.Kind,
		"cidr", cidr, "controller", job.ControllerName, "job", id, "snmp", job.SNMPCommunity != "")
	writeJSON(w, http.StatusOK, map[string]any{"id": id, "state": "pending"})
}

// dispatchNextCoreJob pops the core's oldest pending discovery job (if none is running) and runs
// it in-process by kind. Called on job creation and after each core job finishes, so the core's
// queue drains strictly one at a time - the same cadence a probe's lock-file gives it.
func (s *Server) dispatchNextCoreJob(ctx context.Context) {
	taken, err := s.st.TakeDiscoveryJob(ctx, "")
	if err != nil || taken == nil {
		return
	}
	if taken.Kind == "unifi" {
		go s.runCoreSweep(*taken)
		return
	}
	go s.runCoreScan(*taken)
}

// runCoreScan executes a core-server scan in-process and completes the job like a probe would.
// Runs in its own goroutine with its own deadline; a wedged scan is covered by the store's
// dispatched-job expiry.
func (s *Server) runCoreScan(job store.DiscoveryJob) {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()
	var cred *netscan.SNMPCred
	if job.SNMPCommunity != "" {
		cred = &netscan.SNMPCred{Version: job.SNMPVersion, Community: job.SNMPCommunity, Port: job.SNMPPort}
	}
	hosts, partial, err := netscan.Scan(ctx, job.CIDR, cred)
	errMsg := ""
	switch {
	case err != nil:
		errMsg = err.Error()
		if len(errMsg) > 200 {
			errMsg = errMsg[:200]
		}
	case partial:
		errMsg = "scan hit the time budget - results are partial"
	}
	results := make([]store.DiscoveryResult, 0, len(hosts))
	for _, h := range hosts {
		f := provision.Fingerprint{TCP: h.TCP, DNS: h.DNS, RDNS: h.RDNS, SSHBanner: h.SSH}
		res := store.DiscoveryResult{IP: h.IP, RDNS: h.RDNS, DNS: h.DNS, SSHBanner: h.SSH}
		if h.SNMP != nil {
			res.SysDescr, res.SysObjectID, res.SysName = h.SNMP.SysDescr, h.SNMP.SysObjectID, h.SNMP.SysName
			f.SysDescr, f.SysObjectID, f.SysName = h.SNMP.SysDescr, h.SNMP.SysObjectID, h.SNMP.SysName
		}
		if h.HTTP != nil {
			if b, err := json.Marshal(h.HTTP); err == nil {
				res.HTTPJSON = string(b)
			}
			f.HTTPTitle, f.HTTPServer, f.HTTPLocation = h.HTTP.Title, h.HTTP.Server, h.HTTP.Location
		}
		ports, _ := json.Marshal(h.TCP)
		res.TCPPorts = string(ports)
		res.SuggestedClass = provision.SuggestClass(f)
		results = append(results, res)
	}
	// A fresh context: the scan one may just have expired, and the write must still land.
	sctx, scancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer scancel()
	if err := s.st.CompleteDiscoveryJob(sctx, job.ID, "", errMsg, results); err != nil {
		s.logger.Error("discovery: could not store core scan results", "job", job.ID, "err", err)
		return
	}
	s.logger.Info("discovery: core scan finished", "job", job.ID, "cidr", job.CIDR, "hosts", len(results), "note", errMsg)
	// Core jobs queue like probe jobs do; drain the next one (Take only yields once nothing is
	// dispatched, so the queue runs strictly one at a time).
	s.dispatchNextCoreJob(sctx)
}

// unifiFacts is the controller-sourced device record stored per sweep result (unifi_json) and
// carried to the review screen verbatim. Site is the API site name (the {$UNIFI.SITE} value);
// SiteDesc its display name.
type unifiFacts struct {
	Name     string `json:"name,omitempty"`
	Model    string `json:"model,omitempty"`
	Type     string `json:"type,omitempty"`
	State    int    `json:"state"`
	Version  string `json:"version,omitempty"`
	Site     string `json:"site,omitempty"`
	SiteDesc string `json:"site_desc,omitempty"`
}

// injectUniFiMacros fills the {$UNIFI.*} macros of a sweep-adopted host from the job's saved
// controller and the result's own facts, wherever the request left them blank. The review screen
// never sees the API key - it lands on the host straight from the encrypted store here.
// Best-effort: if the controller has been deleted since the sweep, the normal required-macro
// validation catches whatever stays blank.
func (s *Server) injectUniFiMacros(ctx context.Context, req *createHostRequest) {
	res, err := s.st.DiscoveryResultByID(ctx, req.DiscoveryResultID)
	if err != nil || res.UniFiJSON == "" {
		return
	}
	job, err := s.st.DiscoveryJobByID(ctx, res.JobID)
	if err != nil || job.Kind != "unifi" {
		return
	}
	var uf unifiFacts
	_ = json.Unmarshal([]byte(res.UniFiJSON), &uf)
	if req.Macros == nil {
		req.Macros = map[string]string{}
	}
	set := func(macro, value string) {
		if strings.TrimSpace(req.Macros[macro]) == "" && value != "" {
			req.Macros[macro] = value
		}
	}
	if ctl, err := s.st.UniFiControllerByID(ctx, job.ControllerID); err == nil {
		set("{$UNIFI.URL}", ctl.URL)
		set("{$UNIFI.KEY}", ctl.APIKey)
	}
	set("{$UNIFI.MAC}", res.MAC)
	set("{$UNIFI.SITE}", uf.Site)
}

// runCoreSweep executes a core-server UniFi sweep in-process and completes the job like a probe
// would. Runs in its own goroutine with its own deadline; a wedged sweep is covered by the
// store's dispatched-job expiry.
func (s *Server) runCoreSweep(job store.DiscoveryJob) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	var results []store.DiscoveryResult
	errMsg := ""
	ctl, err := s.st.UniFiControllerByID(ctx, job.ControllerID)
	if err != nil || ctl.APIKey == "" {
		errMsg = "the saved controller no longer exists (or has no API key) - re-add it under Discovery"
	} else {
		devices, err := unifi.Sweep(ctx, ctl.URL, ctl.APIKey)
		if err != nil {
			errMsg = err.Error()
			if len(errMsg) > 200 {
				errMsg = errMsg[:200]
			}
		}
		results = make([]store.DiscoveryResult, 0, len(devices))
		for _, d := range devices {
			facts, _ := json.Marshal(unifiFacts{Name: d.Name, Model: d.Model, Type: d.Type,
				State: d.State, Version: d.Version, Site: d.Site, SiteDesc: d.SiteDesc})
			results = append(results, store.DiscoveryResult{
				IP: d.IP, MAC: d.MAC, TCPPorts: "[]", UniFiJSON: string(facts),
				SuggestedClass: provision.SuggestUniFiClass(d.Type, d.Model),
			})
		}
	}
	// A fresh context: the sweep one may just have expired, and the write must still land.
	sctx, scancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer scancel()
	if err := s.st.CompleteDiscoveryJob(sctx, job.ID, "", errMsg, results); err != nil {
		s.logger.Error("discovery: could not store core sweep results", "job", job.ID, "err", err)
		return
	}
	s.logger.Info("discovery: core sweep finished", "job", job.ID, "controller", job.ControllerName, "devices", len(results), "note", errMsg)
	s.dispatchNextCoreJob(sctx)
}

type discoveryJobView struct {
	ID             int64  `json:"id"`
	ProxyName      string `json:"proxy_name"`
	Kind           string `json:"kind"` // scan | unifi
	ControllerName string `json:"controller_name,omitempty"`
	CIDR           string `json:"cidr"`
	State          string `json:"state"` // pending | dispatched | done | failed
	Error          string `json:"error,omitempty"`
	RequestedBy    string `json:"requested_by,omitempty"`
	CreatedAt      int64  `json:"created_at"`
	CompletedAt    int64  `json:"completed_at,omitempty"`
	Found          int    `json:"found"` // result counts (list only): live hosts / still up for review
	New            int    `json:"new"`
}

func jobView(j store.DiscoveryJob) discoveryJobView {
	return discoveryJobView{ID: j.ID, ProxyName: j.ProxyName, Kind: j.Kind, ControllerName: j.ControllerName,
		CIDR: j.CIDR, State: j.State,
		Error: j.Error, RequestedBy: j.RequestedBy, CreatedAt: j.CreatedAt, CompletedAt: j.CompletedAt,
		Found: j.Found, New: j.NewCount}
}

// GET /api/discovery/jobs (admin) - recent scans, newest first.
func (s *Server) handleListDiscoveryJobs(w http.ResponseWriter, r *http.Request) {
	limit := 30
	if n, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && n > 0 && n <= 100 {
		limit = n
	}
	jobs, err := s.st.ListDiscoveryJobs(r.Context(), limit)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not list scans"})
		return
	}
	out := make([]discoveryJobView, 0, len(jobs))
	for _, j := range jobs {
		out = append(out, jobView(j))
	}
	writeJSON(w, http.StatusOK, out)
}

type discoveryResultView struct {
	ID             int64           `json:"id"`
	IP             string          `json:"ip"`
	MAC            string          `json:"mac,omitempty"`
	RDNS           string          `json:"rdns,omitempty"`
	TCP            []int           `json:"tcp"`
	SysDescr       string          `json:"sysdescr,omitempty"`
	SysObjectID    string          `json:"sysobjectid,omitempty"`
	SysName        string          `json:"sysname,omitempty"`
	HTTP           json.RawMessage `json:"http,omitempty"`
	DNS            bool            `json:"dns,omitempty"`
	SSH            string          `json:"ssh,omitempty"`
	Unifi          json.RawMessage `json:"unifi,omitempty"` // controller-sourced facts (sweep results)
	SuggestedClass string          `json:"suggested_class,omitempty"`
	State          string          `json:"state"`                    // new | ignored | added
	HostID         string          `json:"host_id,omitempty"`        // the host this result was adopted as
	MonitoredID    string          `json:"monitored_id,omitempty"`   // an existing host already uses this IP
	MonitoredName  string          `json:"monitored_name,omitempty"` // its display name
}

// GET /api/discovery/jobs/{id} (admin) - one scan with its results, annotated with whether each IP
// is already monitored (checked live against Zabbix, so it stays fresh after adoptions elsewhere).
func (s *Server) handleGetDiscoveryJob(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid scan id"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 12*time.Second)
	defer cancel()
	job, err := s.st.DiscoveryJobByID(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "scan not found"})
		return
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not load the scan"})
		return
	}
	results, err := s.st.DiscoveryResultsByJob(ctx, id)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not load the results"})
		return
	}
	// Best-effort monitored-IP annotation; a Zabbix hiccup just leaves the badges off.
	ipToHost := map[string]string{}
	hostName := map[string]string{}
	if s.zbx.Authenticated() {
		if ips, err := s.zbx.HostIPs(ctx); err == nil {
			for hid, ip := range ips {
				if _, dup := ipToHost[ip]; !dup {
					ipToHost[ip] = hid
				}
			}
			if hosts, err := s.zbx.Hosts(ctx); err == nil {
				for _, h := range hosts {
					hostName[h.HostID] = h.Name
				}
			}
		}
	}
	out := make([]discoveryResultView, 0, len(results))
	for _, res := range results {
		v := discoveryResultView{ID: res.ID, IP: res.IP, MAC: res.MAC, RDNS: res.RDNS,
			SysDescr: res.SysDescr, SysObjectID: res.SysObjectID, SysName: res.SysName,
			DNS: res.DNS, SSH: res.SSHBanner, State: res.State, HostID: res.HostID}
		if json.Unmarshal([]byte(res.TCPPorts), &v.TCP) != nil || v.TCP == nil {
			v.TCP = []int{}
		}
		if res.HTTPJSON != "" {
			v.HTTP = json.RawMessage(res.HTTPJSON)
		}
		// The suggestion is recomputed from the STORED raw facts on every read (the ingest-time
		// value is kept only as a record): mapping improvements ship core-side and reach past
		// scans immediately - no re-scan needed. Sweep results carry the controller's own device
		// record, which beats any fingerprint.
		if res.UniFiJSON != "" {
			v.Unifi = json.RawMessage(res.UniFiJSON)
			var uf unifiFacts
			if json.Unmarshal([]byte(res.UniFiJSON), &uf) == nil {
				v.SuggestedClass = provision.SuggestUniFiClass(uf.Type, uf.Model)
			}
		} else {
			f := provision.Fingerprint{SysDescr: res.SysDescr, SysObjectID: res.SysObjectID,
				SysName: res.SysName, DNS: res.DNS, TCP: v.TCP, MAC: res.MAC, RDNS: res.RDNS,
				SSHBanner: res.SSHBanner}
			if res.HTTPJSON != "" {
				var hf httpFacts
				if json.Unmarshal([]byte(res.HTTPJSON), &hf) == nil {
					f.HTTPTitle, f.HTTPServer, f.HTTPLocation = hf.Title, hf.Server, hf.Location
				}
			}
			v.SuggestedClass = provision.SuggestClass(f)
		}
		mid := res.HostID
		if mid == "" {
			mid = ipToHost[res.IP]
		}
		if mid != "" {
			v.MonitoredID, v.MonitoredName = mid, hostName[mid]
		}
		out = append(out, v)
	}
	writeJSON(w, http.StatusOK, map[string]any{"job": jobView(*job), "results": out})
}

// POST /api/discovery/results/state (admin) - flip results between new and ignored. An ignored
// device stays ignored on future re-scans of the same probe (the store carries the state over).
func (s *Server) handleSetDiscoveryResultsState(w http.ResponseWriter, r *http.Request) {
	var req struct {
		IDs   []int64 `json:"ids"`
		State string  `json:"state"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64*1024)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}
	if req.State != "new" && req.State != "ignored" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": `state must be "new" or "ignored"`})
		return
	}
	if err := s.st.SetDiscoveryResultsState(r.Context(), req.IDs, req.State); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not update the results"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// scanResultHost is one host as the probe's scanner reports it (raw facts).
type scanResultHost struct {
	IP   string `json:"ip"`
	MAC  string `json:"mac"`
	RDNS string `json:"rdns"`
	TCP  []int  `json:"tcp"`
	SNMP *struct {
		SysDescr    string `json:"sysdescr"`
		SysObjectID string `json:"sysobjectid"`
		SysName     string `json:"sysname"`
	} `json:"snmp"`
	HTTP  json.RawMessage `json:"http"`
	DNS   bool            `json:"dns"`
	SSH   string          `json:"ssh"`
	Unifi json.RawMessage `json:"unifi"` // controller sweep results only
}

// httpFacts is the slice of the scanner's HTTP banner the classifier cares about.
type httpFacts struct {
	Title    string `json:"title"`
	Server   string `json:"server"`
	Location string `json:"location"`
}

// handleScanResults receives a finished scan from the probe (public; authenticated by the same
// long-lived probe token as check-in). The body can carry a whole /22's fingerprints, so its cap is
// its own (2 MB), not check-in's 2 KB.
func (s *Server) handleScanResults(w http.ResponseWriter, r *http.Request) {
	tok := bearerToken(r)
	if tok == "" {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "missing probe token"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	proxyName, err := s.st.ProbeNameByToken(ctx, auth.HashToken(tok))
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid probe token"})
		return
	}
	var req struct {
		JobID int64            `json:"job_id"`
		Error string           `json:"error"`
		Hosts []scanResultHost `json:"hosts"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 2<<20)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}
	if len(req.Hosts) > maxScanHosts {
		req.Hosts = req.Hosts[:maxScanHosts]
	}
	results := make([]store.DiscoveryResult, 0, len(req.Hosts))
	for _, h := range req.Hosts {
		if strings.TrimSpace(h.IP) == "" {
			continue
		}
		f := provision.Fingerprint{TCP: h.TCP, DNS: h.DNS, MAC: h.MAC, RDNS: h.RDNS, SSHBanner: h.SSH}
		res := store.DiscoveryResult{IP: h.IP, MAC: h.MAC, RDNS: h.RDNS, DNS: h.DNS, SSHBanner: h.SSH}
		if h.SNMP != nil {
			res.SysDescr, res.SysObjectID, res.SysName = h.SNMP.SysDescr, h.SNMP.SysObjectID, h.SNMP.SysName
			f.SysDescr, f.SysObjectID, f.SysName = h.SNMP.SysDescr, h.SNMP.SysObjectID, h.SNMP.SysName
		}
		if len(h.HTTP) > 0 {
			res.HTTPJSON = string(h.HTTP)
			var hf httpFacts
			if json.Unmarshal(h.HTTP, &hf) == nil {
				f.HTTPTitle, f.HTTPServer, f.HTTPLocation = hf.Title, hf.Server, hf.Location
			}
		}
		ports, _ := json.Marshal(h.TCP)
		res.TCPPorts = string(ports)
		if len(h.Unifi) > 0 {
			// A sweep result: the controller's device record IS the identity - no fingerprinting.
			res.UniFiJSON = string(h.Unifi)
			var uf unifiFacts
			if json.Unmarshal(h.Unifi, &uf) == nil {
				res.SuggestedClass = provision.SuggestUniFiClass(uf.Type, uf.Model)
			}
		} else {
			res.SuggestedClass = provision.SuggestClass(f)
		}
		results = append(results, res)
	}
	err = s.st.CompleteDiscoveryJob(ctx, req.JobID, proxyName, strings.TrimSpace(req.Error), results)
	if errors.Is(err, store.ErrNotFound) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown scan job"})
		return
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not store the results"})
		return
	}
	s.logger.Info("discovery: scan results received", "proxy", proxyName, "job", req.JobID, "hosts", len(results), "note", strings.TrimSpace(req.Error))
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}
