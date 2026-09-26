import React, { useState } from 'react'
import { createRoot } from 'react-dom/client'
import { QueryClient, QueryClientProvider, useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import axios from 'axios'
import './style.css'

type Edition = 'bedrock' | 'java'
type Server = { name: string; edition: Edition; status: string }
type CreateServer = { name: string; edition: Edition; port: number; worldPath: string; acceptEula: boolean }
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
        <div className="login-mark">◆</div>
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
    return <div className="login-page"><div className="login-mark">◆</div></div>
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
    qc.clear()
    qc.setQueryData(['session'], { authenticated: false, enabled: true })
  }

  return <button className="sign-out" onClick={signOut}>Sign out</button>
}

function ServerCard({ server, backupConfigured }: { server: Server; backupConfigured: boolean }) {
  const qc = useQueryClient()
  const [message, setMessage] = useState('')
  const { data: backup } = useQuery({ queryKey: ['backup', server.name], queryFn: async () => (await api.get<BackupState>(`/servers/${server.name}/backup`)).data, refetchInterval: 5000 })
  const action = useMutation({ mutationFn: async (verb: 'start' | 'stop') => (await api.post(`/servers/${server.name}/${verb}`)).data, onSuccess: async () => { setMessage(''); await qc.invalidateQueries({ queryKey: ['servers'] }) }, onError: e => setMessage(errorMessage(e)) })
  const createBackup = useMutation({ mutationFn: async () => (await api.post<BackupState>(`/servers/${server.name}/backup`)).data, onSuccess: async () => { setMessage(''); await qc.invalidateQueries({ queryKey: ['backup', server.name] }) }, onError: e => setMessage(errorMessage(e)) })
  const busy = backup?.state === 'queued' || backup?.state === 'capturing' || backup?.state === 'uploading'
  const backupDescription = backup?.state === 'complete' ? `Last backup ${new Date(backup.completedAt!).toLocaleString()} · ${backup.snapshotId?.slice(0, 12)}` : backup?.state === 'failed' ? `Backup failed: ${backup.error}` : busy ? ({ queued: 'Backup queued', capturing: 'Capturing server data', uploading: 'Uploading to Google Drive' } as Record<string, string>)[backup!.state] : 'No backup created in this session'
  return <article className="server"><div className="server-main"><div className="server-symbol">{server.edition === 'bedrock' ? 'B' : 'J'}</div><div className="server-info"><h3>{server.name}</h3><p>{server.edition === 'bedrock' ? 'Bedrock · UDP' : 'Java · TCP'}</p></div><span className={`status ${server.status}`}>{server.status}</span><button disabled={action.isPending || backup?.state === 'capturing'} onClick={() => action.mutate(server.status === 'running' ? 'stop' : 'start')}>{server.status === 'running' ? 'Stop' : 'Start'}</button></div><div className="backup-row"><div><strong>Google Drive backup</strong><p className={backup?.state === 'failed' ? 'error' : 'backup-detail'}>{backupDescription}</p></div><button className="backup-button" disabled={!backupConfigured || busy || createBackup.isPending} onClick={() => createBackup.mutate()}>Back up now</button></div>{message && <p className="error" role="status">{message}</p>}</article>
}
function App({ authEnabled }: { authEnabled: boolean }) {
  const qc = useQueryClient()
  const { data: servers = [], isLoading, error } = useQuery({ queryKey: ['servers'], queryFn: async () => (await api.get<Server[]>('/servers')).data, refetchInterval: 5000 })
  const { data: backupConfig } = useQuery({ queryKey: ['backup-config'], queryFn: async () => (await api.get<BackupConfig>('/backups/config')).data, refetchInterval: 30000 })
  const [form, setForm] = useState<CreateServer>({ name: '', edition: 'bedrock', port: 19132, worldPath: '', acceptEula: false })
  const [message, setMessage] = useState('')
  const create = useMutation({ mutationFn: async (value: CreateServer) => (await api.post<Server>('/servers', value)).data, onSuccess: async (server) => { setMessage(`${server.name} created. Start it when ready.`); setForm({ name: '', edition: 'bedrock', port: 19132, worldPath: '', acceptEula: false }); await qc.invalidateQueries({ queryKey: ['servers'] }) }, onError: e => setMessage(errorMessage(e)) })
  return <div className="shell"><header><div className="brand"><span className="brand-icon">◆</span><span>MCUI</span></div>{authEnabled && <SignOutButton />}</header><main><section className="intro"><div><p className="kicker">YOUR SERVERS</p><h1>Make room for your next world.</h1><p className="muted">Create, start, back up, and stop Minecraft servers from one place. Each server keeps its own Compose file and world data.</p></div><div className="count"><strong>{servers.length}</strong><span>{servers.length === 1 ? 'server' : 'servers'}</span></div></section><div className="grid"><section className="panel servers"><div className="section-heading"><div><p className="kicker">OVERVIEW</p><h2>Servers</h2></div><span className="live"><i/>Refreshes every 5s</span></div><div className="backup-banner"><strong>Google Drive backups</strong><span>{backupConfig?.configured ? `Ready · ${backupConfig.interval === '0s' ? 'manual only' : `scheduled every ${backupConfig.interval}`}` : 'Setup required: add rclone.conf and restic-password to backup/'}</span></div>{isLoading ? <p className="muted">Loading servers…</p> : error ? <p className="error">{errorMessage(error)}</p> : servers.length === 0 ? <div className="empty"><div className="empty-icon">▦</div><h3>No servers yet</h3><p>Set up your first Bedrock or Java server using the form.</p></div> : <div className="server-list">{servers.map(server => <ServerCard key={server.name} server={server} backupConfigured={backupConfig?.configured ?? false}/>)}</div>}</section><section className="panel create"><p className="kicker">NEW SERVER</p><h2>Create a server</h2><p className="muted form-intro">Choose an edition and give your server a name. You can optionally import an existing world.</p><form onSubmit={e => { e.preventDefault(); setMessage(''); create.mutate(form) }}><label>Edition<div className="edition-options"><button type="button" className={form.edition === 'bedrock' ? 'selected' : ''} onClick={() => setForm({ ...form, edition: 'bedrock', port: 19132 })}>Bedrock <small>UDP</small></button><button type="button" className={form.edition === 'java' ? 'selected' : ''} onClick={() => setForm({ ...form, edition: 'java', port: 25565 })}>Java <small>TCP</small></button></div></label><label>Server name<input required minLength={1} maxLength={40} pattern="[a-z0-9][a-z0-9-]*" placeholder="my-survival-world" value={form.name} onChange={e => setForm({ ...form, name: e.target.value })}/><small>Lowercase letters, numbers, and hyphens.</small></label><label>Host port<input required type="number" min="1" max="65535" value={form.port} onChange={e => setForm({ ...form, port: Number(e.target.value) })}/></label><label>Existing world path <span className="optional">optional</span><input placeholder="/absolute/path/to/world.zip" value={form.worldPath} onChange={e => setForm({ ...form, worldPath: e.target.value })}/><small>Absolute path on this host to a world folder, ZIP, or tar.gz.</small></label><label className="checkbox"><input type="checkbox" checked={form.acceptEula} onChange={e => setForm({ ...form, acceptEula: e.target.checked })}/><span>I agree to the Minecraft EULA for this server.</span></label>{message && <p className={create.isError ? 'error' : 'notice'} role="status">{message}</p>}<button className="primary" type="submit" disabled={create.isPending}>{create.isPending ? 'Creating…' : 'Create server'} <span>→</span></button></form></section></div></main><footer>MCUI <span>·</span> Self-hosted Minecraft management</footer></div>
}
createRoot(document.getElementById('root')!).render(<React.StrictMode><QueryClientProvider client={queryClient}><AuthGate/></QueryClientProvider></React.StrictMode>)
