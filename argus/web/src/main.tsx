import React from 'react'
import ReactDOM from 'react-dom/client'
import './theme.css'
import App from './App'
import { DialogProvider } from './dialog'

ReactDOM.createRoot(document.getElementById('root')!).render(
  <React.StrictMode>
    <DialogProvider>
      <App />
    </DialogProvider>
  </React.StrictMode>,
)
