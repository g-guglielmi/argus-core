// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

import React from 'react'
import ReactDOM from 'react-dom/client'
import './theme.css'
import App from './App'
import { DialogProvider } from './dialog'
import { ToastProvider } from './toast'
import { ErrorBoundary } from './ui'

ReactDOM.createRoot(document.getElementById('root')!).render(
  <React.StrictMode>
    {/* The last line: an error the page's own boundary can't hold (the shell, a dialog) still leaves a way out. */}
    <ErrorBoundary full>
      <ToastProvider>
        <DialogProvider>
          <App />
        </DialogProvider>
      </ToastProvider>
    </ErrorBoundary>
  </React.StrictMode>,
)
