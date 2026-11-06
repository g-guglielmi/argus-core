// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// The download server's size and Last-Modified land on each image; one it can't answer for keeps
// its name and link only.
func TestHeadDietPiImages(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodHead {
			t.Errorf("method %s, want HEAD", r.Method)
		}
		if r.URL.Path == "/missing.img.xz" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Last-Modified", "Thu, 22 Oct 2026 15:21:23 GMT")
		w.Header().Set("Content-Length", "299179372")
	}))
	defer srv.Close()
	got := headDietPiImages(context.Background(), []dietpiImage{
		{ID: "pc", Name: "a.img.xz", URL: srv.URL + "/a.img.xz"},
		{ID: "gone", Name: "missing.img.xz", URL: srv.URL + "/missing.img.xz"},
	})
	if got[0].Size != 299179372 || got[0].Date != 1792682483 {
		t.Fatalf("pc: %+v", got[0])
	}
	if got[1].Size != 0 || got[1].Date != 0 || got[1].URL == "" {
		t.Fatalf("a missing image: %+v", got[1])
	}
}
