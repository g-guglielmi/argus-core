// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package server

import (
	"strconv"
	"strings"

	"argus/internal/zabbix"
)

// Chart threshold bands: each numeric sensor carries the effective warning / high values read from its
// OWN triggers (macro-resolved by Zabbix, so host overrides, fleet defaults and per-disk-type contexts
// are already applied), and the chart colours only the stretch of line that crosses one. Reading the
// triggers - rather than the threshold catalog - keeps the chart honest to what actually alerts.

// itemThresholds is what a chart needs to band a sensor's line. Below flips the bands for sensors
// where a LOW value is bad (free space, battery charge): the warn/high lines sit under the normal range.
type itemThresholds struct {
	Warn  *float64 `json:"warn,omitempty"`
	High  *float64 `json:"high,omitempty"`
	Below bool     `json:"below,omitempty"`
}

// thrCmp is one `func(/host/key,...) OP number` comparison found in a trigger expression.
type thrCmp struct {
	key string
	op  string
	val float64
}

// thrFuncs are the value functions whose result is in the item's own units (so the number it's
// compared against can be drawn on the item's chart). count/change/nodata/... are not.
var thrFuncs = map[string]bool{"last": true, "min": true, "max": true, "avg": true}

// thrSuffix maps Zabbix expression unit suffixes to multipliers (K/M/G/T are 1024-based in triggers).
var thrSuffix = map[byte]float64{
	'K': 1024, 'M': 1024 * 1024, 'G': 1024 * 1024 * 1024, 'T': 1024 * 1024 * 1024 * 1024,
	's': 1, 'm': 60, 'h': 3600, 'd': 86400, 'w': 604800,
}

func isIdentByte(b byte) bool {
	return b == '_' || b >= '0' && b <= '9' || b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z'
}

// parseThrNumber reads a numeric constant (optionally negative, optionally with a unit suffix) at the
// start of s, returning its value and length. A constant glued to a word ("5abc") is not a number.
func parseThrNumber(s string) (float64, int, bool) {
	n := 0
	if n < len(s) && s[n] == '-' {
		n++
	}
	start := n
	for n < len(s) && (s[n] >= '0' && s[n] <= '9' || s[n] == '.') {
		n++
	}
	if n == start {
		return 0, 0, false
	}
	v, err := strconv.ParseFloat(s[:n], 64)
	if err != nil {
		return 0, 0, false
	}
	if n < len(s) {
		if m, ok := thrSuffix[s[n]]; ok {
			v *= m
			n++
		}
	}
	if n < len(s) && isIdentByte(s[n]) {
		return 0, 0, false
	}
	return v, n, true
}

// parseThrComparisons finds every plain `func(/host/key...) OP number` comparison in an expression.
// A function that is part of arithmetic (`last(/a)/last(/b)>0.9`), a non-value function, an equality
// test (`=0` up/down checks) or a non-numeric right side (`<>"Running"`, `<last(...)`) is skipped.
func parseThrComparisons(expr string) []thrCmp {
	var out []thrCmp
	for i := 0; i+1 < len(expr); i++ {
		if expr[i] != '(' || expr[i+1] != '/' {
			continue
		}
		j := i
		for j > 0 && isIdentByte(expr[j-1]) {
			j--
		}
		name := expr[j:i]
		k := j
		for k > 0 && expr[k-1] == ' ' {
			k--
		}
		arith := k > 0 && strings.IndexByte("+-*/", expr[k-1]) >= 0
		// /host/key - the host part has no slash; the key may carry [params] with quoted commas.
		p := i + 2
		h := strings.IndexByte(expr[p:], '/')
		if h < 0 {
			break
		}
		p += h + 1
		keyStart := p
		depth, inQ := 0, false
		for ; p < len(expr); p++ {
			ch := expr[p]
			if inQ {
				if ch == '\\' {
					p++
				} else if ch == '"' {
					inQ = false
				}
				continue
			}
			if ch == '"' {
				inQ = true
			} else if ch == '[' {
				depth++
			} else if ch == ']' {
				depth--
			} else if depth == 0 && (ch == ',' || ch == ')') {
				break
			}
		}
		key := expr[keyStart:p]
		// Skip the remaining function parameters to the call's closing paren.
		depth, inQ = 0, false
		for ; p < len(expr); p++ {
			ch := expr[p]
			if inQ {
				if ch == '\\' {
					p++
				} else if ch == '"' {
					inQ = false
				}
				continue
			}
			if ch == '"' {
				inQ = true
			} else if ch == '(' {
				depth++
			} else if ch == ')' {
				if depth == 0 {
					break
				}
				depth--
			}
		}
		if p >= len(expr) {
			break
		}
		i = p
		if !thrFuncs[name] || arith {
			continue
		}
		q := p + 1
		for q < len(expr) && expr[q] == ' ' {
			q++
		}
		op := ""
		for _, o := range []string{">=", "<=", "<>", ">", "<", "="} {
			if strings.HasPrefix(expr[q:], o) {
				op = o
				break
			}
		}
		if op == "" || op == "=" || op == "<>" {
			continue
		}
		q += len(op)
		for q < len(expr) && expr[q] == ' ' {
			q++
		}
		v, n, ok := parseThrNumber(expr[q:])
		if !ok {
			continue
		}
		// The constant must be the whole right operand (not `85*2` or `85/last(...)`).
		r := q + n
		for r < len(expr) && expr[r] == ' ' {
			r++
		}
		if r < len(expr) && strings.IndexByte("+-*/", expr[r]) >= 0 {
			continue
		}
		out = append(out, thrCmp{key: key, op: op, val: v})
	}
	return out
}

