import React from 'react'
import { createRoot } from 'react-dom/client'
import './tokens.css'
import './app.css'
import App from './App'
import { watchSystemTheme } from './theme'

const el = document.getElementById('root')
if (!el) throw new Error('#root missing from index.html')

watchSystemTheme()

createRoot(el).render(
  <React.StrictMode>
    <App />
  </React.StrictMode>,
)
