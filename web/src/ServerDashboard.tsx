import { useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import axios from 'axios'

type Server = { name: string; edition: 'bedrock' | 'java'; status: string; port?: number; host?: string; protocol?: string }
type Settings = { image: string; environment: Record<string, string>; revision: string }
type Compose = { yaml: string; revision: string }
const api = axios.create({ baseURL: '/api' })
const message = (error: unknown) => axios.isAxiosError(error) ? (error.response?.data?.error ?? error.message) : String(error)

const suggestions = [
  ['Java', 'TYPE', 'VERSION', 'MEMORY', 'MAX_MEMORY', 'INIT_MEMORY', 'DIFFICULTY', 'MODE', 'MOTD', 'MAX_PLAYERS', 'ONLINE_MODE', 'PVP', 'ALLOW_NETHER', 'VIEW_DISTANCE', 'SIMULATION_DISTANCE', 'SEED', 'OPS', 'WHITELIST'],
  ['Bedrock', 'VERSION', 'SERVER_NAME', 'LEVEL_NAME', 'LEVEL_SEED', 'GAMEMODE', 'DIFFICULTY', 'MAX_PLAYERS', 'ONLINE_MODE', 'ALLOW_CHEATS', 'VIEW_DISTANCE', 'TICK_DISTANCE'],
]

export default function ServerDashboard({ server, onBack }: { server: Server; onBack: () => void }) {
  const qc = useQueryClient()
  const key = encodeURIComponent(server.name)
  const [tab, setTab] = useState<'settings' | 'compose' | 'logs'>('settings')
  const [draft, setDraft] = useState<Settings | null>(null)
  const [yamlDraft, setYamlDraft] = useState<string | null>(null)
  const [newKey, setNewKey] = useState('')
  const [notice, setNotice] = useState('')
  const [error, setError] = useState('')
  const host = server.host || window.location.hostname
  const address = server.port ? `${host}:${server.port}` : ''
  const settings = useQuery({ queryKey: ['settings', key], queryFn: async () => (await api.get<Settings>(`/server-details/${key}/settings`)).data })
  const compose = useQuery({ queryKey: ['compose', key], queryFn: async () => (await api.get<Compose>(`/server-details/${key}/compose`)).data })
  const logs = useQuery({ queryKey: ['logs', key], queryFn: async () => (await api.get<{ logs: string }>(`/server-details/${key}/logs`)).data, enabled: tab === 'logs', refetchInterval: tab === 'logs' ? 5000 : false })
  const current = draft ?? settings.data
  const saveSettings = useMutation({ mutationFn: async () => (await api.put(`/server-details/${key}/settings`, current)).data, onSuccess: async () => { setDraft(null); setError(''); setNotice('Saved. Restart the server to apply changes.'); await Promise.all([qc.invalidateQueries({ queryKey: ['settings', key] }), qc.invalidateQueries({ queryKey: ['compose', key] }), qc.invalidateQueries({ queryKey: ['servers'] })]) }, onError: e => setError(message(e)) })
  const saveCompose = useMutation({ mutationFn: async () => (await api.put(`/server-details/${key}/compose`, { yaml: yamlDraft ?? compose.data?.yaml, revision: compose.data?.revision })).data, onSuccess: async () => { setYamlDraft(null); setError(''); setNotice('Saved. Restart the server to apply changes.'); await Promise.all([qc.invalidateQueries({ queryKey: ['settings', key] }), qc.invalidateQueries({ queryKey: ['compose', key] }), qc.invalidateQueries({ queryKey: ['servers'] })]) }, onError: e => setError(message(e)) })
  const setEnv = (key: string, value: string) => { if (current) setDraft({ ...current, environment: { ...current.environment, [key]: value } }) }
  const removeEnv = (key: string) => { if (current) { const environment = { ...current.environment }; delete environment[key]; setDraft({ ...current, environment }) } }
  const suggested = suggestions[server.edition === 'java' ? 0 : 1].slice(1).filter(key => !current?.environment[key])

  return <main className="dashboard">
    <button className="back-button" onClick={onBack}>← All servers</button>
    <div className="dashboard-heading"><div><p className="kicker">SERVER DASHBOARD</p><h1>{server.name}</h1><p className="muted">{server.edition === 'bedrock' ? 'Bedrock' : 'Java'} · {server.status}</p></div></div>
    <section className="address-panel"><div><strong>Connect address</strong><p>{address || 'No published game port found in Compose'}</p>{server.edition === 'bedrock' && address && <small>Bedrock · UDP</small>}</div><button disabled={!address} onClick={async () => { try { await navigator.clipboard.writeText(address); setNotice('Address copied.') } catch { setError('Could not copy address.') } }}>Copy address</button></section>
    <div className="detail-tabs"><button className={tab === 'settings' ? 'selected' : ''} onClick={() => setTab('settings')}>Settings</button><button className={tab === 'compose' ? 'selected' : ''} onClick={() => setTab('compose')}>Compose YAML</button><button className={tab === 'logs' ? 'selected' : ''} onClick={() => setTab('logs')}>Logs</button></div>
    {notice && <p className="notice" role="status">{notice}</p>}{error && <p className="error" role="alert">{error}</p>}
    {tab === 'settings' && <section className="panel detail-panel"><h2>itzg settings</h2><p className="muted">Edit image and environment variables. Any itzg variable can be added. Existing Compose ports, volumes, and other service options stay in the file. <a href={server.edition === 'java' ? 'https://docker-minecraft-server.readthedocs.io/en/latest/variables/' : 'https://github.com/itzg/docker-minecraft-bedrock-server#server-properties'} target="_blank" rel="noreferrer">View all itzg options ↗</a></p>{settings.isLoading ? <p>Loading…</p> : settings.isError ? <p className="error">{message(settings.error)}</p> : current && <><label>Image<input value={current.image} onChange={e => setDraft({ ...current, image: e.target.value })}/></label><div className="env-list">{Object.entries(current.environment).sort(([a], [b]) => a.localeCompare(b)).map(([key, value]) => <div className="env-row" key={key}><label>{key}<input value={value} onChange={e => setEnv(key, e.target.value)}/></label><button title={`Remove ${key}`} onClick={() => removeEnv(key)}>Remove</button></div>)}</div><div className="env-add"><select value={newKey} onChange={e => setNewKey(e.target.value)}><option value="">Choose a common setting…</option>{suggested.map(key => <option key={key} value={key}>{key}</option>)}</select><input value={newKey} onChange={e => setNewKey(e.target.value.toUpperCase())} placeholder="Or enter any itzg variable"/><button onClick={() => { if (/^[A-Z_][A-Z0-9_]*$/.test(newKey) && current.environment[newKey] === undefined) { setEnv(newKey, ''); setNewKey('') } else { setError('Enter a new environment variable name.') } }}>Add</button></div><button className="primary save-button" disabled={!draft || saveSettings.isPending} onClick={() => saveSettings.mutate()}>Save settings</button></>}</section>}
    {tab === 'compose' && <section className="panel detail-panel"><h2>Compose YAML</h2><p className="muted">Edit any Compose option. The file is validated before saving. Restart the server to apply changes.</p>{compose.isLoading ? <p>Loading…</p> : compose.isError ? <p className="error">{message(compose.error)}</p> : <><textarea spellCheck={false} value={yamlDraft ?? compose.data?.yaml ?? ''} onChange={e => setYamlDraft(e.target.value)}/><button className="primary save-button" disabled={yamlDraft === null || saveCompose.isPending} onClick={() => saveCompose.mutate()}>Save Compose</button></>}</section>}
    {tab === 'logs' && <section className="panel detail-panel"><h2>Recent logs</h2><p className="muted">Last 200 lines. Refreshes every five seconds.</p>{logs.isError ? <p className="error">{message(logs.error)}</p> : <pre className="server-logs">{logs.data?.logs ?? 'Loading…'}</pre>}</section>}
  </main>
}
