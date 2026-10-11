import React from 'react'
import { createRoot } from 'react-dom/client'
import Login from './Login.jsx'
createRoot(document.getElementById('root')).render(<Login onLogin={()=>{ localStorage.getItem('nms_must_change') ? location.href='/change-password.html' : alert('Login OK (preview) — next: dashboard')}} />)
