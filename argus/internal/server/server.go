// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"argus/internal/auth"
	"argus/internal/config"
	"argus/internal/mfa"
	"argus/internal/pki"
	"argus/internal/ratelimit"
	"argus/internal/settings"
	"argus/internal/store"
	"argus/internal/zabbix"
	"argus/web"

	"github.com/go-webauthn/webauthn/webauthn"
)

const mfaChallengeTTL = 10 * time.Minute

type Server struct {
	cfg           config.Config
	zbx           *zabbix.Client
	st            *store.Store
	logger        *slog.Logger
	mgr           *settings.Manager  // runtime-editable settings (Zabbix conn, public URL, tz, limits)
	dummyHash     string             // for constant-ish login timing when a user doesn't exist
	wa            *webauthn.WebAuthn // nil when passkeys are not configured
	signingSecret string             // HMAC secret for signed alert links
	loginLimiter  *ratelimit.Limiter // brute-force protection for login (owned by mgr)
	pkLimiter     *ratelimit.Limiter // anonymous passkey-login begins per address (each stores a ceremony)
	featMu        sync.Mutex         // guards the cached public /api/features answer
	featResetOK   bool
	featResetAt   time.Time
	ca            *pki.CA           // nil when probe enrollment is not configured
	probeLatest   *probeLatestCache // newest published probe version, polled from public GHCR
	updaterLatest *probeLatestCache // newest published argus-updater version, polled from public GHCR
	appLatest     *appLatestCache   // newest published app release, polled from public GHCR
	probeVM       *probeVMCache     // newest probe-vm appliance + assets, polled from GitHub Releases
	digests       digestCache       // tag -> digest, handed out with every update so the updater can verify the pull
	census        *censusCache      // the sensor census, kept warm in memory (census.go)
	hb            heartbeat         // the outside monitor's ping (heartbeat.go)
	hostGroups    hostGroupsCache   // host -> group names, for per-site visibility (scope.go)
	maint         maintCache        // the hosts in a maintenance window right now (maintenance.go)
	mux           *http.ServeMux    // the routes, so the change log can match a request before it runs (changes.go)
}

