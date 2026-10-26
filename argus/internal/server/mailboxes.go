// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"argus/internal/auth"
	"argus/internal/mailread"
	"argus/internal/store"
)

// The email heartbeat (OneUptime's incoming email monitor, DESIGN section 5 "Push sensors"): a push
// sensor can read its runs from the mails its job sends to a mailbox, for the gear that only reports
// by email (a backup's result, a NAS's daily report). Admins set the mailboxes in Settings (IMAP, or
// a Microsoft 365 mailbox through Graph); Argus reads each every 2 minutes and never changes it. A
// mail matches a push sensor by its sender and subject; a failure word in it (the subject first, then
// the start of the text) makes a failed run, else a success word a good one, else it is a failed run
// that says so, so a changed wording doesn't pass quietly. The run counts at the time the mail arrived.

const (
	mailPollEvery  = 2 * time.Minute
	mailLookback   = 48 * time.Hour // what a new mailbox, or a new rule, reads back
	mailRecentKeep = 300            // mails kept per mailbox, for a rule saved after they arrived
	maxMailboxes   = 20
)

// mailboxView is a mailbox as Settings shows it: never its secret.
type mailboxView struct {
	ID         int64  `json:"id"`
	Name       string `json:"name"`
	Kind       string `json:"kind"`
	Host       string `json:"host,omitempty"`
	Port       int    `json:"port,omitempty"`
	Security   string `json:"security,omitempty"`
	Username   string `json:"username,omitempty"`
	HasSecret  bool   `json:"has_secret"`
	Tenant     string `json:"tenant,omitempty"`
	ClientID   string `json:"client_id,omitempty"`
	Address    string `json:"address,omitempty"`
	Folder     string `json:"folder"`
	LastAt     int64  `json:"last_at,omitempty"`
	LastError  string `json:"last_error,omitempty"`
	ErrorSince int64  `json:"error_since,omitempty"`
	Mails      int64  `json:"mails"`
	Sensors    int    `json:"sensors"` // push sensors reading it
}

func toMailboxView(m store.Mailbox, sensors int) mailboxView {
	return mailboxView{ID: m.ID, Name: m.Name, Kind: m.Kind, Host: m.Host, Port: m.Port, Security: m.Security, Username: m.Username,
		HasSecret: m.Secret != "", Tenant: m.Tenant, ClientID: m.ClientID, Address: m.Address, Folder: m.Folder,
		LastAt: m.LastAt, LastError: m.LastError, ErrorSince: m.ErrorSince, Mails: m.Mails, Sensors: sensors}
}

type mailboxRequest struct {
	ID       int64  `json:"id"` // a Test of a saved mailbox: its secret when none is typed
	Name     string `json:"name"`
	Kind     string `json:"kind"`
	Host     string `json:"host"`
	Port     int    `json:"port"`
	Security string `json:"security"`
	Username string `json:"username"`
	Secret   string `json:"secret"` // "" = keep the saved one
	Tenant   string `json:"tenant"`
	ClientID string `json:"client_id"`
	Address  string `json:"address"`
	Folder   string `json:"folder"`
}

var (
	reMailHost = regexp.MustCompile(`^[A-Za-z0-9.-]{1,253}$|^\[[0-9A-Fa-f:.]+\]$`)
	reGUID     = regexp.MustCompile(`^[0-9A-Fa-f]{8}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{12}$`)
	reTenant   = regexp.MustCompile(`^[0-9A-Fa-f]{8}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{12}$|^[A-Za-z0-9.-]{3,253}$`)
)

func noControl(s string) bool {
	for _, c := range s {
		if unicode.IsControl(c) {
			return false
		}
	}
	return true
}

