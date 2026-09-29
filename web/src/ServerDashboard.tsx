import { useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import axios from 'axios'
import Infrastructure from './Infrastructure'
import Console from './Console'
import Packs from './Packs'
import Files from './Files'
import ServerProfileEditor from './ServerProfileEditor'
import NotificationBell from './NotificationBell'

type Server = {
  name: string
  displayName?: string
  iconUrl?: string
  edition: 'bedrock' | 'java'
  status: string
  port?: number
  host?: string
  protocol?: string
}
type BackupState = {
  state: 'idle' | 'queued' | 'capturing' | 'uploading' | 'complete' | 'failed'
  completedAt?: string
  snapshotId?: string
  error?: string
}
type PlayerStatus = {
  available: boolean
  online?: number
  max?: number
  version?: string
  players?: string[]
  reason?: string
}
type Tab = 'overview' | 'infrastructure' | 'console' | 'resources' | 'files'

const api = axios.create({ baseURL: '/api' })
const errorMessage = (error: unknown) => axios.isAxiosError(error)
  ? (error.response?.data?.error ?? error.message)
  : String(error)

function CopyIcon() {
  return <svg aria-hidden="true" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" strokeLinejoin="round">
    <rect x="8" y="8" width="12" height="12" rx="2" />
    <path d="M16 8V6a2 2 0 0 0-2-2H6a2 2 0 0 0-2 2v8a2 2 0 0 0 2 2h2" />
  </svg>
}

export default function ServerDashboard({ server, onBack, backupConfigured }: {
  server: Server
  onBack: () => void
  backupConfigured: boolean
}) {
  const qc = useQueryClient()
  const key = encodeURIComponent(server.name)
  const [tab, setTab] = useState<Tab>(() => { const requested = new URLSearchParams(window.location.search).get('tab'); return requested === 'resources' && server.edition === 'bedrock' ? 'resources' : requested === 'files' ? 'files' : 'overview' })
  const [editingProfile, setEditingProfile] = useState(false)
  const [notice, setNotice] = useState('')
  const [error, setError] = useState('')
  const [copied, setCopied] = useState(false)
  const address = server.port ? `${server.host || window.location.hostname}:${server.port}` : ''

  const logs = useQuery({
    queryKey: ['logs', key],
    queryFn: async () => (await api.get<{ logs: string }>(`/server-details/${key}/logs`)).data,
    enabled: tab === 'overview',
    refetchInterval: tab === 'overview' ? 5000 : false,
  })
  const backup = useQuery({
    queryKey: ['backup', server.name],
    queryFn: async () => (await api.get<BackupState>(`/servers/${key}/backup`)).data,
    refetchInterval: 5000,
  })
  const players = useQuery({
    queryKey: ['players', key],
    queryFn: async () => (await api.get<PlayerStatus>(`/server-details/${key}/players`)).data,
    enabled: tab === 'overview',
    refetchInterval: tab === 'overview' ? 15000 : false,
  })
  const backupBusy = backup.data?.state === 'queued' || backup.data?.state === 'capturing' || backup.data?.state === 'uploading'
  const action = useMutation({
    mutationFn: async (verb: 'start' | 'stop') => (await api.post(`/servers/${key}/${verb}`)).data,
    onSuccess: async () => {
      setError('')
      setNotice('Server action completed.')
      await qc.invalidateQueries({ queryKey: ['servers'] })
    },
    onError: err => setError(errorMessage(err)),
  })
  const createBackup = useMutation({
    mutationFn: async () => (await api.post(`/servers/${key}/backup`)).data,
    onSuccess: async () => {
      setError('')
      setNotice('Backup queued.')
      await qc.invalidateQueries({ queryKey: ['backup', server.name] })
    },
    onError: err => setError(errorMessage(err)),
  })

  async function copyAddress() {
    try {
      await navigator.clipboard.writeText(address)
      setCopied(true)
      window.setTimeout(() => setCopied(false), 2000)
    } catch {
      setError('Could not copy the address.')
    }
  }

  const backupLabel = backupBusy ? 'In progress'
    : backup.data?.state === 'complete' ? 'Complete'
      : backup.data?.state === 'failed' ? 'Failed' : 'No backup yet'
  const backupDetail = backup.data?.state === 'complete' && backup.data.completedAt
    ? new Date(backup.data.completedAt).toLocaleString()
    : backup.data?.error || (backupConfigured ? 'Create a backup whenever you need one.' : 'Configure Google Drive first.')
  const recentLogs = logs.data?.logs?.trim().split('\n').slice(-12).join('\n') || 'No log output yet.'

  return <main className="dashboard">
    <button className="back-button" onClick={onBack}>← All servers</button>

    <section className="dashboard-hero">
      <div className="dashboard-heading">
        <div className="dashboard-emblem">{server.iconUrl ? <img src={server.iconUrl} alt="" /> : server.edition === 'bedrock' ? 'B' : 'J'}</div>
        <div>
          <p className="kicker">{server.edition === 'bedrock' ? 'BEDROCK SERVER' : 'JAVA SERVER'}</p>
          <h1>{server.displayName || server.name}</h1>
        </div>
        <span className={`status-pill ${server.status}`}><i />{server.status}</span>
      </div>
      <p className="muted">Manage your world, connect players, and keep it backed up.</p>
      <div className="dashboard-actions">
        <button type="button" className="secondary-action" aria-expanded={editingProfile} onClick={() => setEditingProfile(value => !value)}>Edit server</button>
        <button className="primary" disabled={action.isPending || backup.data?.state === 'capturing'} onClick={() => action.mutate(server.status === 'running' ? 'stop' : 'start')}>
          {action.isPending ? 'Working…' : server.status === 'running' ? 'Stop server' : 'Start server'}
        </button>
        <button className="secondary-action" disabled={!backupConfigured || backupBusy || createBackup.isPending} onClick={() => createBackup.mutate()}>
          {backupBusy ? 'Backup in progress…' : 'Back up now'}
        </button>
      </div>
      <NotificationBell server={server.name} label={server.displayName || server.name} inline />
    </section>
    {editingProfile && <ServerProfileEditor server={server} onClose={() => setEditingProfile(false)} />}

    <nav className="detail-tabs" aria-label="Server sections">
      {(['overview', 'resources', 'files', 'infrastructure', 'console'] as const).filter(section => section !== 'resources' || server.edition === 'bedrock').map(section => <button
        key={section}
        type="button"
        className={tab === section ? 'selected' : ''}
        aria-current={tab === section ? 'page' : undefined}
        onClick={() => setTab(section)}
      >{section[0].toUpperCase() + section.slice(1)}</button>)}
    </nav>

    {notice && <p className="notice" role="status">{notice}</p>}
    {error && <p className="error" role="alert">{error}</p>}

    {tab === 'overview' && <div className="overview-grid">
      <section className="panel overview-card">
        <p className="kicker">CONNECT</p>
        <h2>Join this world</h2>
        <p className="muted">Share this address with players.</p>
        <div className="copy-field">
          <span role="textbox" aria-label="Server address">{address || 'No published game port'}</span>
          <button type="button" disabled={!address} onClick={copyAddress} aria-label={copied ? 'Address copied' : 'Copy server address'} title={copied ? 'Copied' : 'Copy address'}><CopyIcon /></button>
        </div>
        <small className="copy-hint" role="status">{copied ? 'Copied to clipboard' : `${server.edition === 'bedrock' ? 'Bedrock · UDP' : 'Java · TCP'}${server.port ? ` · Port ${server.port}` : ''}`}</small>
      </section>

      <section className="panel overview-card">
        <p className="kicker">SAFEKEEPING</p>
        <h2>Google Drive backup</h2>
        <p className="muted">Compressed archives stored in Drive.</p>
        <div className="backup-summary">
          <span className={`status-pill ${backup.data?.state === 'complete' ? 'running' : ''}`}><i />{backupLabel}</span>
          <small>{backupDetail}</small>
        </div>
        {backup.data?.snapshotId && <code className="archive-name">{backup.data.snapshotId}</code>}
      </section>

      <section className="panel overview-card players-card">
        <p className="kicker">COMMUNITY</p>
        <h2>Players online</h2>
        {players.isLoading ? <p className="muted">Checking server…</p>
          : players.isError ? <p className="muted">Player status is unavailable.</p>
            : players.data?.available ? <>
              <div className="player-count"><strong>{players.data.online}</strong><span>/ {players.data.max} slots</span></div>
              <p className="player-version">{players.data.version ? `Minecraft ${players.data.version}` : 'Server responding'} · updates every 15s</p>
              {players.data.players?.length ? <div className="player-list">{players.data.players.map(name => <span key={name}>{name}</span>)}</div>
                : <p className="player-note">{players.data.online ? 'Names are not provided by this server status response.' : 'No one is playing right now.'}</p>}
            </> : <p className="muted">{players.data?.reason || (server.status === 'running' ? 'Waiting for a player status response.' : 'Start the server to see players.')}</p>}
      </section>

      <section className="panel overview-card logs-card">
        <div className="overview-card-heading">
          <div><p className="kicker">ACTIVITY</p><h2>Recent output</h2></div>
          <button onClick={() => setTab('console')}>Open console →</button>
        </div>
        {logs.isError ? <p className="error">{errorMessage(logs.error)}</p> : <pre className="server-logs">{recentLogs}</pre>}
      </section>
    </div>}

    <Infrastructure server={server} active={tab === 'infrastructure'} />

    {tab === 'console' && <Console server={server} />}
    {tab === 'resources' && <Packs name={server.name} />}
    {tab === 'files' && <Files server={server.name} stopped={server.status === 'stopped'} />}
  </main>
}