func New(cfg config.Config, zbx *zabbix.Client, st *store.Store, logger *slog.Logger, mgr *settings.Manager) http.Handler {
	dummy, _ := auth.HashPassword("argus-nonexistent-user")
	s := &Server{cfg: cfg, zbx: zbx, st: st, logger: logger, mgr: mgr, dummyHash: dummy,
		signingSecret: GetSigningSecret(context.Background(), st),
		loginLimiter:  mgr.Limiter(), pkLimiter: ratelimit.New(30, 5*time.Minute), probeLatest: &probeLatestCache{}, updaterLatest: &probeLatestCache{}, appLatest: &appLatestCache{}, probeVM: &probeVMCache{}}
	// The sensor census behind the pills, the Overview and the status pages, built in the
	// background and served from memory.
	s.census = newCensusCache(s.buildCensus)
	s.startCensusRefresh(context.Background())
	// Size each probe's Zabbix process counts from its own load (autoscale.go).
	s.startProcAutoscale(context.Background())
	// Poll public GHCR for the newest probe revision so the fleet view can flag "-rN available"
	// even when the target is "latest". Background; a failure just leaves it unknown.
	s.startProbeLatestRefresh(context.Background())
	// Same for the argus-updater sidecar, so the Probes view can flag updater drift.
	s.startUpdaterLatestRefresh(context.Background())
	// Same for the app image, so the UI can show whether this instance is on the newest release.
	s.startAppLatestRefresh(context.Background())
	// Resolve the newest probe-vm appliance + its OVA/qcow2/VHD assets from GitHub Releases, so the
	// Add-probe wizard can offer direct downloads instead of sending the user to GitHub.
	s.startProbeVMRefresh(context.Background())
	// System notices (updates available, self-update results, finished scans, ...) for the channels
	// that opted in.
	s.startNotices(context.Background())
	// Ping the outside monitor once a minute while Argus is healthy (heartbeat.go).
	s.startHeartbeat(context.Background())
	// Mirror the stored core reboot window to the shared update dir so the host reboot timer sees it
	// even if the setting is never touched after this boot (DESIGN §14c). Best-effort.
	s.syncRebootWindowFile(context.Background())
	s.syncZbxWindowFile(context.Background())
	s.syncTimezoneFile(context.Background())
	// Import the device-class templates into Zabbix (§C). Background + idempotent; soft-skips
	// until a Zabbix token is configured, and the create path re-checks before it needs them.
	s.startTemplateReconcile(context.Background())

	// Probe enrollment is available only when the CA is mounted. A load failure disables it
	// (logged) rather than blocking startup.
	if cfg.CACertFile != "" || cfg.CAKeyFile != "" {
		if ca, err := pki.Load(cfg.CACertFile, cfg.CAKeyFile); err != nil {
			logger.Error("probe enrollment disabled: could not load CA", "err", err)
		} else {
			s.ca = ca
			logger.Info("probe enrollment enabled", "ca_subject", ca.SubjectCN())
		}
	}

	if cfg.PasskeysEnabled() {
		wa, err := webauthn.New(&webauthn.Config{
			RPID:          cfg.RPID,
			RPDisplayName: cfg.RPDisplayName,
			RPOrigins:     cfg.RPOrigins,
		})
		if err != nil {
			logger.Error("webauthn init failed; passkeys disabled", "err", err)
		} else {
			s.wa = wa
			logger.Info("passkeys enabled", "rp_id", cfg.RPID, "origins", cfg.RPOrigins)
		}
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.handleHealthz)
	// The detailed health (Zabbix version, its connection error text) is for the admin Settings
	// view; load balancers and uptime monitors get /healthz, which says only "ok".
	mux.HandleFunc("GET /api/health", auth.RequireRole("admin", s.handleAPIHealth))
	mux.HandleFunc("GET /api/features", s.handleFeatures)
	mux.HandleFunc("POST /api/login", s.handleLogin)
	mux.HandleFunc("POST /api/logout", s.handleLogout)
	// self-service password reset (public; email-delivered single-use token)
	mux.HandleFunc("POST /api/password-reset/request", s.handleRequestPasswordReset)
	mux.HandleFunc("POST /api/password-reset/confirm", s.handleConfirmPasswordReset)
	// probe enrollment (public; authenticated by a single-use enrollment token)
	mux.HandleFunc("POST /api/enroll", s.handleEnroll)
	// probe VM break-glass credential report (public; authenticated by the probe token, like check-in)
	mux.HandleFunc("POST /api/probes/break-glass", s.handleReportBreakGlass)
	// probe VM OS patch status report (public; authenticated by the probe token, like check-in). See osstatus.go.
	mux.HandleFunc("POST /api/probes/os-status", s.handleReportProbeOSStatus)
	// probe fleet check-in (public; authenticated by the long-lived probe token from enrollment)
	mux.HandleFunc("POST /api/probes/checkin", s.handleProbeCheckin)
	// network-scan results a probe posts back after check-in handed it a discovery job (public;
	// probe-token authenticated, own 2 MB body cap). See netdiscovery.go.
	mux.HandleFunc("POST /api/probes/scan-results", s.handleScanResults)
	// signed one-click acknowledge link from notifications (public; HMAC-verified, GET confirms)
	mux.HandleFunc("GET /api/alert/ack", s.handleAlertAck)
	mux.HandleFunc("POST /api/alert/ack", s.handleAlertAck)
	// push sensors: a job reports a run at its own URL (public; the token is the credential), and the
	// host's template reads them back through its proxy (public; the host's key). See push.go.
	mux.HandleFunc("GET /api/push/{token}", s.handlePushIngest)
	mux.HandleFunc("POST /api/push/{token}", s.handlePushIngest)
	mux.HandleFunc("GET /api/push/host/{id}", s.handlePushPoll)
	mux.HandleFunc("POST /api/login/totp", s.handleLoginTOTP)
	mux.HandleFunc("POST /api/login/passkey/begin", s.handlePasskeyLoginBegin)
	mux.HandleFunc("POST /api/login/passkey/finish", s.handlePasskeyLoginFinish)
	mux.HandleFunc("GET /api/version", auth.RequireAuth(s.handleVersion))
	mux.HandleFunc("GET /api/version/notes", auth.RequireAuth(s.handleVersionNotes))
	mux.HandleFunc("POST /api/version/check", auth.RequireRole("admin", s.handleVersionCheck))
	mux.HandleFunc("POST /api/probes/check-updates", auth.RequireRole("admin", s.handleProbesCheckUpdates))
	mux.HandleFunc("GET /api/version/tags", auth.RequireAuth(s.handleVersionTags))
	mux.HandleFunc("GET /api/me", auth.RequireAuth(s.handleMe))
	mux.HandleFunc("POST /api/me/password", auth.RequireAuth(s.handleChangeOwnPassword))
	mux.HandleFunc("POST /api/me/preferences", auth.RequireAuth(s.handleUpdatePreferences))
	// Personal (per-user) alert channels: a user's own Telegram/Discord, self-managed from Account.
	mux.HandleFunc("GET /api/me/notify/channels", auth.RequireAuth(s.handleListMyChannels))
	mux.HandleFunc("POST /api/me/notify/channels", auth.RequireAuth(s.handleCreateMyChannel))
	mux.HandleFunc("PATCH /api/me/notify/channels/{id}", auth.RequireAuth(s.handleUpdateMyChannel))
	mux.HandleFunc("POST /api/me/notify/channels/{id}/enabled", auth.RequireAuth(s.handleSetMyChannelEnabled))
	mux.HandleFunc("POST /api/me/notify/channels/{id}/test", auth.RequireAuth(s.handleTestMyChannel))
	mux.HandleFunc("DELETE /api/me/notify/channels/{id}", auth.RequireAuth(s.handleDeleteMyChannel))
	mux.HandleFunc("GET /api/me/notify/sites", auth.RequireAuth(s.handleNotifySites))

	// monitoring read path (any signed-in user)
	mux.HandleFunc("GET /api/problems", auth.RequireAuth(s.handleProblems))
	mux.HandleFunc("GET /api/sensors", auth.RequireAuth(s.handleSensors))
	mux.HandleFunc("GET /api/census", auth.RequireAuth(s.handleCensus))
	mux.HandleFunc("GET /api/search", auth.RequireAuth(s.handleSearch))
	mux.HandleFunc("GET /api/triggers", auth.RequireAuth(s.handleTriggers))
	mux.HandleFunc("GET /api/spark", auth.RequireAuth(s.handleSpark))
	mux.HandleFunc("GET /api/daily", auth.RequireAuth(s.handleDaily))
	mux.HandleFunc("GET /api/proxies", auth.RequireAuth(s.handleProxies))
	mux.HandleFunc("GET /api/hosts", auth.RequireAuth(s.handleHosts))
	// device classes (§C): read the catalog (any user); create a host from a class (admin only).
	mux.HandleFunc("GET /api/classes", auth.RequireAuth(s.handleClasses))
	mux.HandleFunc("POST /api/hosts", auth.RequireRole("admin", s.handleCreateHost))
	mux.HandleFunc("GET /api/hosts/{id}/items", auth.RequireAuth(s.scopedHost(s.handleHostItems)))
	mux.HandleFunc("GET /api/hosts/{id}/problems", auth.RequireAuth(s.scopedHost(s.handleHostProblems)))
	mux.HandleFunc("GET /api/items/{id}/history", auth.RequireAuth(s.scopedItem(s.handleItemHistory)))
	mux.HandleFunc("GET /api/items/{id}/availability", auth.RequireAuth(s.scopedItem(s.handleItemAvailability)))
	mux.HandleFunc("GET /api/hosts/{id}/availability", auth.RequireAuth(s.scopedHost(s.handleHostAvailability)))
	mux.HandleFunc("GET /api/hosts/{id}/incidents", auth.RequireAuth(s.scopedHost(s.handleHostIncidents)))
	mux.HandleFunc("GET /api/incidents", auth.RequireAuth(s.handleIncidents))
	// the change log: who changed what (admin), and one host's changes (any user who sees the host)
	mux.HandleFunc("GET /api/changes", auth.RequireRole("admin", s.handleChanges))
	mux.HandleFunc("GET /api/hosts/{id}/changes", auth.RequireAuth(s.scopedHost(s.handleHostChanges)))
	// tags: read (any user), manage (admin); a probe's tags reach every host it monitors (tags.go)
	mux.HandleFunc("GET /api/tags", auth.RequireAuth(s.handleTags))
	mux.HandleFunc("POST /api/tags", auth.RequireRole("admin", s.handleCreateTag))
	mux.HandleFunc("PATCH /api/tags/{name}", auth.RequireRole("admin", s.handleUpdateTag))
	mux.HandleFunc("DELETE /api/tags/{name}", auth.RequireRole("admin", s.handleDeleteTag))
	mux.HandleFunc("PUT /api/proxies/{id}/tags", auth.RequireRole("admin", s.handleSetProbeTags))
	// bulk actions: one action on the tree's selected hosts, or a problem list's selected sensors (bulk.go)
	mux.HandleFunc("POST /api/bulk/hosts", auth.RequireRoles(s.handleBulkHosts, "admin", "helpdesk"))
	mux.HandleFunc("POST /api/bulk/sensors", auth.RequireAuth(s.handleBulkSensors))
	// states: acknowledge (any user); pause = Zabbix enable/disable, hide = Argus suppression
	// (both helpdesk/admin)
	mux.HandleFunc("POST /api/events/{id}/ack", auth.RequireAuth(s.scopedEvent(s.handleAckEvent)))
	mux.HandleFunc("DELETE /api/events/{id}/ack", auth.RequireAuth(s.scopedEvent(s.handleUnackEvent)))
	mux.HandleFunc("POST /api/hosts/{id}/pause", auth.RequireRoles(s.scopedHost(s.zbxEnableHandler("host", false)), "admin", "helpdesk"))
	mux.HandleFunc("DELETE /api/hosts/{id}/pause", auth.RequireRoles(s.scopedHost(s.zbxEnableHandler("host", true)), "admin", "helpdesk"))
	mux.HandleFunc("POST /api/items/{id}/pause", auth.RequireRoles(s.scopedItem(s.zbxEnableHandler("item", false)), "admin", "helpdesk"))
	mux.HandleFunc("DELETE /api/items/{id}/pause", auth.RequireRoles(s.scopedItem(s.zbxEnableHandler("item", true)), "admin", "helpdesk"))
	mux.HandleFunc("POST /api/items/{id}/mute", auth.RequireRoles(s.scopedItem(s.muteHandler(true)), "admin", "helpdesk"))
	mux.HandleFunc("DELETE /api/items/{id}/mute", auth.RequireRoles(s.scopedItem(s.muteHandler(false)), "admin", "helpdesk"))
	mux.HandleFunc("POST /api/hosts/{id}/hide", auth.RequireRoles(s.scopedHost(s.hideHandler("host")), "admin", "helpdesk"))
	mux.HandleFunc("DELETE /api/hosts/{id}/hide", auth.RequireRoles(s.scopedHost(s.unhideHandler("host")), "admin", "helpdesk"))
	mux.HandleFunc("POST /api/items/{id}/hide", auth.RequireRoles(s.scopedItem(s.hideHandler("item")), "admin", "helpdesk"))
	mux.HandleFunc("DELETE /api/items/{id}/hide", auth.RequireRoles(s.scopedItem(s.unhideHandler("item")), "admin", "helpdesk"))
	// A note on a sensor in trouble, shown and sent with it until it is OK again (sensornotes.go).
	mux.HandleFunc("PUT /api/sensors/{key}/note", auth.RequireRoles(s.handleSetSensorNote, "admin", "helpdesk"))
	mux.HandleFunc("DELETE /api/sensors/{key}/note", auth.RequireRoles(s.handleClearSensorNote, "admin", "helpdesk"))
	mux.HandleFunc("POST /api/items/{id}/priority", auth.RequireRoles(s.scopedItem(s.handleItemPriority), "admin", "helpdesk"))

	// tree groups (Zabbix host groups): list is read-only; create/rename/delete + host membership
	// are config writes, so helpdesk/admin only.
	// maintenance windows: when some hosts' alerts are held (list: any user, their sites' windows)
	mux.HandleFunc("GET /api/maintenance", auth.RequireAuth(s.handleListMaintenance))
	mux.HandleFunc("POST /api/maintenance", auth.RequireRoles(s.handleCreateMaintenance, "admin", "helpdesk"))
	mux.HandleFunc("PATCH /api/maintenance/{id}", auth.RequireRoles(s.handleUpdateMaintenance, "admin", "helpdesk"))
	mux.HandleFunc("DELETE /api/maintenance/{id}", auth.RequireRoles(s.handleDeleteMaintenance, "admin", "helpdesk"))
	mux.HandleFunc("GET /api/groups", auth.RequireAuth(s.handleGroups))
	mux.HandleFunc("POST /api/groups", auth.RequireRoles(s.handleCreateGroup, "admin", "helpdesk"))
	mux.HandleFunc("PATCH /api/groups/{id}", auth.RequireRoles(s.handleRenameGroup, "admin", "helpdesk"))
	mux.HandleFunc("DELETE /api/groups/{id}", auth.RequireRoles(s.handleDeleteGroup, "admin", "helpdesk"))
	mux.HandleFunc("GET /api/tree/order", auth.RequireAuth(s.handleTreeOrder))
	mux.HandleFunc("PUT /api/tree/order", auth.RequireRoles(s.handleSetTreeOrder, "admin", "helpdesk"))
	mux.HandleFunc("GET /api/tree/hidden", auth.RequireAuth(s.handleHiddenGroups))
	mux.HandleFunc("PUT /api/tree/hidden", auth.RequireRoles(s.handleSetHiddenGroup, "admin"))
	mux.HandleFunc("POST /api/hosts/{id}/groups", auth.RequireRoles(s.scopedHost(s.handleSetHostGroups), "admin", "helpdesk"))

	// host settings editor: read the identity + interfaces (any user), reconcile-save them (config write).
	mux.HandleFunc("GET /api/hosts/{id}/config", auth.RequireAuth(s.scopedHost(s.handleHostConfig)))
	mux.HandleFunc("PATCH /api/hosts/{id}/config", auth.RequireRoles(s.scopedHost(s.handleUpdateHostConfig), "admin", "helpdesk"))
	mux.HandleFunc("GET /api/hosts/{id}/push", auth.RequireAuth(s.scopedHost(s.handleListPush)))
	mux.HandleFunc("POST /api/hosts/{id}/push", auth.RequireRoles(s.scopedHost(s.handleCreatePush), "admin", "helpdesk"))
	mux.HandleFunc("PATCH /api/push-sensors/{id}", auth.RequireRoles(s.handleUpdatePush, "admin", "helpdesk"))
	mux.HandleFunc("POST /api/push-sensors/{id}/rotate", auth.RequireRoles(s.handleRotatePush, "admin", "helpdesk"))
	mux.HandleFunc("DELETE /api/push-sensors/{id}", auth.RequireRoles(s.handleDeletePush, "admin", "helpdesk"))
	mux.HandleFunc("POST /api/hosts/{id}/class", auth.RequireRole("admin", s.handleChangeHostClass))
	mux.HandleFunc("POST /api/hosts/{id}/proxy", auth.RequireRoles(s.scopedHost(s.handleSetHostProxy), "admin", "helpdesk"))
	mux.HandleFunc("POST /api/hosts/{id}/discover", auth.RequireRoles(s.scopedHost(s.handleDiscoverNow), "admin", "helpdesk"))

	// network auto-discovery (§B): queue a subnet scan on a probe, review its results, adopt or
	// ignore them (adoption itself goes through POST /api/hosts). Admin only.
	mux.HandleFunc("POST /api/discovery/jobs", auth.RequireRole("admin", s.handleCreateDiscoveryJob))
	mux.HandleFunc("GET /api/discovery/jobs", auth.RequireRole("admin", s.handleListDiscoveryJobs))
	mux.HandleFunc("GET /api/discovery/jobs/{id}", auth.RequireRole("admin", s.handleGetDiscoveryJob))
	mux.HandleFunc("DELETE /api/discovery/jobs/{id}", auth.RequireRole("admin", s.handleDeleteDiscoveryJob))
	mux.HandleFunc("POST /api/discovery/results/state", auth.RequireRole("admin", s.handleSetDiscoveryResultsState))
	// Saved UniFi controllers for the §B sweep (API key write-only; see unifictl.go).
	mux.HandleFunc("GET /api/discovery/controllers", auth.RequireRole("admin", s.handleListUniFiControllers))
	mux.HandleFunc("POST /api/discovery/certificate", auth.RequireRole("admin", s.handleCertificateViaProbe))
	mux.HandleFunc("POST /api/discovery/controllers", auth.RequireRole("admin", s.handleSaveUniFiController))
	mux.HandleFunc("DELETE /api/discovery/controllers/{id}", auth.RequireRole("admin", s.handleDeleteUniFiController))

	// §D thresholds: global threshold defaults, per class template. Per-host threshold + sensor-order
	// overrides ride the host-config GET/PATCH above. Admin-only.
	mux.HandleFunc("GET /api/thresholds", auth.RequireRole("admin", s.handleThresholds))
	mux.HandleFunc("PUT /api/thresholds/default", auth.RequireRole("admin", s.handleSetThresholdDefault))

	// per-proxy SNMP defaults (PRTG-style inheritance): read (any user), save + propagate (config write).
	mux.HandleFunc("GET /api/proxies/{id}/snmp", auth.RequireAuth(s.scopedProxy(s.handleGetProxySNMP)))
	mux.HandleFunc("PUT /api/proxies/{id}/snmp", auth.RequireRoles(s.scopedProxy(s.handleSetProxySNMP), "admin", "helpdesk"))
	mux.HandleFunc("POST /api/proxies/{id}/snmp/adopt", auth.RequireRoles(s.scopedProxy(s.handleAdoptProxySNMP), "admin", "helpdesk"))
	mux.HandleFunc("POST /api/proxies/reconcile", auth.RequireRole("admin", s.handleReconcileProxies))
	mux.HandleFunc("DELETE /api/proxies/{id}", auth.RequireRole("admin", s.handleDeleteProxy))

	// self-service MFA (any signed-in user)
	mux.HandleFunc("GET /api/me/mfa", auth.RequireAuth(s.handleMFAStatus))
	mux.HandleFunc("POST /api/me/mfa/setup", auth.RequireAuth(s.handleMFASetup))
	mux.HandleFunc("POST /api/me/mfa/enable", auth.RequireAuth(s.handleMFAEnable))
	mux.HandleFunc("POST /api/me/mfa/disable", auth.RequireAuth(s.handleMFADisable))
	mux.HandleFunc("POST /api/me/mfa/recovery-codes", auth.RequireAuth(s.handleMFARegenRecovery))

	// self-service passkeys (any signed-in user)
	mux.HandleFunc("GET /api/me/passkeys", auth.RequireAuth(s.handleListPasskeys))
	mux.HandleFunc("POST /api/me/passkeys/register/begin", auth.RequireAuth(s.handlePasskeyRegisterBegin))
	mux.HandleFunc("POST /api/me/passkeys/register/finish", auth.RequireAuth(s.handlePasskeyRegisterFinish))
	mux.HandleFunc("DELETE /api/me/passkeys/{id}", auth.RequireAuth(s.handleDeletePasskey))

	// notifications (admin only)
	mux.HandleFunc("GET /api/notify/channels", auth.RequireRole("admin", s.handleListChannels))
	mux.HandleFunc("POST /api/notify/channels", auth.RequireRole("admin", s.handleCreateChannel))
	mux.HandleFunc("PATCH /api/notify/channels/{id}", auth.RequireRole("admin", s.handleUpdateChannel))
	mux.HandleFunc("POST /api/notify/channels/{id}/enabled", auth.RequireRole("admin", s.handleSetChannelEnabled))
	mux.HandleFunc("POST /api/notify/channels/{id}/test", auth.RequireRole("admin", s.handleTestChannel))
	mux.HandleFunc("DELETE /api/notify/channels/{id}", auth.RequireRole("admin", s.handleDeleteChannel))
	mux.HandleFunc("GET /api/notify/sites", auth.RequireRole("admin", s.handleNotifySites))

	// system settings (admin only) - runtime-editable config
	mux.HandleFunc("GET /api/settings", auth.RequireRole("admin", s.handleListSettings))
	mux.HandleFunc("PATCH /api/settings", auth.RequireRole("admin", s.handleUpdateSettings))
	mux.HandleFunc("GET /api/settings/retention", auth.RequireRole("admin", s.handleGetRetention))
	mux.HandleFunc("PUT /api/settings/retention", auth.RequireRole("admin", s.handleSetRetention))
	mux.HandleFunc("GET /api/backup", auth.RequireRole("admin", s.handleGetBackup))
	mux.HandleFunc("PUT /api/backup", auth.RequireRole("admin", s.handleSetBackup))
	mux.HandleFunc("POST /api/backup/run", auth.RequireRole("admin", s.handleBackupRun))
	mux.HandleFunc("POST /api/backup/ssh-key", auth.RequireRole("admin", s.handleBackupSSHKey))
	mux.HandleFunc("GET /api/settings/heartbeat", auth.RequireRole("admin", s.handleHeartbeat))
	mux.HandleFunc("POST /api/settings/heartbeat", auth.RequireRole("admin", s.handleHeartbeatNow))

	// probe enrollment tokens (admin only)
	mux.HandleFunc("GET /api/probes/tokens", auth.RequireRole("admin", s.handleListEnrollTokens))
	mux.HandleFunc("POST /api/probes/tokens", auth.RequireRole("admin", s.handleCreateEnrollToken))
	mux.HandleFunc("DELETE /api/probes/tokens/{id}", auth.RequireRole("admin", s.handleDeleteEnrollToken))
	// build a first-boot seed ISO (label ARGUSSEED / ARGUS.ENV) for the probe VM (admin; see seed.go)
	mux.HandleFunc("POST /api/probes/seed-iso", auth.RequireRole("admin", s.handleSeedISO))
	mux.HandleFunc("GET /api/probes/vm-images", auth.RequireRole("admin", s.handleProbeVMImages))

	// probe fleet target version (admin only) - the version probes should converge on
	mux.HandleFunc("GET /api/probes/target", auth.RequireRole("admin", s.handleGetProbeTarget))
	mux.HandleFunc("POST /api/probes/{name}/procs/release", auth.RequireRole("admin", s.handleReleaseProcHolds))
	mux.HandleFunc("PUT /api/probes/target", auth.RequireRole("admin", s.handleSetProbeTarget))
	// trigger a dashboard-driven self-update for one probe (admin; probe must be socket-enabled)
	mux.HandleFunc("POST /api/probes/{name}/update", auth.RequireRole("admin", s.handleTriggerProbeUpdate))
	// trigger a self-update of the probe's argus-updater sidecar (admin; recreates itself)
	mux.HandleFunc("POST /api/probes/{name}/updater-update", auth.RequireRole("admin", s.handleTriggerUpdaterUpdate))
	// issue a check-in credential for an already-enrolled probe (admin) - turns on version reporting
	mux.HandleFunc("POST /api/probes/{name}/checkin-token", auth.RequireRole("admin", s.handleIssueCheckinToken))
	// reveal a probe VM's break-glass console credential (admin; decrypted on demand)
	mux.HandleFunc("GET /api/probes/{name}/break-glass", auth.RequireRole("admin", s.handleRevealBreakGlass))

	// one-click core self-update via the argus-updater sidecar (admin triggers; anyone signed in can
	// read the state, since the banner is shown in the shell). See coreupdate.go.
	mux.HandleFunc("POST /api/update/start", auth.RequireRole("admin", s.handleUpdateStart))
	mux.HandleFunc("GET /api/update/state", auth.RequireAuth(s.handleUpdateState))
	mux.HandleFunc("POST /api/update/dismiss", auth.RequireRole("admin", s.handleUpdateDismiss))
	// update the core's argus-updater sidecar itself (admin)
	mux.HandleFunc("POST /api/update/updater", auth.RequireRole("admin", s.handleUpdaterSelfUpdate))
	mux.HandleFunc("POST /api/update/updater/dismiss", auth.RequireRole("admin", s.handleUpdaterDismiss))
	mux.HandleFunc("POST /api/update/updater/check", auth.RequireRole("admin", s.handleUpdaterCheck))

	// OS patching & lifecycle (DESIGN §14c): the core's own patch status + the fleet reboot rollup, plus
	// the operator-scheduled core reboot window (admin sets it; a host timer honours it locally).
	mux.HandleFunc("GET /api/os/status", auth.RequireAuth(s.handleOSStatus))
	mux.HandleFunc("PUT /api/os/reboot-window", auth.RequireRole("admin", s.handleSetRebootWindow))
	mux.HandleFunc("PUT /api/os/zbx-window", auth.RequireRole("admin", s.handleSetZbxWindow))

	// user management (admin only)
	mux.HandleFunc("GET /api/users", auth.RequireRole("admin", s.handleListUsers))
	mux.HandleFunc("POST /api/users", auth.RequireRole("admin", s.handleCreateUser))
	mux.HandleFunc("PATCH /api/users/{id}", auth.RequireRole("admin", s.handleUpdateUser))
	mux.HandleFunc("POST /api/users/{id}/disabled", auth.RequireRole("admin", s.handleSetUserDisabled))
	mux.HandleFunc("DELETE /api/users/{id}", auth.RequireRole("admin", s.handleDeleteUser))
	mux.HandleFunc("POST /api/users/{id}/password", auth.RequireRole("admin", s.handleResetPassword))
	mux.HandleFunc("POST /api/users/{id}/mfa/reset", auth.RequireRole("admin", s.handleAdminResetMFA))
	mux.HandleFunc("POST /api/users/{id}/passkeys/reset", auth.RequireRole("admin", s.handleAdminResetPasskeys))

	// Status pages (a wall screen's read-only dashboard, opened with a secret link instead of a login):
	// the public link + page + its data, and the admin API that manages them. See statuspage.go.
	mux.HandleFunc("GET /status/{token}", s.handleStatusLink)
	mux.HandleFunc("GET /status", s.handleStatusPage)
	mux.HandleFunc("GET /status/data", s.handleStatusData)
	mux.HandleFunc("GET /api/status-pages", auth.RequireRole("admin", s.handleListStatusPages))
	mux.HandleFunc("POST /api/status-pages", auth.RequireRole("admin", s.handleCreateStatusPage))
	mux.HandleFunc("PUT /api/status-pages/{id}/note", auth.RequireRole("admin", s.handleSetStatusNote))
	mux.HandleFunc("DELETE /api/status-pages/{id}/note", auth.RequireRole("admin", s.handleClearStatusNote))
	mux.HandleFunc("PATCH /api/status-pages/{id}", auth.RequireRole("admin", s.handleUpdateStatusPage))
	mux.HandleFunc("POST /api/status-pages/{id}/rotate", auth.RequireRole("admin", s.handleRotateStatusPage))
	mux.HandleFunc("GET /api/status-pages/{id}/link", auth.RequireRole("admin", s.handleStatusPageLink))
	mux.HandleFunc("DELETE /api/status-pages/{id}", auth.RequireRole("admin", s.handleDeleteStatusPage))

	mux.Handle("/", spaHandler())
	s.mux = mux

	if !cfg.CookieSecureSet && !s.cookieSecure() {
		logger.Info("session cookies are not marked Secure (no https Public URL and ARGUS_COOKIE_SECURE unset); fine for plain-HTTP LAN use, set ARGUS_COOKIE_SECURE=true behind TLS")
	} else if cfg.CookieSecureSet && !cfg.CookieSecure && strings.HasPrefix(strings.ToLower(mgr.PublicURL()), "https://") {
		logger.Warn("ARGUS_COOKIE_SECURE=false while the Public URL is https: session cookies would also travel over plain HTTP")
	}

	// Every request gets the security headers and passes the cross-site check (plus the
	// allowed-hosts check once configured), then session resolution (idle timeout read live).
	return securityHeaders(s.hostGuard(auth.Middleware(s.st, s.mgr.SessionIdleTimeout, s.mgr.SessionMaxLifetime)(s.censusInvalidator(s.changeLog(mux)))))
}

