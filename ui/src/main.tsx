import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import './design/tokens.css'
import './design/base.css'
import './design/components.css'
import './shell/shell.css'
import { App } from './app'

createRoot(document.getElementById('root')!).render(
  <StrictMode>
    <App />
  </StrictMode>,
)
