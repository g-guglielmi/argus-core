// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

// Vite's client types: the side-effect imports of stylesheets (theme.css, uPlot's) have no module
// of their own; TypeScript 7 checks such imports and takes this declaration for them.
/// <reference types="vite/client" />