// cookieSecure says whether session cookies carry the Secure flag: ARGUS_COOKIE_SECURE when given,
// else whatever the Public URL's scheme implies, so an https deployment doesn't depend on the
// operator remembering a second switch.
func (s *Server) cookieSecure() bool {
	if s.cfg.CookieSecureSet {
		return s.cfg.CookieSecure
	}
	return strings.HasPrefix(strings.ToLower(s.mgr.PublicURL()), "https://")
}

// spaCSP is the Content-Security-Policy of the app itself: only its own scripts (no inline ones,
// which is why the theme bootstrap is a file), styles from itself plus React's inline style props,
// images from itself and data:/blob: (chart PNGs, the TOTP QR code, downloads), and no framing.
// The status pages set their own in statuspage.go.
const spaCSP = "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data: blob:; " +
	"connect-src 'self'; font-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'; object-src 'none'"

// securityHeaders adds the response headers every page and API answer should carry. HSTS is left
// to the TLS-terminating proxy, which is the only party that knows TLS is in use.
func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "same-origin")
		h.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=(), payment=(), usb=()")
		switch {
		case strings.HasPrefix(r.URL.Path, "/api/"):
			// Authenticated JSON is never worth caching, and some of it (credentials, links) must not be.
			h.Set("Cache-Control", "no-store")
		case strings.HasPrefix(r.URL.Path, "/status"):
			// statusHeaders sets the status pages' own policy.
		default:
			h.Set("Content-Security-Policy", spaCSP)
		}
		next.ServeHTTP(w, r)
	})
}