func minF(vs []float64) float64 {
	m := vs[0]
	for _, v := range vs[1:] {
		if v < m {
			m = v
		}
	}
	return m
}

func maxF(vs []float64) float64 {
	m := vs[0]
	for _, v := range vs[1:] {
		if v > m {
			m = v
		}
	}
	return m
}

// itemThresholdsFrom derives a sensor's chart bands from its triggers. Severity picks the band
// (warning -> warn, average and up -> high, matching severityState). Our band triggers read
// `>=warn and <high` - the `<high` is the NEXT band's cap, not a low threshold - so the direction is
// decided by the single-sided triggers (`>=high` = higher is worse, `<low` = lower is worse), and a
// two-sided trigger then contributes its lower (or upper) edge. A trigger comparing the same value
// both ways (`>=x and <x`: a "dropped below" edge detector) is not a threshold and is skipped.
// Returns nil when no numeric threshold applies (up/down, string state, ...).
func itemThresholdsFrom(trigs []zabbix.ItemTrigger, itemKey string) *itemThresholds {
	type band struct {
		level  int // 1 warn, 2 high
		lo, hi []float64
	}
	var bands []band
	pureAbove, pureBelow := 0, 0
	for _, t := range trigs {
		b := band{}
		switch severityState(t.Priority) {
		case "warning":
			b.level = 1
		case "error":
			b.level = 2
		default:
			continue
		}
		for _, c := range parseThrComparisons(t.Expression) {
			// A trigger over several items (DNS time AND success) only counts comparisons on this one.
			if t.ItemCount > 1 && c.key != itemKey {
				continue
			}
			if c.op[0] == '>' {
				b.lo = append(b.lo, c.val)
			} else {
				b.hi = append(b.hi, c.val)
			}
		}
		switch {
		case len(b.lo) == 0 && len(b.hi) == 0:
			continue
		case len(b.hi) == 0:
			pureAbove++
		case len(b.lo) == 0:
			pureBelow++
		}
		bands = append(bands, b)
	}
	below := pureBelow > 0 && pureAbove == 0
	res := &itemThresholds{Below: below}
	// Several triggers at one level: keep the one that fires first.
	set := func(dst **float64, v float64) {
		if *dst == nil || (!below && v < **dst) || (below && v > **dst) {
			vv := v
			*dst = &vv
		}
	}
	for _, b := range bands {
		var v float64
		if below {
			if len(b.hi) == 0 {
				continue
			}
			v = minF(b.hi)
			if len(b.lo) > 0 && maxF(b.lo) >= v {
				continue
			}
		} else {
			if len(b.lo) == 0 {
				continue
			}
			v = maxF(b.lo)
			if len(b.hi) > 0 && minF(b.hi) <= v {
				continue
			}
		}
		if b.level == 1 {
			set(&res.Warn, v)
		} else {
			set(&res.High, v)
		}
	}
	if res.Warn == nil && res.High == nil {
		return nil
	}
	return res
}
