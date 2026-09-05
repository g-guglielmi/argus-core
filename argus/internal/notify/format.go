package notify

import (
	"math"
	"strconv"
	"strings"
)

var byteUnits = []string{"B", "KB", "MB", "GB", "TB", "PB"}
var bitUnits = []string{"bps", "Kbps", "Mbps", "Gbps", "Tbps"}

// FormatReading renders a Zabbix reading (raw last-value string + units) the way the web UI does, so
// an alert message and the UI agree on how a value reads: seconds as ms/µs/ns, bytes/bits at
// binary/decimal magnitudes, uptime as a duration, and everything else as a trimmed number with its
// unit. Non-numeric readings (text, checksums) pass through unchanged, with no unit.
func FormatReading(lastValue, units string) string {
	raw := strings.TrimSpace(lastValue)
	n, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return raw
	}
	v, u := scaleReading(n, units)
	if u == "" {
		return v
	}
	return v + " " + u
}

func scaleReading(n float64, units string) (string, string) {
	switch units {
	case "B":
		return scaleBy(n, 1024, byteUnits)
	case "Bps":
		v, u := scaleBy(n, 1024, byteUnits)
		return v, u + "ps"
	case "bps":
		return scaleBy(n, 1000, bitUnits)
	case "uptime":
		return fmtDur(int64(n)), ""
	case "s":
		return scaleSeconds(n)
	default:
		return roundNum(n), units
	}
}

// scaleSeconds renders seconds at a human-friendly magnitude: sub-second values as ms/µs/ns.
func scaleSeconds(n float64) (string, string) {
	switch a := math.Abs(n); {
	case a == 0:
		return "0", "s"
	case a < 1e-6:
		return roundNum(n * 1e9), "ns"
	case a < 1e-3:
		return roundNum(n * 1e6), "µs"
	case a < 1:
		return roundNum(n * 1e3), "ms"
	default:
		return roundNum(n), "s"
	}
}

// scaleBy reduces n by `base` until it fits a unit, returning [value, unit].
func scaleBy(n, base float64, units []string) (string, string) {
	v, i := n, 0
	for math.Abs(v) >= base && i < len(units)-1 {
		v /= base
		i++
	}
	if i == 0 {
		return strconv.FormatInt(int64(math.Round(v)), 10), units[i]
	}
	return trimFloat(v, 2), units[i]
}

// roundNum mirrors the UI: integers as-is, |n| >= 1 to 2 decimals, else 4, trailing zeros stripped.
func roundNum(n float64) string {
	if !math.IsInf(n, 0) && n == math.Trunc(n) {
		return strconv.FormatInt(int64(n), 10)
	}
	if math.Abs(n) >= 1 {
		return trimFloat(n, 2)
	}
	return trimFloat(n, 4)
}

// trimFloat rounds n to `decimals` places and formats it with trailing zeros removed.
func trimFloat(n float64, decimals int) string {
	factor := math.Pow(10, float64(decimals))
	return strconv.FormatFloat(math.Round(n*factor)/factor, 'f', -1, 64)
}