// --- health ---

func (s *Server) handleHealthz(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok"))
}

type healthResponse struct {
	Status string       `json:"status"`
	Zabbix zabbixHealth `json:"zabbix"`
}

type zabbixHealth struct {
	Reachable bool   `json:"reachable"`
	Version   string `json:"version,omitempty"`
	Error     string `json:"error,omitempty"`
}

func (s *Server) handleAPIHealth(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()

	resp := healthResponse{Status: "ok"}
	if ver, err := s.zbx.APIVersion(ctx); err != nil {
		resp.Zabbix = zabbixHealth{Reachable: false, Error: err.Error()}
	} else {
		resp.Zabbix = zabbixHealth{Reachable: true, Version: ver}
	}
	writeJSON(w, http.StatusOK, resp)
}

// handleFeatures advertises optional capabilities so the UI can adapt (public).
func (s *Server) handleFeatures(w http.ResponseWriter, r *http.Request) {
	// Self-service password reset needs an email channel to deliver the link.
	resetReady := s.passwordResetReady(r.Context())
	// The clock settings are for the app's header clock (install-wide, so the same for everyone).
	writeJSON(w, http.StatusOK, map[string]any{"passkeys": s.wa != nil, "password_reset": resetReady, "probe_enroll": s.ca != nil,
		"clock_24h": s.mgr.Clock24h(), "timezone": s.mgr.Location().String()})
}

