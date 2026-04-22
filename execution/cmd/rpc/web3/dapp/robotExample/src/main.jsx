import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import './index.css'
import App from './App.jsx'
import { RobotProvider } from './contexts/RobotContext.jsx'

createRoot(document.getElementById('root')).render(
  <StrictMode>
    <RobotProvider>
      <App />
    </RobotProvider>
  </StrictMode>,
)
