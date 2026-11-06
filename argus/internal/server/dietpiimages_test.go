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
// its name and link only; a stand-in gives way to the board's own image once that exists.
func TestHeadDietPiImages(t *testing.T) {
	published := map[string]bool{"/a.img.xz": true, "/zero2.img.xz": true}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodHead {
			t.Errorf("method %s, want HEAD", r.Method)
		}
		if !published[r.URL.Path] {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Last-Modified", "Thu, 22 Oct 2026 15:21:23 GMT")
		w.Header().Set("Content-Length", "299179372")
	}))
	defer srv.Close()
	imgs := []dietpiImage{
		{ID: "pc", Name: "a.img.xz", URL: srv.URL + "/a.img.xz"},
		{ID: "gone", Name: "missing.img.xz", URL: srv.URL + "/missing.img.xz"},
		{ID: "neo3plus", Name: "zero2.img.xz", URL: srv.URL + "/zero2.img.xz", Note: "a stand-in",
			own: &dietpiOwn{Name: "neo3plus.img.xz", URL: srv.URL + "/neo3plus.img.xz"}},
	}
	got := headDietPiImages(context.Background(), imgs)
	if got[0].Size != 299179372 || got[0].Date != 1792682483 {
		t.Fatalf("pc: %+v", got[0])
	}
	if got[1].Size != 0 || got[1].Date != 0 || got[1].URL == "" {
		t.Fatalf("a missing image: %+v", got[1])
	}
	if got[2].Name != "zero2.img.xz" || got[2].Note == "" || got[2].Size == 0 {
		t.Fatalf("no own image yet, the stand-in stays: %+v", got[2])
	}

	published["/neo3plus.img.xz"] = true
	got = headDietPiImages(context.Background(), imgs)
	if got[2].Name != "neo3plus.img.xz" || got[2].URL != srv.URL+"/neo3plus.img.xz" || got[2].Note != "" || got[2].Size == 0 {
		t.Fatalf("the board's own image is out, it takes over: %+v", got[2])
	}
	if imgs[2].Name != "zero2.img.xz" {
		t.Fatal("the list itself must not change")
	}
}
