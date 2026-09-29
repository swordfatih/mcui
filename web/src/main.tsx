import React, { useEffect, useState } from 'react'
import { createRoot } from 'react-dom/client'
import { QueryClient, QueryClientProvider, useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import axios from 'axios'
import './style.css'
import ServerDashboard from './ServerDashboard'
import PackDetail from './PackDetail'
import NotificationBell from './NotificationBell'
import { BrowserRouter, Link, Route, Routes, useNavigate, useParams } from 'react-router'

type Edition = 'bedrock' | 'java'
type Server = { name: string; displayName?: string; iconUrl?: string; edition: Edition; status: string; port?: number; host?: string; protocol?: string }
type CreateServer = { name: string; edition: Edition; port: number; worldPath: string; backupPath: string; backupUrl: string; acceptEula: boolean }
type ImportSource = 'none' | 'world' | 'file' | 'path' | 'drive'
type BackupConfig = { configured: boolean; interval: string; destination: string }
type BackupState = { state: 'idle' | 'queued' | 'capturing' | 'uploading' | 'complete' | 'failed'; startedAt?: string; completedAt?: string; snapshotId?: string; error?: string }
const api = axios.create({ baseURL: '/api' })
const queryClient = new QueryClient()
api.interceptors.response.use(response => response, error => {
  if (axios.isAxiosError(error) && error.response?.status === 401 && !error.config?.url?.startsWith('/auth/')) {
    queryClient.setQueryData(['session'], { authenticated: false })
  }
  return Promise.reject(error)
})
function errorMessage(error: unknown) { return axios.isAxiosError(error) ? (error.response?.data?.error ?? error.message) : String(error) }

function Login({ onSuccess }: { onSuccess: () => void }) {
  const [password, setPassword] = useState('')
  const [message, setMessage] = useState('')
  const login = useMutation({
    mutationFn: async () => api.post('/auth/login', { password }),
    onSuccess: () => {
      setPassword('')
      onSuccess()
    },
    onError: error => setMessage(errorMessage(error)),
  })

  function submit(event: React.FormEvent) {
    event.preventDefault()
    setMessage('')
    login.mutate()
  }

  return (
    <div className="login-page">
      <div className="login-glow" />
      <div className="login-card">
        <div className="login-mark"><img src="/icons/icon-192.png?v=grass-block-1" alt="Minecraft grass block" /></div>
        <p className="kicker">MINECRAFT SERVER CONTROL</p>
        <h1>Welcome back.</h1>
        <p className="muted">Enter your password to manage your worlds.</p>
        <form onSubmit={submit}>
          <label>
            Password
            <input type="password" autoComplete="current-password" autoFocus required value={password}
              onChange={event => setPassword(event.target.value)} placeholder="Your dashboard password" />
          </label>
          {message && <p className="error" role="alert">{message}</p>}
          <button className="primary" type="submit" disabled={login.isPending}>
            {login.isPending ? 'Signing in…' : 'Sign in'} <span>→</span>
          </button>
        </form>
      </div>
      <p className="login-footer">MCUI · Your worlds, one place</p>
    </div>
  )
}

function AuthGate() {
  const qc = useQueryClient()
  const session = useQuery({
    queryKey: ['session'],
    queryFn: async () => (await api.get<{ authenticated: boolean; enabled: boolean }>('/auth/session')).data,
    retry: false,
  })

  if (session.isLoading) {
    return <div className="login-page"><div className="login-mark"><img src="/icons/icon-192.png?v=grass-block-1" alt="Minecraft grass block" /></div></div>
  }
  if (session.isError) {
    return (
      <div className="login-page">
        <div className="login-card">
          <h1>Connection unavailable</h1>
          <p className="muted">Could not reach MCUI.</p>
          <button className="primary" onClick={() => session.refetch()}>Try again <span>→</span></button>
        </div>
      </div>
    )
  }
  if (!session.data?.authenticated) {
    return <Login onSuccess={() => qc.setQueryData(['session'], { authenticated: true, enabled: true })} />
  }
  return <App authEnabled={session.data.enabled} />
}

function SignOutButton() {
  const qc = useQueryClient()

  async function signOut() {
    await api.post('/auth/logout')
    qc.removeQueries({ predicate: query => query.queryKey[0] !== 'session' })
    qc.setQueryData(['session'], { authenticated: false, enabled: true })
  }

  return <button className="sign-out" onClick={signOut}>Sign out</button>
}

function ServerCard({ server }: { server: Server }) {
  const { data: backup } = useQuery({ queryKey: ['backup', server.name], queryFn: async () => (await api.get<BackupState>(`/servers/${encodeURIComponent(server.name)}/backup`)).data, refetchInterval: 5000 })
  const backupStatus = backup?.state === 'failed' ? 'Backup failed' : backup?.state === 'complete' ? 'Backed up' : backup?.state === 'capturing' || backup?.state === 'uploading' ? 'Backing up' : 'No backup yet'
  return <div className="server-card-container"><Link className={`server-card ${server.status === 'running' ? 'server-card-running' : server.status === 'stopped' ? 'server-card-stopped' : ''}`} to={`/servers/${encodeURIComponent(server.name)}`} aria-label={`Open ${server.displayName || server.name} server dashboard`}><span className="server-symbol">{server.iconUrl ? <img src={server.iconUrl} alt="" /> : server.edition === 'bedrock' ? 'B' : 'J'}</span><span className="server-card-copy"><strong>{server.displayName || server.name}</strong><small>{server.edition === 'bedrock' ? 'Bedrock · UDP' : 'Java · TCP'}{server.port ? ` · ${server.port}` : ''}</small></span><span className="server-card-state"><span className={`status-pill ${server.status}`}><i/>{server.status}</span><small>{backupStatus}</small></span><span className="server-card-arrow" aria-hidden="true">→</span></Link><NotificationBell server={server.name} label={server.displayName || server.name} /></div>
}
function ServerDetailRoute({ servers, loading, error, backupConfigured }: { servers: Server[]; loading: boolean; error: unknown; backupConfigured: boolean }) {
  const { name } = useParams()
  const navigate = useNavigate()
  const title = servers.find(server => server.name === name)?.displayName || name
  useEffect(() => { document.title = title ? `${title} · MCUI` : 'MCUI'; return () => { document.title = 'MCUI' } }, [title])
  if (loading) return <main className="route-state"><p className="muted">Loading server…</p></main>
  if (error) return <main className="route-state"><h1>Connection unavailable</h1><p className="error">{errorMessage(error)}</p></main>
  const server = servers.find(item => item.name === name)
  if (!server) return <main className="route-state"><h1>Server not found</h1><p className="muted">This server may have been moved or removed.</p><Link to="/">← All servers</Link></main>
  return <ServerDashboard server={server} backupConfigured={backupConfigured} onBack={() => navigate('/')} />
}
function PackDetailRoute({ servers, loading }: { servers: Server[]; loading: boolean }) {
  const { name, kind, folder } = useParams()
  const server = servers.find(item => item.name === name)
  useEffect(() => { document.title = folder && name ? `${folder} · ${name} · MCUI` : 'MCUI'; return () => { document.title = 'MCUI' } }, [folder, name])
  if (loading) return <main className="route-state"><p className="muted">Loading pack…</p></main>
  if (!server || server.edition !== 'bedrock' || (kind !== 'resource' && kind !== 'behavior') || !folder) return <main className="route-state"><h1>Pack not found</h1><Link to={name ? `/servers/${encodeURIComponent(name)}?tab=resources` : '/'}>← Resources</Link></main>
  return <PackDetail server={server.name} kind={kind} folder={folder} stopped={server.status === 'stopped'} />
}
function App({ authEnabled }: { authEnabled: boolean }) {
  const qc = useQueryClient()
  const { data: servers = [], isLoading, error } = useQuery({ queryKey: ['servers'], queryFn: async () => (await api.get<Server[]>('/servers')).data, refetchInterval: 5000 })
  const { data: backupConfig } = useQuery({ queryKey: ['backup-config'], queryFn: async () => (await api.get<BackupConfig>('/backups/config')).data, refetchInterval: 30000 })
  const [form, setForm] = useState<CreateServer>({ name: '', edition: 'bedrock', port: 19132, worldPath: '', backupPath: '', backupUrl: '', acceptEula: false })
  const [source, setSource] = useState<ImportSource>('none')
  const [backupFile, setBackupFile] = useState<File | null>(null)
  const [message, setMessage] = useState('')
  const create = useMutation({ mutationFn: async (value: CreateServer) => {
    if (source === 'file' && backupFile) {
      const upload = new FormData()
      for (const [key, field] of Object.entries(value)) upload.append(key, String(field))
      upload.append('backupFile', backupFile)
      return (await api.post<Server>('/servers', upload)).data
    }
    return (await api.post<Server>('/servers', value)).data
  }, onSuccess: async (server) => { setMessage(`${server.name} created. Start it when ready.`); setForm({ name: '', edition: 'bedrock', port: 19132, worldPath: '', backupPath: '', backupUrl: '', acceptEula: false }); setBackupFile(null); setSource('none'); await qc.invalidateQueries({ queryKey: ['servers'] }) }, onError: e => setMessage(errorMessage(e)) })
  return <div className="shell"><header><Link className="brand" to="/"><span className="brand-icon"><img src="/icons/icon-192.png?v=grass-block-1" alt="" /></span><span>MCUI</span></Link>{authEnabled && <SignOutButton />}</header><Routes><Route path="/" element={<main><section className="intro"><div><p className="kicker">YOUR SERVERS</p><h1>Make room for your next world.</h1><p className="muted">Create, start, back up, and stop Minecraft servers from one place. Each server keeps its own Compose file and world data.</p></div><div className="count"><strong>{servers.length}</strong><span>{servers.length === 1 ? 'server' : 'servers'}</span></div></section><div className="grid"><section className="panel servers"><div className="section-heading"><div><p className="kicker">OVERVIEW</p><h2>Servers</h2></div><span className="live"><i/>Refreshes every 5s</span></div><div className="backup-banner"><strong>Google Drive backups</strong><span>{backupConfig?.configured ? `Ready · ${backupConfig.interval === '0s' ? 'manual only' : `scheduled every ${backupConfig.interval}`}` : 'Setup required: add rclone.conf to backup/'}</span></div>{isLoading ? <p className="muted">Loading servers…</p> : error ? <p className="error">{errorMessage(error)}</p> : servers.length === 0 ? <div className="empty"><div className="empty-icon">▦</div><h3>No servers yet</h3><p>Set up your first Bedrock or Java server using the form.</p></div> : <div className="server-list">{servers.map(server => <ServerCard key={server.name} server={server}/>)}</div>}</section><section className="panel create">
  <p className="kicker">NEW SERVER</p><h2>Create a server</h2>
  <p className="muted form-intro">Start fresh, import a world, or create from an MCUI backup.</p>
  <form onSubmit={e => {
    e.preventDefault()
    setMessage('')
    if (source === 'file' && !backupFile) { setMessage('Select a backup file.'); return }
    create.mutate({ ...form, worldPath: source === 'world' ? form.worldPath : '', backupPath: source === 'path' ? form.backupPath : '', backupUrl: source === 'drive' ? form.backupUrl : '' })
  }}>
    <label>Edition<div className="edition-options"><button type="button" className={form.edition === 'bedrock' ? 'selected' : ''} onClick={() => setForm({ ...form, edition: 'bedrock', port: 19132 })}>Bedrock <small>UDP</small></button><button type="button" className={form.edition === 'java' ? 'selected' : ''} onClick={() => setForm({ ...form, edition: 'java', port: 25565 })}>Java <small>TCP</small></button></div></label>
    <label>Server name<input required minLength={1} placeholder="my-survival-world" value={form.name} onChange={e => setForm({ ...form, name: e.target.value })}/><small>Used as the server folder name.</small></label>
    <label>Host port<input required type="number" min="1" max="65535" value={form.port} onChange={e => setForm({ ...form, port: Number(e.target.value) })}/></label>
    <label>Import source<select value={source} onChange={e => setSource(e.target.value as ImportSource)}><option value="none">Start fresh</option><option value="world">Existing world on host</option><option value="file">Upload backup file</option><option value="path">Backup file on host</option><option value="drive">Google Drive file link</option></select></label>
    {source === 'world' && <label>Existing world path<input required placeholder="/absolute/path/to/world.zip" value={form.worldPath} onChange={e => setForm({ ...form, worldPath: e.target.value })}/><small>Absolute path on this host to a world folder, ZIP, or tar.gz.</small></label>}
    {source === 'file' && <label>MCUI backup file<input required type="file" accept=".tar.gz,.tgz" onChange={e => setBackupFile(e.target.files?.[0] ?? null)}/><small>Upload a .tar.gz backup from your computer. Large uploads may take a while.</small></label>}
    {source === 'path' && <label>Backup file path<input required placeholder="/absolute/path/to/backup.tar.gz" value={form.backupPath} onChange={e => setForm({ ...form, backupPath: e.target.value })}/><small>Absolute path accessible to MCUI.</small></label>}
    {source === 'drive' && <label>Google Drive file link<input required type="url" placeholder="https://drive.google.com/file/d/.../view" value={form.backupUrl} onChange={e => setForm({ ...form, backupUrl: e.target.value })}/><small>Uses MCUI's configured Drive account. The file must be accessible to that account.</small></label>}
    <label className="checkbox"><input type="checkbox" checked={form.acceptEula} onChange={e => setForm({ ...form, acceptEula: e.target.checked })}/><span>I agree to the Minecraft EULA for this server.</span></label>
    {message && <p className={create.isError ? 'error' : 'notice'} role="status">{message}</p>}
    <button className="primary" type="submit" disabled={create.isPending}>{create.isPending ? 'Creating…' : 'Create server'} <span>→</span></button>
  </form>
</section></div></main>}/><Route path="/servers/:name/packs/:kind/:folder" element={<PackDetailRoute servers={servers} loading={isLoading}/>}/><Route path="/servers/:name" element={<ServerDetailRoute servers={servers} loading={isLoading} error={error} backupConfigured={backupConfig?.configured ?? false}/>}/><Route path="*" element={<main className="route-state"><h1>Page not found</h1><Link to="/">← All servers</Link></main>}/></Routes><footer>MCUI <span>·</span> Self-hosted Minecraft management</footer></div>
}
createRoot(document.getElementById('root')!).render(<React.StrictMode><QueryClientProvider client={queryClient}><BrowserRouter><AuthGate/></BrowserRouter></QueryClientProvider></React.StrictMode>)
