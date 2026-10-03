// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package server

import (
	"context"
	"encoding/json"
	"errors"
	"hash/fnv"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"

	"argus/internal/store"
)

// Tags (DESIGN section 7d): labels across sites. A host has its own and the ones its probe carries
// (every host the probe monitors gets them). The tree filters by them and a channel can be limited to
// hosts with one of its tags.

// tagColors is the palette a tag's colour comes from (the Settings editor offers the same).
var tagColors = []string{"#e5484d", "#f5a524", "#30a46c", "#3b82f6", "#8e4ec6", "#12a594", "#d6409f", "#8b8d98"}

var tagNameShape = regexp.MustCompile(`^[\p{L}\p{N}][\p{L}\p{N} ._:/+-]{0,31}$`)
var tagColorShape = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)

// hostTag is one tag as a host shows it: From names the probe it comes from ("" = the host's own).
type hostTag struct {
	Name  string `json:"name"`
	Color string `json:"color"`
	From  string `json:"from,omitempty"`
}

// cleanTag validates a tag as typed; the colour falls back to one picked from the name.
func cleanTag(name, color, desc string) (store.Tag, string) {
	name = strings.TrimSpace(name)
	if !tagNameShape.MatchString(name) {
		return store.Tag{}, "a tag is 1 to 32 letters, digits, spaces or . _ : / + - and starts with a letter or digit"
	}
	color = strings.TrimSpace(color)
	if color == "" {
		h := fnv.New32a()
		_, _ = h.Write([]byte(strings.ToLower(name)))
		color = tagColors[int(h.Sum32()%uint32(len(tagColors)))]
	}
	if !tagColorShape.MatchString(color) {
		return store.Tag{}, "a colour is #rrggbb"
	}
	desc = strings.TrimSpace(desc)
	if len([]rune(desc)) > 120 {
		return store.Tag{}, "a description is at most 120 characters"
	}
	return store.Tag{Name: name, Color: strings.ToLower(color), Description: desc}, ""
}

// hostTagIndex is every host's tags, its own first then its probe's (by name), with their colours.
func (s *Server) hostTagIndex(ctx context.Context) (map[string][]hostTag, error) {
	tags, err := s.st.ListTags(ctx)
	if err != nil {
		return nil, err
	}
	color := map[string]string{}
	for _, t := range tags {
		color[t.Name] = t.Color
	}
	own, err := s.st.HostTags(ctx)
	if err != nil {
		return nil, err
	}
	probe, err := s.st.ProbeTags(ctx)
	if err != nil {
		return nil, err
	}
	out := map[string][]hostTag{}
	if len(own) == 0 && len(probe) == 0 {
		return out, nil
	}
	idx, err := s.hostIndex(ctx)
	if err != nil {
		return nil, err
	}
	var names map[string]string
	if len(probe) > 0 {
		names = s.proxyNames(ctx)
	}
	for id, h := range idx {
		seen := map[string]bool{}
		var ts []hostTag
		for _, t := range own[id] {
			seen[t] = true
			ts = append(ts, hostTag{Name: t, Color: color[t]})
		}
		for _, t := range probe[h.ProxyID] {
			if !seen[t] {
				seen[t] = true
				from := names[h.ProxyID]
				if from == "" {
					from = "probe " + h.ProxyID // never "", which would read as the host's own
				}
				ts = append(ts, hostTag{Name: t, Color: color[t], From: from})
			}
		}
		if len(ts) > 0 {
			out[id] = ts
		}
	}
	return out, nil
}

// tagNames reduces a host's tags to their names, for routing.
func tagNames(ts []hostTag) []string {
	out := make([]string, 0, len(ts))
	for _, t := range ts {
		out = append(out, t.Name)
	}
	return out
}

type tagView struct {
	Name        string `json:"name"`
	Color       string `json:"color"`
	Description string `json:"description,omitempty"`
	Hosts       int    `json:"hosts"`  // hosts carrying it, their own or from their probe
	Probes      int    `json:"probes"` // probes carrying it
}

// GET /api/tags: every tag with how many hosts and probes carry it.
func (s *Server) handleTags(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 12*time.Second)
	defer cancel()
	tags, err := s.st.ListTags(ctx)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not read the tags"})
		return
	}
	hosts := map[string]int{}
	if idx, err := s.hostTagIndex(ctx); err == nil {
		for _, ts := range idx {
			for _, t := range ts {
				hosts[t.Name]++
			}
		}
	}
	probes := map[string]int{}
	if pt, err := s.st.ProbeTags(ctx); err == nil {
		for _, ts := range pt {
			for _, t := range ts {
				probes[t]++
			}
		}
	}
	out := make([]tagView, 0, len(tags))
	for _, t := range tags {
		out = append(out, tagView{Name: t.Name, Color: t.Color, Description: t.Description, Hosts: hosts[t.Name], Probes: probes[t.Name]})
	}
	writeJSON(w, http.StatusOK, out)
}

