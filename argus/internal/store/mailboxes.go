// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// Mailbox is a mailbox Argus reads for the push sensors that report by email (server/mailboxes.go):
// IMAP with a user name and password, or a Microsoft 365 mailbox through Graph with an app
// registration. Secret is the password or the client secret, kept encrypted. Argus only reads it.
type Mailbox struct {
	ID        int64
	Name      string
	Kind      string // imap | graph
	Host      string // imap: the server
	Port      int    // imap
	Security  string // imap: tls | starttls
	Username  string // imap
	Secret    string // the password (imap) or the client secret (graph); decrypted on read
	Tenant    string // graph: the directory (tenant) id
	ClientID  string // graph: the application (client) id
	Address   string // graph: the mailbox's address
	Folder    string
	CreatedAt int64
	CreatedBy string
	// What the last reads found: when one last worked, why the last one didn't (and since when), how
	// many mails it has read, and where it got to (watermark: kind-specific).
	LastAt     int64
	LastError  string
	ErrorSince int64
	Mails      int64
	Watermark  string
}

const mailboxColumns = `id,name,kind,host,port,security,username,secret_enc,tenant,client_id,address,folder,created_at,created_by,last_at,last_error,error_since,mails,watermark`

func (s *Store) scanMailbox(row rowScanner) (*Mailbox, error) {
	var m Mailbox
	var enc string
	if err := row.Scan(&m.ID, &m.Name, &m.Kind, &m.Host, &m.Port, &m.Security, &m.Username, &enc, &m.Tenant, &m.ClientID, &m.Address, &m.Folder,
		&m.CreatedAt, &m.CreatedBy, &m.LastAt, &m.LastError, &m.ErrorSince, &m.Mails, &m.Watermark); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	if enc != "" {
		m.Secret = s.cipher.Decrypt(enc)
	}
	return &m, nil
}

// ListMailboxes returns every mailbox, oldest first.
func (s *Store) ListMailboxes(ctx context.Context) ([]Mailbox, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+mailboxColumns+` FROM mailboxes ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Mailbox
	for rows.Next() {
		m, err := s.scanMailbox(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *m)
	}
	return out, rows.Err()
}

// GetMailbox returns one mailbox.
func (s *Store) GetMailbox(ctx context.Context, id int64) (*Mailbox, error) {
	return s.scanMailbox(s.db.QueryRowContext(ctx, `SELECT `+mailboxColumns+` FROM mailboxes WHERE id=?`, id))
}

// CreateMailbox stores a new mailbox and returns its id.
func (s *Store) CreateMailbox(ctx context.Context, m Mailbox) (int64, error) {
	res, err := s.db.ExecContext(ctx, `INSERT INTO mailboxes(name,kind,host,port,security,username,secret_enc,tenant,client_id,address,folder,created_at,created_by)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`, m.Name, m.Kind, m.Host, m.Port, m.Security, m.Username, s.cipher.Encrypt(m.Secret), m.Tenant, m.ClientID, m.Address, m.Folder,
		time.Now().Unix(), m.CreatedBy)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// UpdateMailbox changes a mailbox's settings. Where it reads (kind, server, user, mailbox, folder)
// changing starts it over: what it read before says nothing about the new place.
func (s *Store) UpdateMailbox(ctx context.Context, m Mailbox) error {
	_, err := s.db.ExecContext(ctx, `UPDATE mailboxes SET
		watermark = CASE WHEN kind<>? OR host<>? OR port<>? OR username<>? OR tenant<>? OR address<>? OR folder<>? THEN '' ELSE watermark END,
		name=?,kind=?,host=?,port=?,security=?,username=?,secret_enc=?,tenant=?,client_id=?,address=?,folder=?
		WHERE id=?`,
		m.Kind, m.Host, m.Port, m.Username, m.Tenant, m.Address, m.Folder,
		m.Name, m.Kind, m.Host, m.Port, m.Security, m.Username, s.cipher.Encrypt(m.Secret), m.Tenant, m.ClientID, m.Address, m.Folder, m.ID)
	return err
}

// DeleteMailbox removes a mailbox; the push sensors that read it stop reading email.
func (s *Store) DeleteMailbox(ctx context.Context, id int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `UPDATE push_sensors SET mail_box=0 WHERE mail_box=?`, id); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM mailboxes WHERE id=?`, id); err != nil {
		return err
	}
	return tx.Commit()
}

// MailboxRead records a read that worked: the mails it took and where it got to.
func (s *Store) MailboxRead(ctx context.Context, id int64, mails int, watermark string, at time.Time) error {
	_, err := s.db.ExecContext(ctx, `UPDATE mailboxes SET last_at=?, last_error='', error_since=0, mails=mails+?, watermark=? WHERE id=?`,
		at.Unix(), mails, watermark, id)
	return err
}

// MailboxFailed records a read that didn't work, and since when they haven't.
func (s *Store) MailboxFailed(ctx context.Context, id int64, why string, at time.Time) error {
	_, err := s.db.ExecContext(ctx, `UPDATE mailboxes SET last_error=?, error_since=CASE WHEN error_since=0 THEN ? ELSE error_since END WHERE id=?`,
		why, at.Unix(), id)
	return err
}

// PushMailRule is how a push sensor reads its runs from email: the mailbox, what a mail of its job
// looks like (from, subject) and the words that say it worked or failed (comma-separated).
type PushMailRule struct {
	Mailbox int64 // 0 = it doesn't read email
	From    string
	Subject string
	OK      string
	Fail    string
}

// SetPushMailRule stores a push sensor's email rule.
func (s *Store) SetPushMailRule(ctx context.Context, id int64, r PushMailRule) error {
	_, err := s.db.ExecContext(ctx, `UPDATE push_sensors SET mail_box=?, mail_from=?, mail_subject=?, mail_ok=?, mail_fail=? WHERE id=?`,
		r.Mailbox, r.From, r.Subject, r.OK, r.Fail, id)
	return err
}

// RecordPushByID stores a run a push sensor's mail reported. A run older than the one on record (a
// mail that arrived late) counts but doesn't replace it.
func (s *Store) RecordPushByID(ctx context.Context, id int64, ok bool, msg string, at time.Time, mailSubject string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE push_sensors SET
		last_ok = CASE WHEN ? >= last_at THEN ? ELSE last_ok END,
		last_msg = CASE WHEN ? >= last_at THEN ? ELSE last_msg END,
		mail_last_at = CASE WHEN ? >= mail_last_at THEN ? ELSE mail_last_at END,
		mail_last_subject = CASE WHEN ? >= mail_last_at THEN ? ELSE mail_last_subject END,
		last_at = MAX(last_at, ?),
		runs = runs + 1
		WHERE id=?`,
		at.Unix(), ok, at.Unix(), msg, at.Unix(), at.Unix(), at.Unix(), mailSubject, at.Unix(), id)
	return err
}
