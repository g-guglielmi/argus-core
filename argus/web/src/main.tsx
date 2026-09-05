import React from 'react'
import ReactDOM from 'react-dom/client'
import './theme.css'
import App from './App'
import { DialogProvider } from './dialog'
import { ToastProvider } from './toast'

ReactDOM.createRoot(document.getElementById('root')!).render(
  <React.StrictMode>
    <ToastProvider>
      <DialogProvider>
        <App />
      </DialogProvider>
    </ToastProvider>
  </React.StrictMode>,
)