// validate checks a mailbox (secretSaved: a saved one has a secret, so a blank one keeps it).
func (req *mailboxRequest) validate(secretSaved bool) string {
	req.Name, req.Host, req.Username = strings.TrimSpace(req.Name), strings.TrimSpace(req.Host), strings.TrimSpace(req.Username)
	req.Tenant, req.ClientID, req.Address, req.Folder = strings.TrimSpace(req.Tenant), strings.TrimSpace(req.ClientID), strings.TrimSpace(req.Address), strings.TrimSpace(req.Folder)
	if n := len([]rune(req.Name)); n == 0 || n > 64 || !noControl(req.Name) {
		return "a mailbox needs a name, up to 64 characters"
	}
	if len(req.Folder) > 200 || !noControl(req.Folder) || !noControl(req.Username) || len(req.Username) > 256 || len(req.Secret) > 1024 {
		return "a field is too long or has characters it can't have"
	}
	if req.Secret == "" && !secretSaved {
		if req.Kind == "graph" {
			return "the client secret is needed"
		}
		return "the password is needed"
	}
	switch req.Kind {
	case "imap":
		if !reMailHost.MatchString(req.Host) {
			return "the server must be a host name or an address"
		}
		if req.Security != "tls" && req.Security != "starttls" {
			return "the security must be TLS or STARTTLS"
		}
		if req.Port == 0 {
			req.Port = map[string]int{"tls": 993, "starttls": 143}[req.Security]
		}
		if req.Port < 1 || req.Port > 65535 {
			return "the port must be between 1 and 65535"
		}
		if req.Username == "" {
			return "the user name is needed"
		}
		if req.Folder == "" {
			req.Folder = "INBOX"
		}
		req.Tenant, req.ClientID, req.Address = "", "", ""
	case "graph":
		if !reTenant.MatchString(req.Tenant) {
			return "the tenant ID must be the directory's ID (a GUID) or its domain"
		}
		if !reGUID.MatchString(req.ClientID) {
			return "the application (client) ID must be a GUID"
		}
		if strings.Count(req.Address, "@") != 1 || strings.ContainsAny(req.Address, "/?#% \t") || len(req.Address) > 254 {
			return "the mailbox must be an email address"
		}
		if req.Folder == "" {
			req.Folder = "Inbox"
		}
		req.Host, req.Port, req.Security, req.Username = "", 0, "", ""
	default:
		return "the kind must be IMAP or Microsoft 365"
	}
	return ""
}

func (req mailboxRequest) apply(m *store.Mailbox) {
	m.Name, m.Kind, m.Host, m.Port, m.Security, m.Username = req.Name, req.Kind, req.Host, req.Port, req.Security, req.Username
	m.Tenant, m.ClientID, m.Address, m.Folder = req.Tenant, req.ClientID, req.Address, req.Folder
	if req.Secret != "" {
		m.Secret = req.Secret
	}
}

func mailConfig(m store.Mailbox) mailread.Config {
	return mailread.Config{Kind: m.Kind, Host: m.Host, Port: m.Port, Security: m.Security, Username: m.Username, Secret: m.Secret,
		Tenant: m.Tenant, ClientID: m.ClientID, Address: m.Address, Folder: m.Folder}
}

// mailboxUse counts the push sensors reading each mailbox.
func (s *Server) mailboxUse(ctx context.Context) map[int64]int {
	out := map[int64]int{}
	if list, err := s.st.ListPushSensors(ctx, ""); err == nil {
		for _, p := range list {
			if p.Mail.Mailbox != 0 {
				out[p.Mail.Mailbox]++
			}
		}
	}
	return out
}

// GET /api/mailboxes (admin).
func (s *Server) handleListMailboxes(w http.ResponseWriter, r *http.Request) {
	list, err := s.st.ListMailboxes(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": s.errText(r, err)})
		return
	}
	use := s.mailboxUse(r.Context())
	out := make([]mailboxView, 0, len(list))
	for _, m := range list {
		out = append(out, toMailboxView(m, use[m.ID]))
	}
	writeJSON(w, http.StatusOK, out)
}

// GET /api/mailboxes/names (admin, helpdesk): the mailboxes a push sensor can read, by name.
func (s *Server) handleMailboxNames(w http.ResponseWriter, r *http.Request) {
	list, err := s.st.ListMailboxes(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": s.errText(r, err)})
		return
	}
	type nameView struct {
		ID   int64  `json:"id"`
		Name string `json:"name"`
	}
	out := make([]nameView, 0, len(list))
	for _, m := range list {
		out = append(out, nameView{m.ID, m.Name})
	}
	writeJSON(w, http.StatusOK, out)
}

