// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'

// base: './' keeps asset URLs relative so the SPA works behind any path/reverse proxy.
// The dev proxy forwards API/health calls to a locally running Go backend on :8080.
export default defineConfig({
  base: './',
  plugins: [react()],
  build: {
    // Fonts always ship as files: the CSP allows font-src 'self' only, so a small font (the
    // one-glyph Figtree file) inlined as a data: URL would be refused.
    assetsInlineLimit: (file) => (file.endsWith('.woff2') ? false : undefined),
  },
  server: {
    proxy: {
      '/api': 'http://localhost:8080',
      '/healthz': 'http://localhost:8080',
    },
  },
})