type tagRequest struct {
	Name        string `json:"name"`
	Color       string `json:"color"`
	Description string `json:"description"`
}

// POST /api/tags (admin): a new tag.
func (s *Server) handleCreateTag(w http.ResponseWriter, r *http.Request) {
	var req tagRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}
	t, msg := cleanTag(req.Name, req.Color, req.Description)
	if msg != "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": msg})
		return
	}
	if err := s.st.CreateTag(r.Context(), t); err != nil {
		if errors.Is(err, store.ErrTagExists) {
			writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not save the tag"})
		return
	}
	changeObject(r, t.Name)
	writeJSON(w, http.StatusOK, tagView{Name: t.Name, Color: t.Color, Description: t.Description})
}

// PATCH /api/tags/{name} (admin): rename it, or change its colour or description.
func (s *Server) handleUpdateTag(w http.ResponseWriter, r *http.Request) {
	old := r.PathValue("name")
	var req tagRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}
	t, msg := cleanTag(req.Name, req.Color, req.Description)
	if msg != "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": msg})
		return
	}
	var was store.Tag
	if all, err := s.st.ListTags(r.Context()); err == nil {
		for _, x := range all {
			if x.Name == old {
				was = x
			}
		}
	}
	if err := s.st.UpdateTag(r.Context(), old, t); err != nil {
		switch {
		case errors.Is(err, store.ErrTagExists):
			writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
		case errors.Is(err, store.ErrNotFound):
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such tag"})
		default:
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not save the tag"})
		}
		return
	}
	changeObject(r, old)
	changeDiff(r, "Name", was.Name, t.Name)
	changeDiff(r, "Colour", was.Color, t.Color)
	changeDiff(r, "Description", was.Description, t.Description)
	writeJSON(w, http.StatusOK, tagView{Name: t.Name, Color: t.Color, Description: t.Description})
}

// DELETE /api/tags/{name} (admin): drop it from every host, probe and channel.
func (s *Server) handleDeleteTag(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if err := s.st.DeleteTag(r.Context(), name); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not delete the tag"})
		return
	}
	changeObject(r, name)
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

// knownTags checks a list against the tags that exist and drops repeats.
func (s *Server) knownTags(ctx context.Context, in []string) ([]string, string) {
	tags, err := s.st.ListTags(ctx)
	if err != nil {
		return nil, "could not read the tags"
	}
	known := map[string]bool{}
	for _, t := range tags {
		known[t.Name] = true
	}
	seen := map[string]bool{}
	var out []string
	for _, t := range in {
		t = strings.TrimSpace(t)
		if t == "" || seen[t] {
			continue
		}
		if !known[t] {
			return nil, "there's no tag " + t + ": make it in Settings, Tags first"
		}
		seen[t] = true
		out = append(out, t)
	}
	sort.Strings(out)
	return out, ""
}

// PUT /api/proxies/{id}/tags (admin): a probe's tags, which every host it monitors carries.
func (s *Server) handleSetProbeTags(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Tags []string `json:"tags"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 12*time.Second)
	defer cancel()
	tags, msg := s.knownTags(ctx, req.Tags)
	if msg != "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": msg})
		return
	}
	id := r.PathValue("id")
	was, _ := s.st.ProbeTags(ctx)
	if err := s.st.SetProbeTags(ctx, id, tags); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not save the tags"})
		return
	}
	if idx, err := s.hostIndex(ctx); err == nil {
		var hosts []string
		for hid, h := range idx {
			if h.ProxyID == id {
				hosts = append(hosts, hid)
			}
		}
		changeObject(r, s.proxyNames(ctx)[id], hosts...)
		changeDetail(r, "on its "+plural(len(hosts), "host", "hosts"))
	}
	changeDiff(r, "Tags", strings.Join(was[id], ", "), strings.Join(tags, ", "))
	writeJSON(w, http.StatusOK, map[string]any{"tags": nonNilStrings(tags)})
}

func nonNilStrings(v []string) []string {
	if v == nil {
		return []string{}
	}
	return v
}
