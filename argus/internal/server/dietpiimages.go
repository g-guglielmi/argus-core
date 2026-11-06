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
	ID    string `json:"id"`    // "rpi234", "rpi5", "pc"
	Label string `json:"label"` // what it is for
	Name  string `json:"name"`  // the file name
	URL   string `json:"url"`
	Size  int64  `json:"size,omitempty"` // bytes, 0 when unknown
	Date  int64  `json:"date,omitempty"` // when it was built (Last-Modified, unix seconds), 0 when unknown
}

// dietpiImages are the images the wizard offers: a 64-bit Raspberry Pi (the probe image is arm64) and
// an x86-64 mini PC with UEFI (written by the Argus probe installer stick), Debian 13 based.
var dietpiImages = []dietpiImage{
	{ID: "rpi234", Label: "Raspberry Pi 3, 4 or Zero 2 W", Name: "DietPi_RPi234-ARMv8-Trixie.img.xz",
		URL: "https://dietpi.com/downloads/images/DietPi_RPi234-ARMv8-Trixie.img.xz"},
	{ID: "rpi5", Label: "Raspberry Pi 5", Name: "DietPi_RPi5-ARMv8-Trixie.img.xz",
		URL: "https://dietpi.com/downloads/images/DietPi_RPi5-ARMv8-Trixie.img.xz"},
	{ID: "pc", Label: "Mini PC (x86-64, UEFI)", Name: "DietPi_NativePC-UEFI-x86_64-Trixie.img.xz",
		URL: "https://dietpi.com/downloads/images/DietPi_NativePC-UEFI-x86_64-Trixie.img.xz"},
}

// headDietPiImages asks the download server about each image at once. One it can't reach keeps its
// name and link, without size or date: the wizard then shows just those.
func headDietPiImages(ctx context.Context, imgs []dietpiImage) []dietpiImage {
	out := append([]dietpiImage(nil), imgs...)
	var wg sync.WaitGroup
	for i := range out {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c, cancel := context.WithTimeout(ctx, 10*time.Second)
			defer cancel()
			req, err := http.NewRequestWithContext(c, http.MethodHead, out[i].URL, nil)
			if err != nil {
				return
			}
			req.Header.Set("User-Agent", "argus")
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				return
			}
			resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				return
			}
			if resp.ContentLength > 0 {
				out[i].Size = resp.ContentLength
			}
			if t, err := http.ParseTime(resp.Header.Get("Last-Modified")); err == nil {
				out[i].Date = t.Unix()
			}
		}()
	}
	wg.Wait()
	return out
}
