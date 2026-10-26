// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package mailread

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/emersion/go-imap"
	"github.com/emersion/go-imap/client"
)

const imapTimeout = 30 * time.Second

// imapRoots are the certificate authorities an IMAP server's certificate must chain to (nil = the
// system's; tests set their own).
var imapRoots *x509.CertPool

// dialIMAP connects, signs in and opens the folder read-only (EXAMINE: nothing gets marked seen).
// The connection is cut when ctx ends.
func dialIMAP(ctx context.Context, c Config) (*client.Client, *imap.MailboxStatus, func(), error) {
	if c.Host == "" || c.Username == "" {
		return nil, nil, nil, errors.New("the server and the user name are needed")
	}
	port := c.Port
	if port == 0 {
		port = 993
		if c.Security == "starttls" {
			port = 143
		}
	}
	addr := net.JoinHostPort(c.Host, strconv.Itoa(port))
	dialer := &net.Dialer{Timeout: 20 * time.Second}
	tlsConf := &tls.Config{ServerName: c.Host, MinVersion: tls.VersionTLS12, RootCAs: imapRoots}
	var cl *client.Client
	var err error
	if c.Security == "starttls" {
		if cl, err = client.DialWithDialer(dialer, addr); err == nil {
			if err = cl.StartTLS(tlsConf); err != nil {
				_ = cl.Terminate()
				return nil, nil, nil, fmt.Errorf("STARTTLS: %w", err)
			}
		}
	} else {
		cl, err = client.DialWithDialerTLS(dialer, addr, tlsConf)
	}
	if err != nil {
		return nil, nil, nil, fmt.Errorf("could not connect to %s: %w", addr, err)
	}
	cl.Timeout = imapTimeout
	stop := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			_ = cl.Terminate()
		case <-stop:
		}
	}()
	closer := func() {
		close(stop)
		_ = cl.Logout()
	}
	if err := cl.Login(c.Username, c.Secret); err != nil {
		closer()
		return nil, nil, nil, fmt.Errorf("the server refused the login: %w", err)
	}
	folder := c.Folder
	if folder == "" {
		folder = "INBOX"
	}
	st, err := cl.Select(folder, true)
	if err != nil {
		closer()
		return nil, nil, nil, fmt.Errorf("could not open the folder %s: %w", folder, err)
	}
	return cl, st, closer, nil
}

// imapWatermark is "<UIDVALIDITY>:<last UID read>".
func parseIMAPWatermark(wm string) (validity, last uint32, ok bool) {
	a, b, found := strings.Cut(wm, ":")
	if !found {
		return 0, 0, false
	}
	v, err1 := strconv.ParseUint(a, 10, 32)
	l, err2 := strconv.ParseUint(b, 10, 32)
	if err1 != nil || err2 != nil {
		return 0, 0, false
	}
	return uint32(v), uint32(l), true
}

func readIMAP(ctx context.Context, c Config, watermark string, lookback time.Duration) ([]Mail, string, error) {
	cl, st, closer, err := dialIMAP(ctx, c)
	if err != nil {
		return nil, watermark, err
	}
	defer closer()
	crit := imap.NewSearchCriteria()
	validity, last, ok := parseIMAPWatermark(watermark)
	if ok && validity == st.UidValidity {
		crit.Uid = new(imap.SeqSet)
		crit.Uid.AddRange(last+1, 0) // last+1:*
	} else {
		// A new mailbox, or the server renumbered the folder: what arrived in the lookback.
		last = 0
		crit.Since = time.Now().Add(-lookback)
	}
	uids, err := cl.UidSearch(crit)
	if err != nil {
		return nil, watermark, fmt.Errorf("could not search the folder: %w", err)
	}
	// "n:*" always answers the newest mail, even below n.
	var want []uint32
	for _, u := range uids {
		if u > last {
			want = append(want, u)
		}
	}
	sort.Slice(want, func(i, j int) bool { return want[i] < want[j] })
	if len(want) > MaxPerRead {
		want = want[:MaxPerRead]
	}
	if len(want) == 0 {
		if ok && validity == st.UidValidity {
			return nil, watermark, nil
		}
		// Nothing in the lookback: start from the newest UID there is (or look back again next time
		// when the server doesn't say it, rather than read the whole folder).
		if st.UidNext > 1 {
			return nil, fmt.Sprintf("%d:%d", st.UidValidity, st.UidNext-1), nil
		}
		return nil, "", nil
	}
	mails, err := fetchIMAP(cl, want)
	if err != nil {
		return nil, watermark, err
	}
	return mails, fmt.Sprintf("%d:%d", st.UidValidity, want[len(want)-1]), nil
}

func fetchIMAP(cl *client.Client, uids []uint32) ([]Mail, error) {
	set := new(imap.SeqSet)
	set.AddNum(uids...)
	section := &imap.BodySectionName{Peek: true, Partial: []int{0, headMax}}
	items := []imap.FetchItem{imap.FetchUid, imap.FetchInternalDate, section.FetchItem()}
	ch := make(chan *imap.Message, 16)
	done := make(chan error, 1)
	go func() { done <- cl.UidFetch(set, items, ch) }()
	var out []Mail
	for msg := range ch {
		m := Mail{ID: strconv.FormatUint(uint64(msg.Uid), 10), At: msg.InternalDate}
		if r := msg.GetBody(section); r != nil {
			raw, _ := io.ReadAll(io.LimitReader(r, headMax))
			m.From, m.Subject, m.Text = parseMessage(raw)
		}
		out = append(out, m)
	}
	if err := <-done; err != nil {
		return nil, fmt.Errorf("could not read the mails: %w", err)
	}
	sort.SliceStable(out, func(i, j int) bool { return atoiU(out[i].ID) < atoiU(out[j].ID) })
	return out, nil
}

func atoiU(s string) uint64 { n, _ := strconv.ParseUint(s, 10, 64); return n }

func probeIMAP(ctx context.Context, c Config) (int, time.Time, error) {
	cl, st, closer, err := dialIMAP(ctx, c)
	if err != nil {
		return 0, time.Time{}, err
	}
	defer closer()
	if st.Messages == 0 {
		return 0, time.Time{}, nil
	}
	set := new(imap.SeqSet)
	set.AddNum(st.Messages)
	ch := make(chan *imap.Message, 1)
	done := make(chan error, 1)
	go func() { done <- cl.Fetch(set, []imap.FetchItem{imap.FetchInternalDate}, ch) }()
	var newest time.Time
	for msg := range ch {
		newest = msg.InternalDate
	}
	if err := <-done; err != nil {
		return int(st.Messages), time.Time{}, nil
	}
	return int(st.Messages), newest, nil
}
