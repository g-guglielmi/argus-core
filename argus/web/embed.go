// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package web

import "embed"

// Dist holds the built React frontend. The `dist` directory is produced by
// `npm run build` (Vite) and baked into the Go binary at compile time.
// A committed .gitkeep keeps `go build` working before a frontend build; CI
// overwrites the directory with the real assets.
//
//go:embed all:dist
var Dist embed.FS
