import { useEffect, useState } from 'react'

export default function Login({ onLogin }) {
  const [setup, setSetup] = useState(null) // {firstSetup, defaultUser, defaultPass} or null
  const [u, setU] = useState('')
  const [p, setP] = useState('')
  const [err, setErr] = useState('')

  useEffect(() => {
    // cek first-setup: backend jika ada, fallback localStorage untuk preview tanpa backend
    fetch('/api/setup-status').then(r=>r.json()).then(d=>{
      if (d.firstSetup) setSetup(d)
      else {
        const done = localStorage.getItem('nms_setup_done')
        if (!done) setSetup({ firstSetup:true, defaultUser:'superadmin', defaultPass:'Admin@123', source:'local' })
      }
    }).catch(()=>{
      const done = localStorage.getItem('nms_setup_done')
      if (!done) setSetup({ firstSetup:true, defaultUser:'superadmin', defaultPass:'Admin@123', source:'local' })
    })
  }, [])

  function submit(e){
    e.preventDefault()
    if(!u||!p){ setErr('Isi username & password'); return }
    // dummy: jika superadmin/Admin@123 pertama kali, tandai akan wajib ganti
    fetch('/api/login', {method:'POST', headers:{'Content-Type':'application/json'}, body:JSON.stringify({u,p})})
      .then(r=>r.json().catch(()=>({ok:false})))
      .then(d=>{
        if(d.must_change_password){ localStorage.setItem('nms_must_change','1'); onLogin?.(d) }
        else if(d.ok||d.token){ localStorage.setItem('nms_setup_done','1'); onLogin?.(d) }
        else setErr(d.error||'Login gagal (preview: coba superadmin / Admin@123)')
      }).catch(()=>{
        // preview mode tanpa backend
        if(u==='superadmin' && p==='Admin@123'){
          if(!localStorage.getItem('nms_setup_done')){ localStorage.setItem('nms_must_change','1'); onLogin?.({preview:true}) }
          else { localStorage.setItem('nms_setup_done','1'); onLogin?.({preview:true}) }
        } else setErr('Preview tanpa backend — coba superadmin / Admin@123')
      })
  }

  return (
    <div style={{maxWidth:420, margin:'6vh auto', fontFamily:'system-ui', padding:16}}>
      <h1 style={{fontSize:22, fontWeight:800}}>NMS — Multi-Device Management</h1>
      <p style={{color:'#64748B', fontSize:13}}>Login untuk akses dashboard monitoring & remote</p>

      {setup?.firstSetup && (
        <div style={{background:'#FEF3C7', border:'1px solid #FDE68A', borderRadius:12, padding:'12px 14px', margin:'14px 0'}}>
          <div style={{fontWeight:800, fontSize:13, color:'#92400E'}}>⚠️ Login awal — kredensial default masih aktif</div>
          <div style={{fontSize:13, marginTop:6}}>
            Username: <code style={{background:'#fff', padding:'2px 6px', borderRadius:6}}>{setup.defaultUser}</code> &nbsp;
            Password: <code style={{background:'#fff', padding:'2px 6px', borderRadius:6}}>{setup.defaultPass}</code>
          </div>
          <div style={{fontSize:12, color:'#92400E', marginTop:6}}>Segera ganti setelah login pertama. Banner ini otomatis hilang setelah password diganti.</div>
          {setup.source==='local' && <div style={{fontSize:11, color:'#a16207'}}>(preview lokal — backend belum jalan)</div>}
        </div>
      )}

      <form onSubmit={submit} style={{background:'#fff', border:'1px solid #E2E8F0', borderRadius:16, padding:16}}>
        {err && <div style={{background:'#FEE2E2', border:'1px solid #FCA5A5', color:'#991B1B', borderRadius:8, padding:'8px 10px', fontSize:13, marginBottom:10}}>{err}</div>}
        <label style={{fontSize:13, fontWeight:600}}>Username<input value={u} onChange={e=>setU(e.target.value)} placeholder="superadmin" style={inp} /></label>
        <label style={{fontSize:13, fontWeight:600}}>Password<input type="password" value={p} onChange={e=>setP(e.target.value)} placeholder="••••••••" style={inp} /></label>
        <button type="submit" style={{width:'100%', background:'#0A2463', color:'#fff', border:0, borderRadius:10, padding:'10px 14px', fontWeight:700, cursor:'pointer', marginTop:8}}>Masuk</button>
        <div style={{fontSize:11, color:'#94A3B8', marginTop:8, textAlign:'center'}}>Default first-time: superadmin / Admin@123 (hilang setelah diganti)</div>
      </form>
    </div>
  )
}
const inp = { width:'100%', padding:'8px 10px', border:'1.5px solid #CBD5E1', borderRadius:8, margin:'6px 0 10px', fontSize:14 }
