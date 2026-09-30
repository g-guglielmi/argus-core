// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package server

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"

	"argus/internal/auth"
	"argus/internal/buildinfo"
	"argus/internal/store"
)

// Backups (DESIGN section 14e). The core host backs itself up: a root timer runs argus-backup
// (deploy/core/host), which archives Argus's database, the Zabbix database and the configuration,
// keys and certificates that make up the core, keeps the newest archives on the VM and exports them
// to SMB, NFS, rsync over SSH or S3. Argus holds the plan: the schedule, how many to keep, whether
// metric history is included, the passphrase that encrypts the archives, and the remote target with
// its credentials, stored like any other secret. The host reads the plan with
// `docker exec argus /argus backup-plan` (root on the host only), reports back in backup-status.json
// in the shared update dir, and picks up backup-request.json there ("Back up now", "Test").

const (
	backupConfigKey     = "backup:config"
	backupPassphraseKey = "backup:passphrase"
	backupSMBPassKey    = "backup:smb_password"
	backupS3SecretKey   = "backup:s3_secret"
	backupSSHKey        = "backup:ssh_key"     // OpenSSH private key for rsync over SSH (secret)
	backupSSHPubKey     = "backup:ssh_pub_key" // its public half, to add on the target
	backupStatusFile    = "backup-status.json"
	backupRequestFile   = "backup-request.json"
	// backupLocalDir is where the host keeps its archives (argus-backup's default).
	backupLocalDir = "/var/backups/argus"
)

// backupRemote is where archives are exported; the secrets (SMB password, S3 secret key, SSH private
// key) are stored apart and only ever handed to the host.
type backupRemote struct {
	Type string `json:"type"` // "" (none) | smb | nfs | rsync | s3
	// SMB (a Windows share, a NAS): //server/share, a folder inside it, and the login.
	Share    string `json:"share,omitempty"`
	Username string `json:"username,omitempty"`
	Domain   string `json:"domain,omitempty"`
	Version  string `json:"version,omitempty"` // SMB dialect, "" = negotiate
	// NFS: server:/export and mount options.
	Export  string `json:"export,omitempty"`
	Options string `json:"options,omitempty"`
	// rsync over SSH: user@host:/path, with the key Argus generates.
	Target string `json:"target,omitempty"`
	Port   int    `json:"port,omitempty"`
	// S3 or compatible (MinIO, Backblaze B2, Wasabi, Cloudflare R2).
	Endpoint  string `json:"endpoint,omitempty"`
	Region    string `json:"region,omitempty"`
	Bucket    string `json:"bucket,omitempty"`
	AccessKey string `json:"access_key,omitempty"`
	// Path is the folder inside the share / export / bucket (S3: the key prefix).
	Path string `json:"path,omitempty"`
}

// backupConfig is the plan's non-secret part, as the UI edits it.
type backupConfig struct {
	Enabled bool         `json:"enabled"`
	Hour    int          `json:"hour"`   // daily at hour:minute, Argus timezone
	Minute  int          `json:"minute"` //
	Keep    int          `json:"keep"`   // archives kept, on the VM and on the remote
	History bool         `json:"history"`
	Remote  backupRemote `json:"remote"`
}

func defaultBackupConfig() backupConfig {
	return backupConfig{Enabled: false, Hour: 2, Minute: 30, Keep: 7, History: true}
}

