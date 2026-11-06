// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package syslogout

import (
	"bufio"
	"context"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestFormat(t *testing.T) {
	at := time.Date(2026, 11, 6, 17, 43, 5, 123e6, time.UTC)
	c := Config{Facility: 16, Hostname: "monitoring.example.com"}
	got := Format(c, Entry{At: at, Severity: SevNotice, MsgID: "signin",
		Fields:  [][2]string{{"actor", "admin@example.com"}, {"ip", "10.0.0.5"}, {"empty", ""}, {"detail", `say "hi" ] \ ok`}, {"bad name", "x"}},
		Message: "admin@example.com Sign-in failed:\nwrong password"})
	want := `<133>1 2026-11-06T17:43:05.123Z monitoring.example.com argus - signin [argus@32473 actor="admin@example.com" ip="10.0.0.5" detail="say \"hi\" \] \\ ok"] admin@example.com Sign-in failed: wrong password`
	if got != want {
		t.Fatalf("\n got %s\nwant %s", got, want)
	}
	if got := Format(Config{Facility: 23}, Entry{At: at, Severity: SevInfo, Message: "m"}); got != "<190>1 2026-11-06T17:43:05.123Z - argus - - - m" {
		t.Fatalf("no host, no msgid, no fields: %s", got)
	}
}

func TestFrameAndAddress(t *testing.T) {
	if string(Frame("udp", "abc")) != "abc" || string(Frame("tcp", "abc")) != "3 abc" || string(Frame("tls", "héllo")) != "6 héllo" {
		t.Fatal("framing: a datagram as it is, octet counting over TCP and TLS (bytes, not characters)")
	}
	for c, want := range map[Config]string{
		{Server: "logs.example.lan", Protocol: "udp"}: "logs.example.lan:514",
		{Server: "logs.example.lan", Protocol: "tls"}: "logs.example.lan:6514",
		{Server: "10.0.0.20:1514", Protocol: "tcp"}:   "10.0.0.20:1514",
		{Server: "[2001:db8::1]", Protocol: "tcp"}:    "[2001:db8::1]:514",
		{Server: "", Protocol: "udp"}:                 "",
	} {
		if got := c.Address(); got != want {
			t.Errorf("%+v: %q, want %q", c, got, want)
		}
	}
}

// Over TCP the entries arrive in order, octet-counted; a server that comes up late still gets them.
func TestForwarderTCP(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close() // nobody listens yet: the first tries fail and the entries wait

	var mu sync.Mutex
	cfg := Config{Server: addr, Protocol: "tcp", Facility: 16, Hostname: "argus"}
	f := New(func() Config { mu.Lock(); defer mu.Unlock(); return cfg })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f.Start(ctx)
	for i := 1; i <= 3; i++ {
		f.Send(Entry{Severity: SevInfo, MsgID: "change", Message: "entry " + strconv.Itoa(i)})
	}
	deadline := time.Now().Add(5 * time.Second)
	for f.Status().LastError == "" && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if f.Status().LastError == "" {
		t.Fatal("no error while the server was down")
	}

	ln, err = net.Listen("tcp", addr)
	if err != nil {
		t.Skip("the port was taken meanwhile:", err)
	}
	defer ln.Close()
	got := make(chan string, 3)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		br := bufio.NewReader(conn)
		for i := 0; i < 3; i++ {
			n, err := br.ReadString(' ')
			if err != nil {
				return
			}
			size, _ := strconv.Atoi(strings.TrimSpace(n))
			buf := make([]byte, size)
			if _, err := io.ReadFull(br, buf); err != nil {
				return
			}
			got <- string(buf)
		}
	}()
	for i := 1; i <= 3; i++ {
		select {
		case m := <-got:
			if !strings.HasSuffix(m, "entry "+strconv.Itoa(i)) || !strings.HasPrefix(m, "<134>1 ") {
				t.Fatalf("message %d: %q", i, m)
			}
		case <-time.After(10 * time.Second):
			t.Fatalf("message %d never came", i)
		}
	}
	if s := f.Status(); s.Sent != 3 || s.Dropped != 0 {
		t.Fatalf("status %+v", s)
	}
}

// Nothing is queued while syslog is off, and a full queue drops (and counts) what doesn't fit.
func TestForwarderOffAndFull(t *testing.T) {
	off := New(func() Config { return Config{} })
	off.Send(Entry{Message: "x"})
	if off.Status().Queued != 0 {
		t.Fatal("queued while off")
	}
	full := New(func() Config { return Config{Server: "127.0.0.1:9", Protocol: "udp"} })
	for i := 0; i < QueueSize+5; i++ {
		full.Send(Entry{Message: "x"})
	}
	if s := full.Status(); s.Queued != QueueSize || s.Dropped != 5 {
		t.Fatalf("status %+v", s)
	}
}
