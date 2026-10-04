import { useEffect, useState, type DragEvent, type FormEvent } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { DndContext, KeyboardSensor, PointerSensor, TouchSensor, closestCenter, useSensor, useSensors, type DragEndEvent } from '@dnd-kit/core'
import { SortableContext, sortableKeyboardCoordinates, useSortable, verticalListSortingStrategy } from '@dnd-kit/sortable'
import { CSS } from '@dnd-kit/utilities'
import axios from 'axios'
import { Link } from 'react-router'
import CustomDimension from './CustomDimension'
import MinecraftText, { plainMinecraftText } from './MinecraftText'

export type Pack = { id: string; name: string; uuid: string; version: number[] | string; kind: 'resource' | 'behavior'; active: boolean; order: number; loadState: string; builtIn: boolean; hasIcon: boolean }
type Listing = { packs: Pack[]; world: string; running: boolean }
type Order = { resource: string[]; behavior: string[] }
type UpdateSummary = { kind: string; name: string; folder: string; from: string; to: string; disabled: number; missing: number }
type UpdatePreview = { revision: string; updates: UpdateSummary[]; warnings: string[] }
const api = axios.create({ baseURL: '/api' })
const message = (error: unknown) => axios.isAxiosError(error) ? error.response?.data?.error || error.message : String(error)

export function packDetailPath(server: string, pack: Pack) { return `/servers/${encodeURIComponent(server)}/packs/${pack.kind}/${encodeURIComponent(pack.id.split('/')[1])}` }
export function packIconPath(server: string, pack: Pack) { return `/api/server-packs/${encodeURIComponent(server)}/${pack.kind}/${encodeURIComponent(pack.id.split('/')[1])}/icon` }

function PackCard({ server, pack, active, running, onToggle, dragHandle }: { server: string; pack: Pack; active: boolean; running: boolean; onToggle?: () => void; dragHandle?: ReturnType<typeof useSortable> }) {
  const name = plainMinecraftText(pack.name)
  return <article ref={dragHandle?.setNodeRef} style={dragHandle ? { transform: CSS.Transform.toString(dragHandle.transform), transition: dragHandle.transition } : undefined} className={`pack-card ${active ? 'pack-active' : ''} ${dragHandle?.isDragging ? 'pack-dragging' : ''}`}>
    {dragHandle && <button className="pack-handle" type="button" {...dragHandle.attributes} {...dragHandle.listeners} aria-label={`Drag ${name} to reorder`} title="Drag to reorder">⠿</button>}
    <span className="pack-icon" aria-hidden="true">{pack.hasIcon ? <img src={packIconPath(server, pack)} alt="" /> : pack.kind === 'resource' ? 'R' : 'B'}</span>
    <div className="pack-copy"><strong><MinecraftText value={pack.name} /></strong><small>{pack.uuid} · v{Array.isArray(pack.version) ? pack.version.join('.') : pack.version}</small>{active && <span className={`pack-load ${pack.loadState === 'seen in Pack Stack' || pack.loadState === 'Selected in world JSON' ? 'loaded' : ''}`}>{pack.loadState}{pack.kind === 'behavior' && !running ? ' · server stopped' : ''}</span>}</div>
    {pack.builtIn ? <span className="pack-system-state">{active ? 'Referenced by world' : 'Bedrock-provided'}</span> : <div className="pack-card-actions"><button type="button" className={`pack-switch ${active ? 'selected' : ''}`} role="switch" aria-checked={active} aria-label={`${active ? 'Deactivate' : 'Activate'} ${name}`} onClick={onToggle}><i aria-hidden="true" /><span>{active ? 'Active' : 'Off'}</span></button><Link className="pack-open" to={packDetailPath(server, pack)} aria-label={`Open ${name} details`}>Details →</Link></div>}
  </article>
}

function SortablePack({ server, pack, running, onToggle }: { server: string; pack: Pack; running: boolean; onToggle: () => void }) {
  const dragHandle = useSortable({ id: pack.id })
  return <PackCard server={server} pack={pack} active running={running} onToggle={onToggle} dragHandle={dragHandle} />
}