var (
	reSMBShare  = regexp.MustCompile(`^//[A-Za-z0-9._\[\]:-]+/[A-Za-z0-9._ $-]+$`)
	reSubPath   = regexp.MustCompile(`^([A-Za-z0-9._ -]+(/[A-Za-z0-9._ -]+)*)?$`)
	reLogin     = regexp.MustCompile(`^[A-Za-z0-9._@-]{0,64}$`)
	reNFSExport = regexp.MustCompile(`^[A-Za-z0-9._\[\]:-]+:/[A-Za-z0-9._/ -]*$`)
	reNFSOpts   = regexp.MustCompile(`^[A-Za-z0-9=,._-]{0,128}$`)
	reRsync     = regexp.MustCompile(`^[A-Za-z0-9._-]{1,32}@[A-Za-z0-9._\[\]:-]+:[A-Za-z0-9._/ ~-]+$`)
	reRegion    = regexp.MustCompile(`^[a-z0-9-]{0,32}$`)
	reBucket    = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{1,61}[a-z0-9]$`)
	reAccessKey = regexp.MustCompile(`^[A-Za-z0-9/+=_-]{1,128}$`)
	smbVersions = map[string]bool{"": true, "1.0": true, "2.0": true, "2.1": true, "3.0": true, "3.02": true, "3.1.1": true}
)

// validate checks the plan; its values end up on command lines on the core host (mount, rsync,
// rclone), so each has a strict shape - and argus-backup checks them again.
func (c *backupConfig) validate() string {
	if c.Hour < 0 || c.Hour > 23 || c.Minute < 0 || c.Minute > 59 {
		return "the backup time must be a time of day"
	}
	if c.Keep < 1 || c.Keep > 90 {
		return "keep between 1 and 90 backups"
	}
	r := &c.Remote
	r.Path = strings.Trim(strings.TrimSpace(r.Path), "/")
	if !reSubPath.MatchString(r.Path) || strings.Contains(r.Path, "..") {
		return "the folder must be a plain path inside the target (letters, digits, . _ - and spaces)"
	}
	switch r.Type {
	case "":
		*r = backupRemote{}
	case "smb":
		r.Share = strings.TrimSpace(strings.ReplaceAll(r.Share, `\`, "/"))
		if !reSMBShare.MatchString(r.Share) {
			return `the share must look like //server/share`
		}
		if !reLogin.MatchString(r.Username) || !reLogin.MatchString(r.Domain) {
			return "the user name and domain may hold letters, digits and . _ @ - only"
		}
		if !smbVersions[r.Version] {
			return "unknown SMB version"
		}
		r.Export, r.Options, r.Target, r.Port, r.Endpoint, r.Region, r.Bucket, r.AccessKey = "", "", "", 0, "", "", "", ""
	case "nfs":
		if !reNFSExport.MatchString(r.Export) {
			return "the export must look like server:/path"
		}
		if !reNFSOpts.MatchString(r.Options) {
			return "mount options may hold letters, digits and = , . _ - only"
		}
		r.Share, r.Username, r.Domain, r.Version, r.Target, r.Port, r.Endpoint, r.Region, r.Bucket, r.AccessKey = "", "", "", "", "", 0, "", "", "", ""
	case "rsync":
		if !reRsync.MatchString(r.Target) {
			return "the target must look like user@server:/path"
		}
		if r.Port == 0 {
			r.Port = 22
		}
		if r.Port < 1 || r.Port > 65535 {
			return "the SSH port must be 1-65535"
		}
		r.Share, r.Username, r.Domain, r.Version, r.Export, r.Options, r.Endpoint, r.Region, r.Bucket, r.AccessKey, r.Path = "", "", "", "", "", "", "", "", "", "", ""
	case "s3":
		u, err := url.Parse(strings.TrimSpace(r.Endpoint))
		if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.User != nil || (u.Path != "" && u.Path != "/") || u.RawQuery != "" {
			return "the endpoint must be an http(s) address, like https://s3.eu-central-1.amazonaws.com"
		}
		r.Endpoint = u.Scheme + "://" + u.Host
		if !reRegion.MatchString(r.Region) {
			return "the region may hold lowercase letters, digits and - only"
		}
		if !reBucket.MatchString(r.Bucket) || strings.Contains(r.Bucket, "..") {
			return "the bucket name must be 3-63 lowercase letters, digits, dots or dashes"
		}
		if !reAccessKey.MatchString(r.AccessKey) {
			return "the access key is required"
		}
		r.Share, r.Username, r.Domain, r.Version, r.Export, r.Options, r.Target, r.Port = "", "", "", "", "", "", "", 0
	default:
		return "unknown remote type"
	}
	return ""
}