// passwordResetReady answers the public /api/features flag from a short cache: the endpoint is
// unauthenticated, and the channel list needn't be re-read on every page load.
func (s *Server) passwordResetReady(ctx context.Context) bool {
	s.featMu.Lock()
	defer s.featMu.Unlock()
	if time.Since(s.featResetAt) < 30*time.Second {
		return s.featResetOK
	}
	s.featResetOK = s.firstEmailChannel(ctx) != nil
	s.featResetAt = time.Now()
	return s.featResetOK
}

// --- auth ---

type loginRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

type userResponse struct {
	Email      string    `json:"email"`
	Name       string    `json:"name"`
	Surname    string    `json:"surname"`
	Role       string    `json:"role"`
	MFAEnabled bool      `json:"mfa_enabled"`
	Landing    string    `json:"landing"`
	Advanced   bool      `json:"advanced"`
	Sites      []string  `json:"sites"` // the sites this account sees; empty = every site (scope.go)
	Quiet      quietView `json:"quiet"`
}

// quietView is a user's quiet hours (start/end in minutes after midnight, -1 = off).
type quietView struct {
	Start int `json:"start"`
	End   int `json:"end"`
	Floor int `json:"floor"`
}

func toUserResponse(u *store.User) userResponse {
	sites := []string{}
	if sc := scopeOf(u); !sc.all {
		sites = sc.sites
	}
	return userResponse{Email: u.Email, Name: u.Name, Surname: u.Surname, Role: u.Role, MFAEnabled: u.TOTPEnabled, Landing: normalizeLanding(u.Landing), Advanced: u.Advanced, Sites: sites,
		Quiet: quietView{Start: u.QuietStart, End: u.QuietEnd, Floor: u.QuietFloor}}
}