function VersionedPackList({ server, packs, running, activeIDs, onToggle }: { server: string; packs: Pack[]; running: boolean; activeIDs: string[]; onToggle?: (pack: Pack) => void }) {
  const folder = (pack: Pack) => pack.id.split('/')[1]
  const bases = new Map(packs.map(pack => [folder(pack).toLowerCase(), pack]))
  const versions = new Map<string, Pack[]>()
  const children = new Set<string>()
  for (const pack of packs) {
    const match = /^(.+?)_(\d+(?:\.\d+)+)$/i.exec(folder(pack))
    const base = match && bases.get(match[1].toLowerCase())
    if (!base) continue
    const siblings = versions.get(base.id) || []
    siblings.push(pack)
    versions.set(base.id, siblings)
    children.add(pack.id)
  }
  return <div className="pack-list">{packs.filter(pack => !children.has(pack.id)).map(pack => {
    const siblings = versions.get(pack.id)?.sort((a, b) => folder(a).localeCompare(folder(b), undefined, { numeric: true })) || []
    return <div className="pack-family" key={pack.id}><PackCard server={server} pack={pack} active={activeIDs.includes(pack.id)} running={running} onToggle={onToggle ? () => onToggle(pack) : undefined} />{siblings.length > 0 && <details className="pack-version-list"><summary>{siblings.length} version{siblings.length === 1 ? '' : 's'} of {folder(pack)}</summary><div className="pack-list">{siblings.map(version => <PackCard key={version.id} server={server} pack={version} active={activeIDs.includes(version.id)} running={running} onToggle={onToggle ? () => onToggle(version) : undefined} />)}</div></details>}</div>
  })}</div>
}

