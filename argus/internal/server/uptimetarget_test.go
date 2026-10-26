// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package server

import (
	"math"
	"strconv"
	"testing"

	"argus/internal/zabbix"
)

func tr(clock int64, avg float64) zabbix.TrendRow {
	return zabbix.TrendRow{Clock: strconv.FormatInt(clock, 10), ValueAvg: strconv.FormatFloat(avg, 'f', -1, 64), Num: "60"}
}

// A month's downtime: an hour all up costs nothing, one half down costs half of it; a maintenance
// window takes its share out; the raw checks after the last trend hour count each to the next.
func TestMonthDowntime(t *testing.T) {
	from := int64(0)
	trends := []zabbix.TrendRow{tr(0, 1), tr(3600, 0.5), tr(7200, 0), tr(10800, 1)}
	if got := monthDowntime(trends, nil, nil, from, 14400); got != 1800+3600 {
		t.Fatalf("no maintenance: %v", got)
	}
	// A window over the whole third hour (and half the second): its downtime doesn't count.
	spans := [][2]int64{{5400, 10800}}
	if got := monthDowntime(trends, nil, spans, from, 14400); math.Abs(got-900) > 0.01 {
		t.Fatalf("with maintenance: %v", got)
	}
	// After the last trend hour (ends 14400): down at 14400 and 14460, up at 14520, now 14580.
	hist := []zabbix.HistoryPoint{{Clock: "14400", Value: "0"}, {Clock: "14460", Value: "0"}, {Clock: "14520", Value: "1"}, {Clock: "14000", Value: "0"}}
	if got := monthDowntime(trends, hist, nil, from, 14580); got != 1800+3600+120 {
		t.Fatalf("raw tail: %v", got)
	}
}

// A host's own target wins (0 = none, whatever its site's); else its site's.
func TestEffectiveTarget(t *testing.T) {
	sites := map[string]float64{"site1": 99.5}
	hosts := map[string]float64{"7": 99.9, "8": 0}
	if p, src, _ := effectiveTarget(sites, hosts, "7", []string{"site1/Network"}); p != 99.9 || src != "host" {
		t.Errorf("own: %v %s", p, src)
	}
	if p, _, _ := effectiveTarget(sites, hosts, "8", []string{"site1"}); p != 0 {
		t.Errorf("none: %v", p)
	}
	if p, src, site := effectiveTarget(sites, hosts, "9", []string{"site1/Servers"}); p != 99.5 || src != "site" || site != "site1" {
		t.Errorf("site's: %v %s %s", p, src, site)
	}
	if p, _, _ := effectiveTarget(sites, hosts, "10", []string{"site2"}); p != 0 {
		t.Errorf("no target: %v", p)
	}
}