func loadBackupConfig(ctx context.Context, st *store.Store) backupConfig {
	c := defaultBackupConfig()
	if raw, ok, err := st.MetaGet(ctx, backupConfigKey); err == nil && ok {
		_ = json.Unmarshal([]byte(raw), &c)
	}
	return c
}

func hasSecret(ctx context.Context, st *store.Store, key string) bool {
	v, ok, err := st.MetaGetSecret(ctx, key)
	return err == nil && ok && v != ""
}

func getSecret(ctx context.Context, st *store.Store, key string) string {
	v, _, _ := st.MetaGetSecret(ctx, key)
	return v
}

// ensureBackupSSHKey creates the ed25519 key pair for rsync over SSH if there is none (or when
// regenerate is set) and returns its public half in authorized_keys form.
func ensureBackupSSHKey(ctx context.Context, st *store.Store, regenerate bool) (string, error) {
	if !regenerate {
		if pub, ok, _ := st.MetaGet(ctx, backupSSHPubKey); ok && pub != "" && hasSecret(ctx, st, backupSSHKey) {
			return pub, nil
		}
	}
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return "", err
	}
	block, err := ssh.MarshalPrivateKey(priv, "argus-backup")
	if err != nil {
		return "", err
	}
	sshPub, err := ssh.NewPublicKey(pub)
	if err != nil {
		return "", err
	}
	pubLine := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(sshPub))) + " argus-backup"
	if err := st.MetaSetSecret(ctx, backupSSHKey, string(pem.EncodeToMemory(block))); err != nil {
		return "", err
	}
	if err := st.MetaSet(ctx, backupSSHPubKey, pubLine); err != nil {
		return "", err
	}
	return pubLine, nil
}

// BackupPlan is what `argus backup-plan` prints for the host: the whole plan, secrets included.
type BackupPlan struct {
	Version    int          `json:"version"`
	Argus      string       `json:"argus_version"`
	Enabled    bool         `json:"enabled"`
	Hour       int          `json:"hour"`
	Minute     int          `json:"minute"`
	Timezone   string       `json:"timezone"`
	Keep       int          `json:"keep"`
	History    bool         `json:"history"`
	Passphrase string       `json:"passphrase"`
	Remote     backupRemote `json:"remote"`
	Password   string       `json:"password,omitempty"`    // SMB
	SecretKey  string       `json:"secret_key,omitempty"`  // S3
	PrivateKey string       `json:"private_key,omitempty"` // rsync over SSH
}

// LoadBackupPlan assembles the plan from the store (for the backup-plan subcommand).
func LoadBackupPlan(ctx context.Context, st *store.Store, timezone string) BackupPlan {
	c := loadBackupConfig(ctx, st)
	p := BackupPlan{Version: 1, Argus: buildinfo.Version, Enabled: c.Enabled, Hour: c.Hour, Minute: c.Minute, Timezone: timezone, Keep: c.Keep,
		History: c.History, Passphrase: getSecret(ctx, st, backupPassphraseKey), Remote: c.Remote}
	switch c.Remote.Type {
	case "smb":
		p.Password = getSecret(ctx, st, backupSMBPassKey)
	case "s3":
		p.SecretKey = getSecret(ctx, st, backupS3SecretKey)
	case "rsync":
		p.PrivateKey = getSecret(ctx, st, backupSSHKey)
	}
	return p
}