export default function Packs({ name }: { name: string }) {
  const key = encodeURIComponent(name)
  const qc = useQueryClient()
  const [dimensionBusy, setDimensionBusy] = useState(false)
  const [order, setOrder] = useState<Order | null>(null)
  const [touched, setTouched] = useState(false)
  const [file, setFile] = useState<File | null>(null)
  const [url, setURL] = useState('')
  const [source, setSource] = useState<'file' | 'url'>('file')
  const [mode, setMode] = useState<'install' | 'update'>('install')
  const [pendingUpdate, setPendingUpdate] = useState<UpdatePreview | null>(null)
  const [draggingFile, setDraggingFile] = useState(false)
  const [notice, setNotice] = useState('')
  const [updateWarnings, setUpdateWarnings] = useState<string[]>([])
  const [error, setError] = useState('')
  const listing = useQuery({ queryKey: ['packs', key], queryFn: async () => (await api.get<Listing>(`/server-details/${key}/packs`)).data, refetchInterval: 10000 })
  useEffect(() => {
    if (!listing.data || touched) return
    const selected = (kind: Pack['kind']) => listing.data!.packs.filter(p => p.kind === kind && p.active).sort((a, b) => a.order - b.order).map(p => p.id)
    setOrder({ resource: selected('resource'), behavior: selected('behavior') })
  }, [listing.data, touched])
  const save = useMutation({ mutationFn: async (value: Order) => (await api.put(`/server-details/${key}/packs`, value)).data, onSuccess: async () => { setNotice('Saved to the world pack JSON files. Restart the server to apply changes.'); setError(''); await qc.invalidateQueries({ queryKey: ['packs', key] }); setTouched(false) }, onError: err => setError(message(err)) })
  const sendArchive = async (preview = false, expectedHash = '') => {
    const params = mode === 'update' ? { mode: 'update', ...(preview ? { preview: '1' } : { expectedHash }) } : {}
    if (source === 'file' && file) { const body = new FormData(); body.append('file', file); return (await api.post(`/server-details/${key}/packs`, body, { params })).data }
    return (await api.post(`/server-details/${key}/packs`, { url }, { params })).data
  }
  const inspectUpdate = useMutation({ mutationFn: async () => await sendArchive(true) as UpdatePreview, onSuccess: preview => { setPendingUpdate(preview); setError('') }, onError: err => setError(message(err)) })
  const upload = useMutation({ mutationFn: async () => await sendArchive(false, pendingUpdate?.revision), onSuccess: async (data: { installed?: number; updated?: number; updates?: UpdateSummary[]; warnings?: string[] }) => { const disabled = data.updates?.reduce((count, item) => count + item.disabled, 0) || 0; const missing = data.updates?.reduce((count, item) => count + item.missing, 0) || 0; setNotice(mode === 'update' ? `Updated ${data.updated} pack${data.updated === 1 ? '' : 's'}. Reapplied ${disabled - missing} disabled asset path${disabled - missing === 1 ? '' : 's'}; ${missing} ${missing === 1 ? 'was' : 'were'} absent from the new pack. Restart the server.` : `Installed ${data.installed} pack${data.installed === 1 ? '' : 's'}. Activate below, save, then restart the server.`); setUpdateWarnings(data.warnings || []); setError(''); setFile(null); setURL(''); setPendingUpdate(null); await Promise.all([qc.invalidateQueries({ queryKey: ['packs', key] }), qc.invalidateQueries({ queryKey: ['pack-assets', name] })]); setTouched(false) }, onError: err => { setPendingUpdate(null); setError(message(err)) } })
  const current = order || { resource: [], behavior: [] }
  const saved = (kind: Pack['kind']) => listing.data?.packs.filter(p => p.kind === kind && p.active).sort((a, b) => a.order - b.order).map(p => p.id) || []
  const dirty = (['resource', 'behavior'] as const).some(kind => JSON.stringify(current[kind]) !== JSON.stringify(saved(kind)))
  const sensors = useSensors(useSensor(PointerSensor, { activationConstraint: { distance: 6 } }), useSensor(TouchSensor, { activationConstraint: { delay: 180, tolerance: 8 } }), useSensor(KeyboardSensor, { coordinateGetter: sortableKeyboardCoordinates }))
  function change(next: Order) { if (dimensionBusy) return; setTouched(true); setOrder(next); setNotice('') }
  function reorder(kind: Pack['kind'], event: DragEndEvent) {
    const { active, over } = event
    if (!over || active.id === over.id) return
    const builtInIDs = new Set(listing.data?.packs.filter(pack => pack.kind === kind && pack.builtIn).map(pack => pack.id) || [])
    const ids = current[kind].filter(id => !builtInIDs.has(id)), source = ids.indexOf(String(active.id)), destination = ids.indexOf(String(over.id))
    if (source < 0 || destination < 0) return
    ids.splice(source, 1); ids.splice(destination, 0, String(active.id))
    let position = 0
    change({ ...current, [kind]: current[kind].map(id => builtInIDs.has(id) ? id : ids[position++]) })
  }
  function toggle(pack: Pack) {
    const ids = current[pack.kind]
    change({ ...current, [pack.kind]: ids.includes(pack.id) ? ids.filter(id => id !== pack.id) : [...ids, pack.id] })
  }
  function dropFile(event: DragEvent<HTMLElement>) { event.preventDefault(); setDraggingFile(false); if (event.dataTransfer.files[0]) setFile(event.dataTransfer.files[0]) }
  function submit(event: FormEvent<HTMLFormElement>) { event.preventDefault(); if (mode === 'update') inspectUpdate.mutate(); else upload.mutate() }
  return <section className="packs-page">
    <div className="packs-heading"><p className="kicker">BEDROCK ADD-ONS</p><h2>Resource & behavior packs</h2><p className="muted">Drag active packs to set their order. Handles work with a mouse, touch, or keyboard.</p></div>
    {notice && <p className="notice" role="status">{notice}</p>}{updateWarnings.length > 0 && <div className="notice" role="status"><strong>Update needs review:</strong><ul>{updateWarnings.map((warning, index) => <li key={index}>{warning}</li>)}</ul></div>}{error && <p className="error" role="alert">{error}</p>}
    {listing.isLoading && <p className="muted">Loading packs…</p>}{listing.isError && <p className="error" role="alert">{message(listing.error)}</p>}
    {listing.data && <><p className="muted pack-world">World: {listing.data.world} · Resource selection comes from world JSON; behavior pack load status comes from recent Pack Stack logs.</p>
      {(['resource', 'behavior'] as const).map(kind => {
        const packs = listing.data!.packs.filter(pack => pack.kind === kind)
        const byID = new Map(packs.map(pack => [pack.id, pack]))
        const active = current[kind].map(id => byID.get(id)).filter((pack): pack is Pack => pack !== undefined && !pack.builtIn)
        const inactive = packs.filter(pack => !current[kind].includes(pack.id) && !pack.builtIn).sort((a, b) => plainMinecraftText(a.name).localeCompare(plainMinecraftText(b.name)))
        const builtIn = packs.filter(pack => pack.builtIn).sort((a, b) => plainMinecraftText(a.name).localeCompare(plainMinecraftText(b.name)))
        return <section className="panel pack-section" key={kind}><div className="pack-section-heading"><h3>{kind === 'resource' ? 'Resource packs' : 'Behavior packs'}</h3><span>{active.length} active</span></div>
          {active.length ? <DndContext sensors={sensors} collisionDetection={closestCenter} onDragEnd={event => reorder(kind, event)}><SortableContext items={active.map(pack => pack.id)} strategy={verticalListSortingStrategy}><div className="pack-list">{active.map(pack => <SortablePack key={pack.id} server={name} pack={pack} running={listing.data!.running} onToggle={() => toggle(pack)} />)}</div></SortableContext></DndContext> : <p className="muted">No active add-ons.</p>}
          {inactive.length > 0 && <details className="pack-disclosure"><summary>Inactive packs <span>{inactive.length}</span></summary><VersionedPackList server={name} packs={inactive} running={listing.data!.running} activeIDs={current[kind]} onToggle={toggle} /></details>}
          {builtIn.length > 0 && <details className="pack-disclosure"><summary>Bedrock-provided packs <span>{builtIn.length}</span></summary><p className="muted">{kind === 'resource' ? 'Vanilla, chemistry, and editor packs' : 'Vanilla, chemistry, editor, experimental, and server library packs'} supplied with Bedrock.</p><VersionedPackList server={name} packs={builtIn} running={listing.data!.running} activeIDs={current[kind]} /></details>}
        </section>
      })}
      <div className="pack-save-row"><button className="primary" type="button" disabled={dimensionBusy || save.isPending || !dirty} onClick={() => save.mutate(current)}>{save.isPending ? 'Saving…' : 'Save changes'}</button><p role="status">{dirty ? 'Unsaved changes. ' : ''}Saving writes activation and order to the world’s pack JSON files. It does not restart the server; restart it to apply changes.</p></div>
    </>}
    <CustomDimension name={name} disabled={!!listing.data?.running || !listing.data || dirty || save.isPending || upload.isPending || inspectUpdate.isPending} onBusy={setDimensionBusy} />
    <fieldset className="dimension-pack-controls" disabled={dimensionBusy}><section className="panel pack-section pack-install"><h3>Pack archives</h3><p className="muted">Install new packs or update installed packs from an MCADDON, MCPACK, ZIP, tar.gz, or tgz archive. Updates match each pack by manifest UUID.</p><div className="pack-source-tabs" role="group" aria-label="Pack action"><button type="button" className={mode === 'install' ? 'selected' : ''} onClick={() => { setMode('install'); setPendingUpdate(null) }}>Install new</button><button type="button" className={mode === 'update' ? 'selected' : ''} onClick={() => { setMode('update'); setPendingUpdate(null) }}>Update installed</button></div><div className="pack-source-tabs" role="group" aria-label="Pack source"><button type="button" className={source === 'file' ? 'selected' : ''} onClick={() => { setSource('file'); setPendingUpdate(null) }}>Upload file</button><button type="button" className={source === 'url' ? 'selected' : ''} onClick={() => { setSource('url'); setPendingUpdate(null) }}>Download URL</button></div><form onSubmit={submit}>
      {source === 'file' ? <label className={`pack-dropzone ${draggingFile ? 'dragging' : ''}`} onDragEnter={event => { event.preventDefault(); setDraggingFile(true) }} onDragOver={event => event.preventDefault()} onDragLeave={event => { if (!event.currentTarget.contains(event.relatedTarget as Node)) setDraggingFile(false) }} onDrop={dropFile}><input type="file" accept=".zip,.mcaddon,.mcpack,.tar.gz,.tgz" onChange={event => setFile(event.target.files?.[0] || null)} /><span className="pack-upload-icon" aria-hidden="true">↑</span><strong>{file ? file.name : 'Drop an archive here'}</strong><small>{file ? 'Click to choose a different file' : 'or click to choose a file · ZIP, MCADDON, MCPACK, tar.gz, tgz'}</small></label>
        : <label className="pack-url-label">Direct download link<input type="url" required placeholder="https://example.com/addon.mcaddon" value={url} onChange={event => setURL(event.target.value)} /><small>Public HTTPS links only. The archive is removed after processing.</small></label>}
      {mode === 'update' && <small>Stop the server first. Each incoming pack must have the same UUID and a higher version than one installed pack. MCUI reapplies disabled files recorded in its `.mcui` asset index by exact path; renamed files need a new selection. Review the matched paths and warnings before updating.</small>}
      <button className="primary" disabled={upload.isPending || inspectUpdate.isPending || (mode === 'update' && listing.data?.running) || (source === 'file' ? !file : !url)}>{inspectUpdate.isPending ? 'Reviewing…' : mode === 'update' ? 'Review update' : upload.isPending ? 'Installing…' : 'Install packs'}</button>
    </form></section></fieldset>
    {pendingUpdate && <div className="files-dialog-backdrop" role="presentation"><section className="files-dialog panel" role="dialog" aria-modal="true" aria-label="Review pack update"><h3>Update {pendingUpdate.updates.length} pack{pendingUpdate.updates.length === 1 ? '' : 's'}?</h3><p>The installed folders and world activation order stay in place. MCUI reapplies disabled asset paths from `.mcui` when those same paths exist in the new pack.</p><ul>{pendingUpdate.updates.map(item => <li key={`${item.kind}-${item.folder}`}><strong><MinecraftText value={item.name} /></strong> ({item.kind}, folder <code>{item.folder}</code>) · {item.from} → {item.to} · {item.disabled === 0 ? 'No .mcui disabled assets found' : `${item.disabled - item.missing} of ${item.disabled} disabled asset paths found in new pack`}{item.missing > 0 ? ` · ${item.missing} absent in new version` : ''}</li>)}</ul>{pendingUpdate.warnings.map((warning, index) => <small key={index}>Review: {warning}</small>)}<div className="files-dialog-actions"><button type="button" className="secondary-action" onClick={() => setPendingUpdate(null)}>Cancel</button><button type="button" className="primary" disabled={upload.isPending} onClick={() => upload.mutate()}>{upload.isPending ? 'Updating…' : 'Update packs'}</button></div></section></div>}
  </section>
}