// POST /api/mailboxes, PATCH /api/mailboxes/{id} (admin).
func (s *Server) handleSaveMailbox(w http.ResponseWriter, r *http.Request) {
	var req mailboxRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}
	ctx := r.Context()
	var m store.Mailbox
	editing := r.PathValue("id") != ""
	if editing {
		cur, err := s.st.GetMailbox(ctx, atoi64(r.PathValue("id")))
		if err != nil {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "mailbox not found"})
			return
		}
		m = *cur
		// A new kind needs its own secret: the old one was another system's.
		if req.Kind != m.Kind && req.Secret == "" {
			m.Secret = ""
		}
	}
	if msg := req.validate(m.Secret != ""); msg != "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": msg})
		return
	}
	list, _ := s.st.ListMailboxes(ctx)
	for _, o := range list {
		if o.ID != m.ID && strings.EqualFold(o.Name, req.Name) {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "there is already a mailbox named " + o.Name})
			return
		}
	}
	if !editing && len(list) >= maxMailboxes {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": fmt.Sprintf("up to %d mailboxes", maxMailboxes)})
		return
	}
	req.apply(&m)
	if editing {
		if err := s.st.UpdateMailbox(ctx, m); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": s.errText(r, err)})
			return
		}
		changeObject(r, m.Name)
	} else {
		if u, ok := auth.UserFrom(ctx); ok {
			m.CreatedBy = u.Email
		}
		id, err := s.st.CreateMailbox(ctx, m)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": s.errText(r, err)})
			return
		}
		m.ID = id
	}
	saved, err := s.st.GetMailbox(ctx, m.ID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": s.errText(r, err)})
		return
	}
	s.mail.wake() // read it now rather than in 2 minutes
	writeJSON(w, http.StatusOK, toMailboxView(*saved, s.mailboxUse(ctx)[saved.ID]))
}

// DELETE /api/mailboxes/{id} (admin): the push sensors that read it stop reading email.
func (s *Server) handleDeleteMailbox(w http.ResponseWriter, r *http.Request) {
	id := atoi64(r.PathValue("id"))
	if _, err := s.st.GetMailbox(r.Context(), id); err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "mailbox not found"})
		return
	}
	if err := s.st.DeleteMailbox(r.Context(), id); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": s.errText(r, err)})
		return
	}
	s.mail.forget(id)
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// POST /api/mailboxes/test (admin): sign in with what the form holds (a saved mailbox's secret when
// none is typed) and say what the folder holds.
func (s *Server) handleTestMailbox(w http.ResponseWriter, r *http.Request) {
	var req mailboxRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}
	var m store.Mailbox
	if req.ID != 0 {
		if cur, err := s.st.GetMailbox(r.Context(), req.ID); err == nil && cur.Kind == req.Kind {
			m.Secret = cur.Secret
		}
	}
	if strings.TrimSpace(req.Name) == "" {
		req.Name = "test"
	}
	if msg := req.validate(m.Secret != ""); msg != "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": msg})
		return
	}
	req.apply(&m)
	ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
	defer cancel()
	n, newest, err := mailread.Probe(ctx, mailConfig(m))
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	out := map[string]any{"ok": true, "count": n, "folder": m.Folder}
	if !newest.IsZero() {
		out["newest"] = newest.Unix()
	}
	writeJSON(w, http.StatusOK, out)
}

// mailState is what the read loop keeps between reads: the recent mails of each mailbox (a rule saved
// after its job's mail arrived still finds it) and a way to read at once.
type mailState struct {
	mu     sync.Mutex
	recent map[int64][]mailread.Mail
	kick   chan struct{}
}

func (m *mailState) wake() {
	select {
	case m.kick <- struct{}{}:
	default:
	}
}

func (m *mailState) forget(id int64) {
	m.mu.Lock()
	delete(m.recent, id)
	m.mu.Unlock()
}

func (m *mailState) remember(id int64, mails []mailread.Mail) {
	m.mu.Lock()
	defer m.mu.Unlock()
	keep := append(m.recent[id], mails...)
	cut := time.Now().Add(-mailLookback)
	i := 0
	for i < len(keep) && (keep[i].At.Before(cut) || len(keep)-i > mailRecentKeep) {
		i++
	}
	m.recent[id] = append([]mailread.Mail(nil), keep[i:]...)
}

func (m *mailState) recentOf(id int64) []mailread.Mail {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]mailread.Mail(nil), m.recent[id]...)
}

// startMailRead reads every mailbox every 2 minutes (and at once when one is saved).
func (s *Server) startMailRead(ctx context.Context) {
	go func() {
		t := time.NewTimer(45 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			case <-s.mail.kick:
				if !t.Stop() {
					select {
					case <-t.C:
					default:
					}
				}
			}
			s.readMailboxes(ctx)
			t.Reset(mailPollEvery)
		}
	}()
}

