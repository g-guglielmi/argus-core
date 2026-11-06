// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

// Package syslogout sends Argus's activity log (DESIGN section 7c: who signed in, who changed what)
// to an external syslog server, one RFC 5424 message per entry, over UDP, TCP or TLS. TCP and TLS
// use octet-counting framing (RFC 6587 3.4.1, RFC 5425), which rsyslog, syslog-ng and the SIEMs take.
//
// Entries queue in memory and go out in order; a server that is down holds the queue (up to
// QueueSize entries, newer ones beyond that are dropped and counted) and the sender keeps trying with
// a growing pause. Turning syslog off drops what is queued.
package syslogout

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"
)

// QueueSize is how many entries wait while the server can't be reached.
const QueueSize = 10000

// sdID is the structured-data ID of Argus's fields. A private SD-ID needs an enterprise number; 32473
// is the one IANA reserves for examples and documentation (RFC 5612) until Argus has its own.
const sdID = "argus@32473"

// Config is where the activity goes. Server "" turns sending off.
type Config struct {
	Server   string // host:port (514 for UDP and TCP, 6514 for TLS when no port is given)
	Protocol string // udp | tcp | tls
	Facility int    // 16..23 = local0..local7
	Hostname string // the HOSTNAME field: Argus's own name ("-" when unknown)
}

// Address is the server with its protocol's default port when it has none.
func (c Config) Address() string {
	if c.Server == "" {
		return ""
	}
	if _, _, err := net.SplitHostPort(c.Server); err == nil {
		return c.Server
	}
	port := "514"
	if c.Protocol == "tls" {
		port = "6514"
	}
	return net.JoinHostPort(strings.Trim(c.Server, "[]"), port)
}

// Severities used (RFC 5424 6.2.1).
const (
	SevWarning = 4
	SevNotice  = 5
	SevInfo    = 6
)

// Entry is one activity-log entry as syslog carries it.
type Entry struct {
	At       time.Time
	Severity int
	MsgID    string      // what kind of entry: "signin", "change"
	Fields   [][2]string // its structured data, in order; empty values are left out
	Message  string      // the readable line
}

// Status is how sending has gone, for Settings.
type Status struct {
	Sent        int64  `json:"sent"`
	Dropped     int64  `json:"dropped"`
	Queued      int    `json:"queued"`
	LastSentAt  int64  `json:"last_sent_at,omitempty"`
	LastError   string `json:"last_error,omitempty"`
	LastErrorAt int64  `json:"last_error_at,omitempty"`
}

// Forwarder sends entries to the configured server.
type Forwarder struct {
	cfg  func() Config
	q    chan Entry
	dial func(ctx context.Context, c Config) (net.Conn, error)

	mu      sync.Mutex
	st      Status
	conn    net.Conn
	connFor Config
}

// New makes a forwarder reading its configuration from cfg on every entry, so a change in Settings
// applies to the next one.
func New(cfg func() Config) *Forwarder {
	return &Forwarder{cfg: cfg, q: make(chan Entry, QueueSize), dial: dialConfig}
}

// Send queues an entry; it never blocks. Nothing is queued while sending is off.
func (f *Forwarder) Send(e Entry) {
	if f == nil || f.cfg().Server == "" {
		return
	}
	select {
	case f.q <- e:
	default:
		f.mu.Lock()
		f.st.Dropped++
		f.mu.Unlock()
	}
}

// Status reports the counters and the last error.
func (f *Forwarder) Status() Status {
	f.mu.Lock()
	defer f.mu.Unlock()
	s := f.st
	s.Queued = len(f.q)
	return s
}

// Start sends queued entries until ctx ends.
func (f *Forwarder) Start(ctx context.Context) {
	go func() {
		for {
			select {
			case <-ctx.Done():
				f.closeConn()
				return
			case e := <-f.q:
				f.deliver(ctx, e)
			}
		}
	}()
}

// deliver sends one entry, retrying with a growing pause (up to a minute) while the server can't be
// reached; it gives the entry up only when sending is turned off or Argus stops.
func (f *Forwarder) deliver(ctx context.Context, e Entry) {
	pause := time.Second
	for {
		c := f.cfg()
		if c.Server == "" {
			f.closeConn()
			return
		}
		err := f.write(ctx, c, e)
		f.mu.Lock()
		if err == nil {
			f.st.Sent++
			f.st.LastSentAt = time.Now().Unix()
			f.mu.Unlock()
			return
		}
		f.st.LastError, f.st.LastErrorAt = err.Error(), time.Now().Unix()
		f.mu.Unlock()
		select {
		case <-ctx.Done():
			return
		case <-time.After(pause):
		}
		if pause *= 2; pause > time.Minute {
			pause = time.Minute
		}
	}
}

