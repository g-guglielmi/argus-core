// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package server

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"strings"
	"time"

	"argus/internal/zabbix"
)

// Data retention (Settings expansion, §D): how long Zabbix keeps raw history and hourly trends,
// plus TimescaleDB compression. The values live in Zabbix's global housekeeping settings - the core
// installers set 30d / 730d / compress after 7d - and this exposes them in Argus so changing them
// no longer means opening the Zabbix frontend.
//
// The chart tabs set the floors: 2h and 2d read raw history, 7d through 1Y read trends
// (timeRanges in hosts.go). History shorter than 2 days would leave the 2d tab partly empty and
// trends shorter than 7 days the 7d tab, so those are the minimums; a trend period under a year
// only earns a warning in the UI (the 1Y tab fills in gradually).
const (
	retentionMinHistoryDays = 2
	retentionMinTrendDays   = 7
	retentionMaxDays        = 9125 // Zabbix's 25-year cap
	retentionMinCompressDay = 7    // Zabbix won't compress chunks younger than 7 days
)

type retentionView struct {
	Available bool   `json:"available"`       // the token can read housekeeping (needs Super admin)
	Error     string `json:"error,omitempty"` // why it isn't available
	// History / trend periods in whole days, and whether they override every item's own period.
	// Argus always saves with the override on; an install that never set it shows false here.
	HistoryDays     int  `json:"history_days"`
	HistoryOverride bool `json:"history_override"`
	TrendDays       int  `json:"trend_days"`
	TrendOverride   bool `json:"trend_override"`
	// Compression needs TimescaleDB. Zabbix 7.0's housekeeping.get does not return the documented
	// compression_availability field at all, so availability comes from db_extension (what the
	// Zabbix frontend itself uses), with compression already on as proof too.
	CompressionAvailable bool `json:"compression_available"`
	Compression          bool `json:"compression"`
	CompressAfterDays    int  `json:"compress_after_days"`
	MinHistoryDays       int  `json:"min_history_days"`
	MinTrendDays         int  `json:"min_trend_days"`
}

func periodDays(p string) int {
	secs, err := zabbix.ParsePeriod(p)
	if err != nil {
		return 0
	}
	return int(math.Round(float64(secs) / 86400))
}

// compressionAvailable reports whether the database can compress history (see retentionView).
func compressionAvailable(hk zabbix.Housekeeping) bool {
	return strings.EqualFold(string(hk.DBExtension), "timescaledb") || hk.CompressionAvailability == "1" || hk.CompressionStatus == "1"
}

func retentionFrom(hk zabbix.Housekeeping) retentionView {
	return retentionView{
		Available:            true,
		HistoryDays:          periodDays(string(hk.History)),
		HistoryOverride:      hk.HistoryGlobal == "1",
		TrendDays:            periodDays(string(hk.Trends)),
		TrendOverride:        hk.TrendsGlobal == "1",
		CompressionAvailable: compressionAvailable(hk),
		Compression:          hk.CompressionStatus == "1",
		CompressAfterDays:    periodDays(string(hk.CompressOlder)),
		MinHistoryDays:       retentionMinHistoryDays,
		MinTrendDays:         retentionMinTrendDays,
	}
}

// retentionUnavailable turns a housekeeping read failure into the view's explanation.
func retentionUnavailable(err error) retentionView {
	msg := "Could not read the retention settings from Zabbix: " + err.Error()
	if zabbix.IsPermissionError(err) {
		msg = "The Zabbix API token can't read housekeeping settings - Zabbix allows that only for a Super admin. Use a Super admin token, or change retention in the Zabbix frontend (Administration → Housekeeping)."
	}
	return retentionView{Error: msg, MinHistoryDays: retentionMinHistoryDays, MinTrendDays: retentionMinTrendDays}
}

// handleGetRetention returns the current retention settings (admin).
func (s *Server) handleGetRetention(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	hk, err := s.zbx.Housekeeping(ctx)
	if err != nil {
		writeJSON(w, http.StatusOK, retentionUnavailable(err))
		return
	}
	writeJSON(w, http.StatusOK, retentionFrom(hk))
}

type retentionUpdate struct {
	HistoryDays       int  `json:"history_days"`
	TrendDays         int  `json:"trend_days"`
	Compression       bool `json:"compression"`
	CompressAfterDays int  `json:"compress_after_days"`
}

// validate checks the requested periods against the chart floors and Zabbix's own limits.
func (u retentionUpdate) validate(compressionAvailable bool) error {
	if u.HistoryDays < retentionMinHistoryDays || u.HistoryDays > retentionMaxDays {
		return fmt.Errorf("history must be between %d and %d days (the 2d chart tab reads raw history)", retentionMinHistoryDays, retentionMaxDays)
	}
	if u.TrendDays < retentionMinTrendDays || u.TrendDays > retentionMaxDays {
		return fmt.Errorf("trends must be between %d and %d days (the 7d to 1Y chart tabs read trends)", retentionMinTrendDays, retentionMaxDays)
	}
	// Without TimescaleDB the compression fields are ignored (never written), not an error: the UI
	// hides them, but the request still carries the current state.
	if u.Compression && compressionAvailable {
		if u.CompressAfterDays < retentionMinCompressDay || u.CompressAfterDays > retentionMaxDays {
			return fmt.Errorf("compression must start after between %d and %d days", retentionMinCompressDay, retentionMaxDays)
		}
	}
	return nil
}

// handleSetRetention writes history / trend periods (always as a global override, so every item
// follows them) and, where the database supports it, the compression settings (admin).
func (s *Server) handleSetRetention(w http.ResponseWriter, r *http.Request) {
	var u retentionUpdate
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 512)).Decode(&u); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	hk, err := s.zbx.Housekeeping(ctx)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": retentionUnavailable(err).Error})
		return
	}
	compressionAvailable := compressionAvailable(hk)
	if err := u.validate(compressionAvailable); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	fields := map[string]any{
		"hk_history_mode": 1, "hk_history_global": 1, "hk_history": fmt.Sprintf("%dd", u.HistoryDays),
		"hk_trends_mode": 1, "hk_trends_global": 1, "hk_trends": fmt.Sprintf("%dd", u.TrendDays),
	}
	if compressionAvailable {
		if u.Compression {
			fields["compression_status"] = 1
			fields["compress_older"] = fmt.Sprintf("%dd", u.CompressAfterDays)
		} else {
			fields["compression_status"] = 0
		}
	}
	if err := s.zbx.UpdateHousekeeping(ctx, fields); err != nil {
		msg := "Zabbix rejected the retention change: " + err.Error()
		if zabbix.IsPermissionError(err) {
			msg = retentionUnavailable(err).Error
		}
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": msg})
		return
	}
	s.logger.Info("retention updated", "history_days", u.HistoryDays, "trend_days", u.TrendDays, "compression", u.Compression, "compress_after_days", u.CompressAfterDays)
	hk, err = s.zbx.Housekeeping(ctx)
	if err != nil {
		writeJSON(w, http.StatusOK, retentionUnavailable(err))
		return
	}
	writeJSON(w, http.StatusOK, retentionFrom(hk))
}
