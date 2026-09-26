import { useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import axios from 'axios'

type Server = { name: string; edition: 'bedrock' | 'java'; status: string; port?: number; protocol?: string }
type Settings = { image: string; restart: string; stopGracePeriod: string; environment: Record<string, string>; revision: string }
type Compose = { yaml: string; revision: string }
type Resources = { available: boolean; cpu?: string; memory?: string; memoryPercent?: string; network?: string; diskIO?: string; processes?: string; reason?: string }
type Storage = { available: boolean; bytes?: number; reason?: string }
const api = axios.create({ baseURL: '/api' })
const errorMessage = (error: unknown) => axios.isAxiosError(error) ? (error.response?.data?.error ?? error.message) : String(error)

const common = {
  java: [
    ['MOTD', 'Message of the day', 'Shown in the multiplayer server list'],
    ['VERSION', 'Minecraft version', 'For example LATEST or a version number'],
    ['MEMORY', 'Java memory', 'For example 2G'],
    ['MAX_PLAYERS', 'Maximum players', 'Player slots'],
    ['DIFFICULTY', 'Difficulty', 'peaceful, easy, normal, or hard'],
    ['MODE', 'Game mode', 'survival, creative, adventure, or spectator'],
  ],
  bedrock: [
    ['SERVER_NAME', 'Server name', 'Shown to players'],
    ['VERSION', 'Bedrock version', 'For example LATEST or a version number'],
    ['MAX_PLAYERS', 'Maximum players', 'Player slots'],
    ['DIFFICULTY', 'Difficulty', 'peaceful, easy, normal, or hard'],
    ['GAMEMODE', 'Game mode', 'survival, creative, or adventure'],
    ['VIEW_DISTANCE', 'View distance', 'Distance in chunks'],
  ],
} as const

function metricWidth(value?: string) { const percent = Number.parseFloat(value || '0'); return `${Math.max(0, Math.min(percent, 100))}%` }
function formatBytes(bytes?: number) { if (bytes === undefined) return '—'; const units = ['B', 'KiB', 'MiB', 'GiB', 'TiB']; let size = bytes; let unit = 0; while (size >= 1024 && unit < units.length - 1) { size /= 1024; unit++ } return `${size.toFixed(unit === 0 ? 0 : 1)} ${units[unit]}` }
function choices(variable: string): string[] | null {
  if (variable === 'DIFFICULTY') return ['peaceful', 'easy', 'normal', 'hard']
  if (variable === 'MODE') return ['survival', 'creative', 'adventure', 'spectator']
  if (variable === 'GAMEMODE') return ['survival', 'creative', 'adventure']
  return null
}

export default function Infrastructure({ server, active }: { server: Server; active: boolean }) {
  const qc = useQueryClient()
  const key = encodeURIComponent(server.name)
  const [draft, setDraft] = useState<Settings | null>(null)
  const [yamlDraft, setYamlDraft] = useState<string | null>(null)
  const [newKey, setNewKey] = useState('')
  const [message, setMessage] = useState('')
  const [error, setError] = useState('')
  const resources = useQuery({ queryKey: ['resources', key], queryFn: async () => (await api.get<Resources>(`/server-details/${key}/resources`)).data, enabled: active, refetchInterval: active ? 15000 : false })
  const storage = useQuery({ queryKey: ['storage', key], queryFn: async () => (await api.get<Storage>(`/server-details/${key}/storage`)).data, enabled: active, refetchInterval: active ? 60000 : false })
  const settings = useQuery({ queryKey: ['settings', key], queryFn: async () => (await api.get<Settings>(`/server-details/${key}/settings`)).data, enabled: active })
  const compose = useQuery({ queryKey: ['compose', key], queryFn: async () => (await api.get<Compose>(`/server-details/${key}/compose`)).data, enabled: active })
  const current = draft ?? settings.data
  const setEnv = (variable: string, value: string) => { if (current) setDraft({ ...current, environment: { ...current.environment, [variable]: value } }) }
  const removeEnv = (variable: string) => { if (current) { const environment = { ...current.environment }; delete environment[variable]; setDraft({ ...current, environment }) } }
  const setCommonEnv = (variable: string, value: string) => value === '' ? removeEnv(variable) : setEnv(variable, value)
  const saveSettings = useMutation({ mutationFn: async () => (await api.put(`/server-details/${key}/settings`, current)).data, onSuccess: async () => { setDraft(null); setError(''); setMessage('Configuration saved. Stop and start the server to apply it.'); await Promise.all([qc.invalidateQueries({ queryKey: ['settings', key] }), qc.invalidateQueries({ queryKey: ['compose', key] }), qc.invalidateQueries({ queryKey: ['servers'] })]) }, onError: e => { setMessage(''); setError(errorMessage(e)) } })
  const saveCompose = useMutation({ mutationFn: async () => (await api.put(`/server-details/${key}/compose`, { yaml: yamlDraft ?? compose.data?.yaml, revision: compose.data?.revision })).data, onSuccess: async () => { setYamlDraft(null); setError(''); setMessage('Compose file saved. Stop and start the server to apply it.'); await Promise.all([qc.invalidateQueries({ queryKey: ['settings', key] }), qc.invalidateQueries({ queryKey: ['compose', key] }), qc.invalidateQueries({ queryKey: ['servers'] })]) }, onError: e => { setMessage(''); setError(errorMessage(e)) } })

  return <div className="infrastructure" hidden={!active}>
    <div className="infra-intro"><div><p className="kicker">INFRASTRUCTURE</p><h2>Run and configure your server</h2><p className="muted">Live container resources and the Compose settings behind this world.</p></div><span className="infra-refresh"><i/>Metrics refresh every 15s</span></div>
    {message && <p className="notice" role="status">{message}</p>}{error && <p className="error" role="alert">{error}</p>}
    <section className="infra-section" id="infra-resources"><div className="infra-section-head"><span className="infra-step">01</span><div><h3>Resources</h3><p>Current container usage and server data size</p></div></div><div className="resource-grid"><div className="resource-card"><span>CPU</span><strong>{resources.data?.available ? resources.data.cpu : '—'}</strong><div className="metric-track"><i style={{ width: metricWidth(resources.data?.cpu) }}/></div><small>Container CPU usage</small></div><div className="resource-card"><span>Memory</span><strong>{resources.data?.available ? resources.data.memoryPercent : '—'}</strong><div className="metric-track"><i style={{ width: metricWidth(resources.data?.memoryPercent) }}/></div><small>{resources.data?.available ? resources.data.memory : 'No live data'}</small></div><div className="resource-card"><span>World storage</span><strong>{storage.data?.available ? formatBytes(storage.data.bytes) : '—'}</strong><div className="metric-track muted-track"><i/></div><small>Size of /data · refreshes every 60s</small></div></div><div className="infra-secondary-metrics"><span>Network I/O <strong>{resources.data?.available ? resources.data.network : '—'}</strong></span><span>Block I/O <strong>{resources.data?.available ? resources.data.diskIO : '—'}</strong></span><span>Processes <strong>{resources.data?.available ? resources.data.processes : '—'}</strong></span></div>{resources.data && !resources.data.available && <p className="infra-hint">{resources.data.reason}</p>}{storage.data && !storage.data.available && <p className="infra-hint">Storage: {storage.data.reason}</p>}</section>
    <section className="infra-section" id="infra-configuration"><div className="infra-section-head"><span className="infra-step">02</span><div><h3>Container & game settings</h3><p>Fields saved to this server's Compose file</p></div></div>{settings.isLoading ? <p className="muted">Loading configuration…</p> : settings.isError ? <p className="error">{errorMessage(settings.error)}</p> : current && <form onSubmit={event => { event.preventDefault(); saveSettings.mutate() }}><div className="infra-container-summary"><span>Edition <strong>{server.edition === 'bedrock' ? 'Bedrock' : 'Java'}</strong></span><span>Published port <strong>{server.port ? `${server.port}/${server.protocol || (server.edition === 'bedrock' ? 'udp' : 'tcp')}` : 'Not published'}</strong></span><span>Status <strong>{server.status}</strong></span></div><label className="infra-field infra-image">Docker image<input value={current.image} onChange={event => setDraft({ ...current, image: event.target.value })}/><small>Image and tag used when the container is recreated.</small></label><div className="infra-compose-options"><label className="infra-field">Restart policy<select value={current.restart} onChange={event => setDraft({ ...current, restart: event.target.value })}><option value="">Compose default</option><option value="unless-stopped">Unless stopped</option><option value="always">Always</option><option value="on-failure">On failure</option><option value="no">Never</option></select><small>Controls automatic container restarts.</small></label><label className="infra-field">Stop grace period<input value={current.stopGracePeriod} onChange={event => setDraft({ ...current, stopGracePeriod: event.target.value })} placeholder="Compose default, e.g. 2m"/><small>Time allowed for a clean shutdown.</small></label></div><div className="infra-fields">{common[server.edition].map(([variable, label, help]) => <label className="infra-field" key={variable}>{label}{choices(variable) ? <select value={current.environment[variable] ?? ''} onChange={event => setCommonEnv(variable, event.target.value)}><option value="">Use image default</option>{current.environment[variable] && !choices(variable)?.includes(current.environment[variable]) && <option value={current.environment[variable]}>{current.environment[variable]}</option>}{choices(variable)?.map(option => <option key={option} value={option}>{option}</option>)}</select> : <input value={current.environment[variable] ?? ''} onChange={event => setCommonEnv(variable, event.target.value)} placeholder="Use image default"/>}<small>{help}</small></label>)}</div><details className="infra-disclosure"><summary><span>All environment variables</span><small>{Object.keys(current.environment).length} configured · add any itzg option</small></summary><div className="env-list">{Object.entries(current.environment).sort(([a], [b]) => a.localeCompare(b)).map(([variable, value]) => <div className="env-row" key={variable}><label>{variable}<input value={value} onChange={event => setEnv(variable, event.target.value)}/></label><button type="button" onClick={() => removeEnv(variable)} aria-label={`Remove ${variable}`}>Remove</button></div>)}</div><div className="env-add"><input aria-label="New variable name" value={newKey} onChange={event => setNewKey(event.target.value.toUpperCase())} placeholder="NEW_VARIABLE_NAME"/><button type="button" onClick={() => { if (/^[A-Z_][A-Z0-9_]*$/.test(newKey) && current.environment[newKey] === undefined) { setEnv(newKey, ''); setNewKey(''); setError('') } else { setError('Enter a new environment variable name.') } }}>Add variable</button></div><a href={server.edition === 'java' ? 'https://docker-minecraft-server.readthedocs.io/en/latest/variables/' : 'https://github.com/itzg/docker-minecraft-bedrock-server#server-properties'} target="_blank" rel="noreferrer">Browse all itzg options ↗</a></details><div className="infra-form-footer"><span>Changes apply after stopping and starting the server.</span><button className="primary" type="submit" disabled={!draft || saveSettings.isPending}>{saveSettings.isPending ? 'Saving…' : 'Save configuration'}</button></div></form>}</section>
    <section className="infra-section" id="infra-compose"><div className="infra-section-head"><span className="infra-step">03</span><div><h3>Compose source</h3><p>Edit ports, volumes, restart policy, and other advanced options</p></div></div><details className="infra-disclosure"><summary><span>Open raw Compose YAML</span><small>Validated before saving</small></summary>{compose.isLoading ? <p className="muted">Loading Compose file…</p> : compose.isError ? <p className="error">{errorMessage(compose.error)}</p> : <><textarea spellCheck={false} aria-label="Compose YAML" value={yamlDraft ?? compose.data?.yaml ?? ''} onChange={event => setYamlDraft(event.target.value)}/><div className="infra-form-footer"><span>Changes apply after stopping and starting the server.</span><button className="primary" type="button" disabled={yamlDraft === null || saveCompose.isPending} onClick={() => saveCompose.mutate()}>{saveCompose.isPending ? 'Saving…' : 'Save Compose'}</button></div></>}</details></section>
  </div>
}
