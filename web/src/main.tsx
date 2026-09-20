import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import { App } from './App'
import { SharePage } from './features/share/SharePage'
import { applyStoredTheme } from './components/theme'
import './styles/index.css'

applyStoredTheme()

// The application shell is cached for offline start (WEB-N8); the share page and the API never are.
if ('serviceWorker' in navigator && import.meta.env.PROD && window.location.pathname !== '/s') {
  window.addEventListener('load', () => void navigator.serviceWorker.register('/sw.js').catch(() => undefined))
}

const root = document.getElementById('root')
if (!root) throw new Error('missing #root element')

createRoot(root).render(
  <StrictMode>
    {window.location.pathname === '/s' ? <SharePage /> : <App />}
  </StrictMode>,
)
