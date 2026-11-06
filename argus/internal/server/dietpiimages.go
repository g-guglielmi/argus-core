// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package server

import (
	"context"
	"net/http"
	"sync"
	"time"
)

// dietpiImage is one DietPi image the DietPi method of Add probe links (DESIGN 14a): DietPi's own
// download from dietpi.com, never a copy of ours. Its size and build date come from the download
// server (a HEAD request, with the GitHub lookups): DietPi's image names carry no version.
type dietpiImage struct {
	ID    string `json:"id"`    // "rpi234", "rpi5", "neo3", "neo3plus", "pc"
	Label string `json:"label"` // what it is for
	Name  string `json:"name"`  // the file name
	URL   string `json:"url"`
	Note  string `json:"note,omitempty"` // why this image, when it is a stand-in for the board's own
	Size  int64  `json:"size,omitempty"` // bytes, 0 when unknown
	Date  int64  `json:"date,omitempty"` // when it was built (Last-Modified, unix seconds), 0 when unknown

	// own is the board's own image when the one above is a stand-in: once dietpi.com has it, it
	// takes the stand-in's place (and the note goes).
	own *dietpiOwn
}

type dietpiOwn struct{ Name, URL string }

const dietpiImageURL = "https://dietpi.com/downloads/images/"

// dietpiImages are the images the wizard offers, Debian 13 based: a 64-bit Raspberry Pi or NanoPi
// (the probe image is arm64) and an x86-64 mini PC with UEFI (written by the Argus probe installer
// stick).
var dietpiImages = []dietpiImage{
	{ID: "rpi234", Label: "Raspberry Pi 3, 4 or Zero 2 W", Name: "DietPi_RPi234-ARMv8-Trixie.img.xz",
		URL: dietpiImageURL + "DietPi_RPi234-ARMv8-Trixie.img.xz"},
	{ID: "rpi5", Label: "Raspberry Pi 5", Name: "DietPi_RPi5-ARMv8-Trixie.img.xz",
		URL: dietpiImageURL + "DietPi_RPi5-ARMv8-Trixie.img.xz"},
	{ID: "neo3", Label: "NanoPi NEO3", Name: "DietPi_NanoPiNEO3-ARMv8-Trixie.img.xz",
		URL: dietpiImageURL + "DietPi_NanoPiNEO3-ARMv8-Trixie.img.xz"},
	// DietPi has no NEO3 Plus image yet (MichaIng/DietPi discussion 8390): its NanoPi Zero2 image runs
	// the board (same RK3528A; USB 3 runs at USB 2 speed, which a probe doesn't use).
	{ID: "neo3plus", Label: "NanoPi NEO3 Plus", Name: "DietPi_NanoPiZero2-ARMv8-Trixie.img.xz",
		URL:  dietpiImageURL + "DietPi_NanoPiZero2-ARMv8-Trixie.img.xz",
		Note: "DietPi's NanoPi Zero2 image, which runs the NEO3 Plus until DietPi publishes one of its own",
		own:  &dietpiOwn{Name: "DietPi_NanoPiNEO3Plus-ARMv8-Trixie.img.xz", URL: dietpiImageURL + "DietPi_NanoPiNEO3Plus-ARMv8-Trixie.img.xz"}},
	{ID: "pc", Label: "Mini PC (x86-64, UEFI)", Name: "DietPi_NativePC-UEFI-x86_64-Trixie.img.xz",
		URL: dietpiImageURL + "DietPi_NativePC-UEFI-x86_64-Trixie.img.xz"},
}

// headDietPiImages asks the download server about each image at once. A stand-in is replaced by the
// board's own image as soon as that exists. One it can't reach keeps its name and link, without size
// or date: the wizard then shows just those.
func headDietPiImages(ctx context.Context, imgs []dietpiImage) []dietpiImage {
	out := append([]dietpiImage(nil), imgs...)
	var wg sync.WaitGroup
	for i := range out {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if o := out[i].own; o != nil {
				if size, date, ok := headImage(ctx, o.URL); ok {
					out[i].Name, out[i].URL, out[i].Note, out[i].Size, out[i].Date = o.Name, o.URL, "", size, date
					return
				}
			}
			if size, date, ok := headImage(ctx, out[i].URL); ok {
				out[i].Size, out[i].Date = size, date
			}
		}()
	}
	wg.Wait()
	return out
}

// headImage is one HEAD to the download server: the size and Last-Modified of a file it has.
func headImage(ctx context.Context, url string) (size, date int64, ok bool) {
	c, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(c, http.MethodHead, url, nil)
	if err != nil {
		return 0, 0, false
	}
	req.Header.Set("User-Agent", "argus")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, 0, false
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, 0, false
	}
	if resp.ContentLength > 0 {
		size = resp.ContentLength
	}
	if t, err := http.ParseTime(resp.Header.Get("Last-Modified")); err == nil {
		date = t.Unix()
	}
	return size, date, true
}