// normalizeLanding coerces a stored landing value to a known option (defensive against a blank
// column on a pre-migration row).
func normalizeLanding(v string) string {
	if v == "errors" {
		return "errors"
	}
	return "overview"
}

// clientIP returns the caller's IP. Behind trusted reverse proxies (Settings -> Trusted proxies,
// ARGUS_TRUST_PROXY) it's read from X-Forwarded-For from the right, past the proxies' own addresses;
// entries further left came from the client and can be forged (they'd let anyone pick their IP to
// dodge the login rate limit or a status page's network list). Otherwise it's the socket address.
func (s *Server) clientIP(r *http.Request) string {
	return s.mgr.TrustProxy().ClientIP(r.RemoteAddr, r.Header.Values("X-Forwarded-For"))
}

// fromTrustedProxy reports whether a request's forwarded headers (host, scheme) may be believed.
func (s *Server) fromTrustedProxy(r *http.Request) bool {
	return s.mgr.TrustProxy().Trusts(r.RemoteAddr)
}

// rateBlocked returns true (and writes a 429 with Retry-After) if any key is currently throttled.
func (s *Server) rateBlocked(w http.ResponseWriter, keys ...string) bool {
	for _, k := range keys {
		if blocked, retry := s.loginLimiter.Blocked(k); blocked {
			writeThrottled(w, retry)
			return true
		}
	}
	return false
}