// backupStatus is what argus-backup reports (backup-status.json); Argus only reads it.
type backupStatus struct {
	Version    int   `json:"version"`
	At         int64 `json:"at"` // when the host last wrote this
	Configured bool  `json:"configured"`
	Running    bool  `json:"running,omitempty"`
	LastRun    *struct {
		At       int64  `json:"at"`
		OK       bool   `json:"ok"`
		Error    string `json:"error,omitempty"`
		Archive  string `json:"archive,omitempty"`
		Size     int64  `json:"size,omitempty"`
		Duration int64  `json:"duration_s,omitempty"`
		Trigger  string `json:"trigger,omitempty"`
		Warning  string `json:"warning,omitempty"`
	} `json:"last_run,omitempty"`
	LastOKAt int64 `json:"last_ok_at,omitempty"`
	Local    []struct {
		Name string `json:"name"`
		Size int64  `json:"size"`
		At   int64  `json:"at"`
	} `json:"local,omitempty"`
	LocalDir string `json:"local_dir,omitempty"`
	FreeDisk int64  `json:"free_bytes,omitempty"`
	Remote   *struct {
		Type  string `json:"type"`
		OK    bool   `json:"ok"`
		At    int64  `json:"at"`
		Error string `json:"error,omitempty"`
		Files int    `json:"files,omitempty"`
	} `json:"remote,omitempty"`
	Test *struct {
		At    int64  `json:"at"`
		OK    bool   `json:"ok"`
		Error string `json:"error,omitempty"`
	} `json:"test,omitempty"`
	NextDueAt int64 `json:"next_due_at,omitempty"`
}

func (s *Server) readBackupStatus() (*backupStatus, error) {
	if s.cfg.UpdateDir == "" {
		return nil, nil
	}
	var st backupStatus
	ok, err := readUpdateJSON(s.updatePath(backupStatusFile), &st)
	if err != nil || !ok {
		return nil, err
	}
	return &st, nil
}

type backupView struct {
	Config         backupConfig  `json:"config"`
	HasPassphrase  bool          `json:"has_passphrase"`
	HasSMBPassword bool          `json:"has_smb_password"`
	HasS3Secret    bool          `json:"has_s3_secret"`
	SSHPublicKey   string        `json:"ssh_public_key,omitempty"`
	LocalDir       string        `json:"local_dir"`
	Status         *backupStatus `json:"status"`
	Pending        string        `json:"pending,omitempty"` // a request the host hasn't picked up: "backup" | "test"
	Channel        bool          `json:"channel"`           // the shared update dir is configured
}

func (s *Server) backupView(ctx context.Context) backupView {
	v := backupView{Config: loadBackupConfig(ctx, s.st), LocalDir: backupLocalDir, Channel: s.cfg.UpdateDir != "",
		HasPassphrase: hasSecret(ctx, s.st, backupPassphraseKey), HasSMBPassword: hasSecret(ctx, s.st, backupSMBPassKey),
		HasS3Secret: hasSecret(ctx, s.st, backupS3SecretKey)}
	if pub, ok, _ := s.st.MetaGet(ctx, backupSSHPubKey); ok {
		v.SSHPublicKey = pub
	}
	v.Status, _ = s.readBackupStatus()
	if s.cfg.UpdateDir != "" {
		var req struct {
			Kind string `json:"kind"`
		}
		if ok, _ := readUpdateJSON(s.updatePath(backupRequestFile), &req); ok {
			v.Pending = req.Kind
		}
	}
	return v
}

// handleGetBackup serves GET /api/backup (admin).
func (s *Server) handleGetBackup(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()
	writeJSON(w, http.StatusOK, s.backupView(ctx))
}