// write sends e over the open connection, opening (or reopening, after a settings change or an error)
// it as needed.
func (f *Forwarder) write(ctx context.Context, c Config, e Entry) error {
	f.mu.Lock()
	conn := f.conn
	if conn != nil && f.connFor != c {
		_ = conn.Close()
		conn, f.conn = nil, nil
	}
	f.mu.Unlock()
	if conn == nil {
		dctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		nc, err := f.dial(dctx, c)
		cancel()
		if err != nil {
			return err
		}
		f.mu.Lock()
		f.conn, f.connFor, conn = nc, c, nc
		f.mu.Unlock()
	}
	_ = conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
	if _, err := conn.Write(Frame(c.Protocol, Format(c, e))); err != nil {
		f.closeConn()
		return err
	}
	return nil
}

func (f *Forwarder) closeConn() {
	f.mu.Lock()
	if f.conn != nil {
		_ = f.conn.Close()
		f.conn = nil
	}
	f.mu.Unlock()
}

// Test sends one message now on a connection of its own and says whether the server took it (over UDP
// that only means it could be sent: UDP has no answer).
func (f *Forwarder) Test(ctx context.Context, e Entry) error {
	c := f.cfg()
	if c.Server == "" {
		return errors.New("no syslog server is set")
	}
	dctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	conn, err := f.dial(dctx, c)
	if err != nil {
		return err
	}
	defer conn.Close()
	_ = conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
	_, err = conn.Write(Frame(c.Protocol, Format(c, e)))
	return err
}

func dialConfig(ctx context.Context, c Config) (net.Conn, error) {
	addr := c.Address()
	switch c.Protocol {
	case "tcp":
		var d net.Dialer
		return d.DialContext(ctx, "tcp", addr)
	case "tls":
		host, _, _ := net.SplitHostPort(addr)
		d := tls.Dialer{Config: &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12}}
		return d.DialContext(ctx, "tcp", addr)
	default:
		var d net.Dialer
		return d.DialContext(ctx, "udp", addr)
	}
}

// Frame is a message as the protocol carries it: a datagram as it is over UDP, octet-counted over TCP
// and TLS ("LEN SP MSG").
func Frame(protocol, msg string) []byte {
	if protocol == "tcp" || protocol == "tls" {
		return []byte(strconv.Itoa(len(msg)) + " " + msg)
	}
	return []byte(msg)
}

// Format is the RFC 5424 message: <PRI>1 TIMESTAMP HOSTNAME APP-NAME PROCID MSGID [SD] MSG.
func Format(c Config, e Entry) string {
	fac := c.Facility
	if fac < 0 || fac > 23 {
		fac = 16
	}
	sev := e.Severity
	if sev < 0 || sev > 7 {
		sev = SevInfo
	}
	at := e.At
	if at.IsZero() {
		at = time.Now()
	}
	var sd strings.Builder
	sd.WriteString("[" + sdID)
	n := 0
	for _, kv := range e.Fields {
		if kv[1] == "" || !validName(kv[0]) {
			continue
		}
		fmt.Fprintf(&sd, " %s=\"%s\"", kv[0], escapeParam(kv[1]))
		n++
	}
	sd.WriteString("]")
	sdText := sd.String()
	if n == 0 {
		sdText = "-"
	}
	msg := strings.ReplaceAll(strings.ReplaceAll(e.Message, "\r", " "), "\n", " ")
	return fmt.Sprintf("<%d>1 %s %s argus - %s %s %s", fac*8+sev, at.UTC().Format("2006-01-02T15:04:05.000Z"),
		header(c.Hostname, 255), header(e.MsgID, 32), sdText, msg)
}

// header is a header field: printable US-ASCII without spaces, at most n characters, "-" when empty.
func header(s string, n int) string {
	var b strings.Builder
	for _, r := range s {
		if r > 32 && r < 127 {
			b.WriteRune(r)
		}
		if b.Len() >= n {
			break
		}
	}
	if b.Len() == 0 {
		return "-"
	}
	return b.String()
}

// validName is an SD parameter name: 1 to 32 printable US-ASCII characters, none of = ] " or space.
func validName(s string) bool {
	if s == "" || len(s) > 32 {
		return false
	}
	for _, r := range s {
		if r <= 32 || r >= 127 || r == '=' || r == ']' || r == '"' {
			return false
		}
	}
	return true
}

// escapeParam escapes a parameter value: \ " and ] take a backslash; line breaks become spaces.
func escapeParam(s string) string {
	s = strings.NewReplacer("\\", "\\\\", "\"", "\\\"", "]", "\\]", "\r", " ", "\n", " ").Replace(s)
	if len(s) > 1024 {
		s = s[:1024]
	}
	return s
}