// writeThrottled answers 429 with a Retry-After hint.
func writeThrottled(w http.ResponseWriter, retry time.Duration) {
	secs := int(retry.Seconds())
	if secs < 1 {
		secs = 1
	}
	w.Header().Set("Retry-After", strconv.Itoa(secs))
	writeJSON(w, http.StatusTooManyRequests, map[string]string{
		"error": fmt.Sprintf("Too many attempts. Try again in about %d seconds.", secs)})
}

// errText is an error as shown to the signed-in user: admins see it whole, everyone else sees a
// Zabbix RPC error without its "data" part (SQL fragments, internal names).
func (s *Server) errText(r *http.Request, err error) string {
	if u, ok := auth.UserFrom(r.Context()); ok && u.Role == "admin" {
		return err.Error()
	}
	return zabbix.UserMessage(err)
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var req loginRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}

	ipKey := "login:ip:" + s.clientIP(r)
	acctKey := "login:acct:" + strings.ToLower(strings.TrimSpace(req.Email))
	if s.rateBlocked(w, ipKey) {
		return
	}
	// The account counter slows a guess spread over many addresses without letting anyone lock
	// a user out: while it is over the limit a wrong password is answered 429 instead of 401,
	// but the right one still signs in (and clears the counter).
	acctBlocked, acctRetry := s.loginLimiter.Blocked(acctKey)
	loginFailed := func() {
		s.loginLimiter.Fail(ipKey)
		s.loginLimiter.Fail(acctKey)
		if acctBlocked {
			writeThrottled(w, acctRetry)
			return
		}
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid credentials"})
	}

	u, err := s.st.UserByEmail(r.Context(), req.Email)
	if err != nil {
		_, _ = auth.VerifyPassword(req.Password, s.dummyHash) // equalize timing
		loginFailed()
		return
	}
	ok, err := auth.VerifyPassword(req.Password, u.PasswordHash)
	if err != nil || !ok {
		loginFailed()
		return
	}
	// Password correct - clear the failure counters for this IP and account.
	s.loginLimiter.Reset(ipKey)
	s.loginLimiter.Reset(acctKey)

	// If MFA is enabled, the password alone doesn't authenticate: issue a short-lived
	// challenge and make the client complete the second factor.
	if u.TOTPEnabled {
		raw, id, err := auth.NewSessionToken()
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
			return
		}
		if err := s.st.CreateMFAChallenge(r.Context(), id, u.ID, time.Now().Add(mfaChallengeTTL)); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"mfa_required": true, "mfa_token": raw})
		return
	}

	s.issueSession(w, r, u)
}