// handleSetBackup serves PUT /api/backup (admin): the plan, and any secrets typed (blank = unchanged).
func (s *Server) handleSetBackup(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Config      backupConfig `json:"config"`
		Passphrase  string       `json:"passphrase"`
		SMBPassword string       `json:"smb_password"`
		S3Secret    string       `json:"s3_secret"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16384)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}
	c := req.Config
	if msg := c.validate(); msg != "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": msg})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	if p := req.Passphrase; p != "" && len([]rune(p)) < 12 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "the passphrase must be at least 12 characters"})
		return
	}
	hasPass := req.Passphrase != "" || hasSecret(ctx, s.st, backupPassphraseKey)
	if c.Remote.Type != "" && !hasPass {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "set an encryption passphrase first: archives leave the core only encrypted"})
		return
	}
	if c.Remote.Type == "s3" && req.S3Secret == "" && !hasSecret(ctx, s.st, backupS3SecretKey) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "the S3 secret key is required"})
		return
	}
	for key, val := range map[string]string{backupPassphraseKey: req.Passphrase, backupSMBPassKey: req.SMBPassword, backupS3SecretKey: req.S3Secret} {
		if val == "" {
			continue
		}
		if err := s.st.MetaSetSecret(ctx, key, val); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not save the backup settings"})
			return
		}
	}
	if c.Remote.Type == "rsync" {
		if _, err := ensureBackupSSHKey(ctx, s.st, false); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not create the SSH key"})
			return
		}
	}
	b, _ := json.Marshal(c)
	if err := s.st.MetaSet(ctx, backupConfigKey, string(b)); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not save the backup settings"})
		return
	}
	s.logger.Info("backup settings saved", "enabled", c.Enabled, "remote", c.Remote.Type, "keep", c.Keep)
	writeJSON(w, http.StatusOK, s.backupView(ctx))
}

// handleBackupRun serves POST /api/backup/run (admin): {"kind":"backup"} backs up now, {"kind":"test"}
// checks the remote target; the host picks the request up within seconds.
func (s *Server) handleBackupRun(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Kind string `json:"kind"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024)).Decode(&req); err != nil || (req.Kind != "backup" && req.Kind != "test") {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": `kind must be "backup" or "test"`})
		return
	}
	if s.cfg.UpdateDir == "" {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "the core's shared update folder (ARGUS_UPDATE_DIR) is not set, so the host can't be reached"})
		return
	}
	by := ""
	if u, _ := auth.UserFrom(r.Context()); u != nil {
		by = u.Email
	}
	if err := s.writeUpdateJSONAtomic(backupRequestFile, map[string]any{"kind": req.Kind, "at": time.Now().Unix(), "by": by}); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not reach the host: " + err.Error()})
		return
	}
	s.logger.Info("backup requested", "kind", req.Kind, "by", by)
	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()
	writeJSON(w, http.StatusOK, s.backupView(ctx))
}

// handleBackupSSHKey serves POST /api/backup/ssh-key (admin): a new key pair for rsync over SSH.
func (s *Server) handleBackupSSHKey(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()
	if _, err := ensureBackupSSHKey(ctx, s.st, true); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not create the SSH key"})
		return
	}
	s.logger.Info("backup SSH key regenerated")
	writeJSON(w, http.StatusOK, s.backupView(ctx))
}

// backupNotices raises system notices when backups fail or stop: the last run failed, or the last
// good one is more than a day and a half old while backups are on.
func (s *Server) backupNotices(ctx context.Context) []notice {
	c := loadBackupConfig(ctx, s.st)
	if !c.Enabled {
		return nil
	}
	st, err := s.readBackupStatus()
	if err != nil || st == nil {
		return nil
	}
	var out []notice
	if lr := st.LastRun; lr != nil && !lr.OK {
		out = append(out, notice{key: "backup-failed", title: "Core backup failed", detail: lr.Error, view: "settings"})
	}
	if st.LastOKAt > 0 {
		if age := time.Since(time.Unix(st.LastOKAt, 0)); age > 36*time.Hour {
			out = append(out, notice{key: "backup-overdue", title: "No core backup lately",
				detail: fmt.Sprintf("The last good backup is %d hours old.", int(age.Hours())), view: "settings"})
		}
	}
	if rm := st.Remote; rm != nil && !rm.OK && rm.Error != "" {
		out = append(out, notice{key: "backup-remote", title: "Core backup export failed", detail: rm.Error, view: "settings"})
	}
	return out
}

// backupDBInto writes a consistent copy of the Argus database to path (the backup-db subcommand).
func backupDBInto(ctx context.Context, st *store.Store, path string) error {
	if !strings.HasPrefix(path, "/") || strings.Contains(path, "..") {
		return fmt.Errorf("the path must be absolute")
	}
	_ = os.Remove(path)
	return st.VacuumInto(ctx, path)
}

// BackupDBInto is backupDBInto for main.
func BackupDBInto(ctx context.Context, st *store.Store, path string) error {
	return backupDBInto(ctx, st, path)
}