func (s *Server) readMailboxes(ctx context.Context) {
	boxes, err := s.st.ListMailboxes(ctx)
	if err != nil || len(boxes) == 0 {
		return
	}
	sensors, err := s.st.ListPushSensors(ctx, "")
	if err != nil {
		return
	}
	for _, b := range boxes {
		rctx, cancel := context.WithTimeout(ctx, 90*time.Second)
		mails, wm, err := mailread.Read(rctx, mailConfig(b), b.Watermark, mailLookback)
		cancel()
		now := time.Now()
		if err != nil {
			_ = s.st.MailboxFailed(ctx, b.ID, err.Error(), now)
			s.logger.Debug("mailbox: could not read", "mailbox", b.Name, "err", err)
			continue
		}
		for _, m := range mails {
			for _, p := range sensors {
				if p.Mail.Mailbox != b.ID {
					continue
				}
				if hit, ok, msg := matchMail(p.Mail, m); hit {
					_ = s.st.RecordPushByID(ctx, p.ID, ok, msg, m.At, m.Subject)
				}
			}
		}
		s.mail.remember(b.ID, mails)
		_ = s.st.MailboxRead(ctx, b.ID, len(mails), wm, now)
	}
}

// applyRecentMail gives a push sensor whose rule was just saved the newest matching mail its mailbox
// read in the last two days, so it doesn't wait for its job's next mail.
func (s *Server) applyRecentMail(ctx context.Context, p store.PushSensor) {
	if p.Mail.Mailbox == 0 {
		return
	}
	recent := s.mail.recentOf(p.Mail.Mailbox)
	for i := len(recent) - 1; i >= 0; i-- {
		m := recent[i]
		if m.At.Unix() <= p.LastAt {
			return
		}
		if hit, ok, msg := matchMail(p.Mail, m); hit {
			_ = s.st.RecordPushByID(ctx, p.ID, ok, msg, m.At, m.Subject)
			return
		}
	}
}

// matchMail reads a mail against a push sensor's rule: whether it is its job's (the sender and the
// subject contain what the rule says), and if so whether the run went well and its message.
func matchMail(rule store.PushMailRule, m mailread.Mail) (hit, ok bool, msg string) {
	if rule.From == "" && rule.Subject == "" {
		return false, false, ""
	}
	if rule.From != "" && !containsFold(m.From, rule.From) {
		return false, false, ""
	}
	if rule.Subject != "" && !containsFold(m.Subject, rule.Subject) {
		return false, false, ""
	}
	subject := m.Subject
	if subject == "" {
		subject = "(no subject)"
	}
	fail, good := mailWords(rule.Fail), mailWords(rule.OK)
	for _, text := range []string{m.Subject, m.Text} {
		if hasWord(text, fail) {
			return true, false, cleanPushMsg(subject)
		}
		if hasWord(text, good) {
			return true, true, cleanPushMsg(subject)
		}
	}
	return true, false, cleanPushMsg("Neither a success nor a failure word: " + subject)
}

func containsFold(s, sub string) bool {
	return strings.Contains(strings.ToLower(s), strings.ToLower(strings.TrimSpace(sub)))
}

// mailWords splits a rule's word list ("Success, Completed").
func mailWords(list string) []string {
	var out []string
	for _, w := range strings.Split(list, ",") {
		if w = strings.ToLower(strings.TrimSpace(w)); w != "" {
			out = append(out, w)
		}
	}
	return out
}

// hasWord reports whether text has one of the words where a word starts ("fail" finds "Failed", and
// "ok" isn't found in "book"), ignoring case.
func hasWord(text string, words []string) bool {
	low := strings.ToLower(text)
	for _, w := range words {
		for i := 0; ; {
			j := strings.Index(low[i:], w)
			if j < 0 {
				break
			}
			at := i + j
			if at == 0 || !isWordRune(lastRune(low[:at])) {
				return true
			}
			i = at + len(w)
		}
	}
	return false
}

func isWordRune(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) }

func lastRune(s string) rune { r, _ := utf8.DecodeLastRuneInString(s); return r }

// mailboxTrouble is, for each mailbox that can't be read, why and since when, for the push sensors
// that read it ("Backup reports can't be read since 08:12: the server refused the login").
func (s *Server) mailboxTrouble(ctx context.Context) map[int64]string {
	out := map[int64]string{}
	boxes, err := s.st.ListMailboxes(ctx)
	if err != nil {
		return out
	}
	loc := s.location()
	for _, b := range boxes {
		if b.ErrorSince > 0 && b.LastError != "" {
			out[b.ID] = fmt.Sprintf("its mailbox %s can't be read since %s: %s", b.Name, time.Unix(b.ErrorSince, 0).In(loc).Format("Jan 2 15:04"), b.LastError)
		}
	}
	return out
}