// handleLoginTOTP completes a login that requires a second factor.
func (s *Server) handleLoginTOTP(w http.ResponseWriter, r *http.Request) {
	var req struct {
		MFAToken string `json:"mfa_token"`
		Code     string `json:"code"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}
	challengeID := auth.HashToken(req.MFAToken)
	uid, err := s.st.MFAChallengeUserID(r.Context(), challengeID)
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "this sign-in has expired; please start again"})
		return
	}

	ipKey := "totp:ip:" + s.clientIP(r)
	userKey := "totp:uid:" + strconv.FormatInt(uid, 10)
	if s.rateBlocked(w, ipKey, userKey) {
		return
	}

	u, err := s.st.UserByID(r.Context(), uid)
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "this sign-in has expired; please start again"})
		return
	}

	// Accept either a current TOTP code or one of the user's one-time recovery codes. A TOTP code
	// works once: the time step of the last accepted code is kept, so a code seen over a shoulder
	// can't be replayed inside its validity window.
	step := time.Now().Unix() / 30
	valid := mfa.Validate(req.Code, u.TOTPSecret)
	if valid {
		if last, err := s.st.TOTPLastStep(r.Context(), u.ID); err == nil && step <= last {
			valid = false
		} else {
			_ = s.st.SetTOTPLastStep(r.Context(), u.ID, step)
		}
	}
	if !valid {
		if consumed, _ := s.st.ConsumeRecoveryCode(r.Context(), u.ID, mfa.HashRecoveryCode(req.Code)); consumed {
			valid = true
		}
	}
	if !valid {
		// Throttle code-guessing; keep the challenge alive so the user can retry in its window.
		s.loginLimiter.Fail(ipKey)
		s.loginLimiter.Fail(userKey)
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid code"})
		return
	}

	s.loginLimiter.Reset(ipKey)
	s.loginLimiter.Reset(userKey)
	// One sign-in per challenge: the row is deleted in the statement that redeems it, so two
	// completions racing with the same code yield one session.
	if ok, err := s.st.ConsumeMFAChallenge(r.Context(), challengeID); err != nil || !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "this sign-in has expired; please start again"})
		return
	}
	s.issueSession(w, r, u)
}

// issueSession creates a server-side session, sets the cookie, and returns the user.
func (s *Server) issueSession(w http.ResponseWriter, r *http.Request, u *store.User) {
	if u.Disabled {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "this account has been disabled"})
		return
	}
	raw, id, err := auth.NewSessionToken()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}
	ttl := s.mgr.SessionMaxLifetime()
	if err := s.st.CreateSession(r.Context(), id, u.ID, time.Now().Add(ttl)); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}
	auth.SetSessionCookie(w, raw, s.cookieSecure(), ttl)
	writeJSON(w, http.StatusOK, toUserResponse(u))
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if raw := auth.SessionCookie(r); raw != "" {
		_ = s.st.DeleteSession(r.Context(), auth.HashToken(raw))
	}
	auth.ClearSessionCookie(w, s.cookieSecure())
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFrom(r.Context()) // guaranteed present by RequireAuth
	writeJSON(w, http.StatusOK, toUserResponse(u))
}

// handleUpdatePreferences stores per-user UI preferences (landing view, advanced mode). Fields are
// optional pointers so a caller can update one without touching the other.
func (s *Server) handleUpdatePreferences(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFrom(r.Context()) // guaranteed present by RequireAuth
	var req struct {
		Landing  *string `json:"landing"`
		Advanced *bool   `json:"advanced"`
		// Quiet hours of the user's personal channels: minutes after midnight (Argus timezone), both
		// -1 to turn them off, and the lowest severity still sent during them.
		Quiet *struct {
			Start int `json:"start"`
			End   int `json:"end"`
			Floor int `json:"floor"`
		} `json:"quiet"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}
	if req.Landing != nil {
		if *req.Landing != "overview" && *req.Landing != "errors" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "landing must be 'overview' or 'errors'"})
			return
		}
		if err := s.st.UpdateUserLanding(r.Context(), u.ID, *req.Landing); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not save preference"})
			return
		}
		u.Landing = *req.Landing
	}
	if req.Advanced != nil {
		if err := s.st.UpdateUserAdvanced(r.Context(), u.ID, *req.Advanced); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not save preference"})
			return
		}
		u.Advanced = *req.Advanced
	}
	if q := req.Quiet; q != nil {
		off := q.Start == -1 && q.End == -1
		if !off && (q.Start < 0 || q.Start >= 24*60 || q.End < 0 || q.End >= 24*60 || q.Start == q.End) {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "quiet hours need a start and an end time that differ"})
			return
		}
		if q.Floor < 3 || q.Floor > 5 {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "choose the lowest severity still sent during quiet hours: Average, High or Disaster"})
			return
		}
		if err := s.st.SetUserQuietHours(r.Context(), u.ID, q.Start, q.End, q.Floor); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not save preference"})
			return
		}
		u.QuietStart, u.QuietEnd, u.QuietFloor = q.Start, q.End, q.Floor
	}
	writeJSON(w, http.StatusOK, toUserResponse(u))
}

// --- helpers / static ---

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func spaHandler() http.Handler {
	sub, _ := fs.Sub(web.Dist, "dist")
	fileServer := http.FileServer(http.FS(sub))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Explicit cache policy - the embedded FS carries no Last-Modified/ETag, so without headers
		// the browser is left to heuristics, and a heuristically-cached index.html keeps a tab on
		// the OLD frontend bundle after a self-update while /api/version already reports the new
		// backend (charts then silently run last build's logic). The SPA shell must always
		// revalidate; the hashed /assets/* are immutable by construction and may cache forever.
		serveIndex := func() {
			w.Header().Set("Cache-Control", "no-cache")
			http.ServeFileFS(w, r, sub, "index.html")
		}
		if r.URL.Path == "/" {
			serveIndex()
			return
		}
		name := r.URL.Path[1:]
		if _, err := fs.Stat(sub, name); err != nil {
			serveIndex()
			return
		}
		if strings.HasPrefix(name, "assets/") {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		} else {
			w.Header().Set("Cache-Control", "no-cache")
		}
		fileServer.ServeHTTP(w, r)
	})
}
