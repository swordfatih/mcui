import { useEffect, useState, type DragEvent, type FormEvent } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { DndContext, KeyboardSensor, PointerSensor, TouchSensor, closestCenter, useSensor, useSensors, type DragEndEvent } from '@dnd-kit/core'
import { SortableContext, sortableKeyboardCoordinates, useSortable, verticalListSortingStrategy } from '@dnd-kit/sortable'
import { CSS } from '@dnd-kit/utilities'
import axios from 'axios'

type Pack = { id: string; name: string; uuid: string; version: number[] | string; kind: 'resource' | 'behavior'; active: boolean; order: number; loadState: string; builtIn: boolean }
type Listing = { packs: Pack[]; world: string; running: boolean }
type Order = { resource: string[]; behavior: string[] }
const api = axios.create({ baseURL: '/api' })
const message = (error: unknown) => axios.isAxiosError(error) ? error.response?.data?.error || error.message : String(error)

function PackCard({ pack, active, running, onToggle, dragHandle }: { pack: Pack; active: boolean; running: boolean; onToggle: () => void; dragHandle?: ReturnType<typeof useSortable> }) {
  return <article ref={dragHandle?.setNodeRef} style={dragHandle ? { transform: CSS.Transform.toString(dragHandle.transform), transition: dragHandle.transition } : undefined} className={`pack-card ${active ? 'pack-active' : ''} ${dragHandle?.isDragging ? 'pack-dragging' : ''}`}>
    {dragHandle && <button className="pack-handle" type="button" {...dragHandle.attributes} {...dragHandle.listeners} aria-label={`Drag ${pack.name} to reorder`} title="Drag to reorder">⠿</button>}
    <div className="pack-copy"><strong>{pack.name}</strong><small>{pack.uuid} · v{Array.isArray(pack.version) ? pack.version.join('.') : pack.version}</small>{active && <span className={`pack-load ${pack.loadState === 'seen in Pack Stack' ? 'loaded' : ''}`}>{pack.loadState}{!running ? ' · server stopped' : ''}</span>}</div>
    <label className="pack-toggle"><input type="checkbox" checked={active} onChange={onToggle} /><span>{active ? 'Active' : 'Activate'}</span></label>
  </article>
}

function SortablePack({ pack, running, onToggle }: { pack: Pack; running: boolean; onToggle: () => void }) {
  const dragHandle = useSortable({ id: pack.id })
  return <PackCard pack={pack} active running={running} onToggle={onToggle} dragHandle={dragHandle} />
}

export default function Packs({ name }: { name: string }) {
  const key = encodeURIComponent(name)
  const qc = useQueryClient()
  const [order, setOrder] = useState<Order | null>(null)
  const [file, setFile] = useState<File | null>(null)
  const [url, setURL] = useState('')
  const [source, setSource] = useState<'file' | 'url'>('file')
  const [draggingFile, setDraggingFile] = useState(false)
  const [notice, setNotice] = useState('')
  const [error, setError] = useState('')
  const listing = useQuery({ queryKey: ['packs', key], queryFn: async () => (await api.get<Listing>(`/server-details/${key}/packs`)).data, refetchInterval: 10000 })
  useEffect(() => {
    if (!listing.data || order) return
    const selected = (kind: Pack['kind']) => listing.data!.packs.filter(p => p.kind === kind && p.active).sort((a, b) => a.order - b.order).map(p => p.id)
    setOrder({ resource: selected('resource'), behavior: selected('behavior') })
  }, [listing.data, order])
  const save = useMutation({ mutationFn: async (value: Order) => (await api.put(`/server-details/${key}/packs`, value)).data, onSuccess: async () => { setNotice('Saved to the world pack JSON files. Restart the server to apply changes.'); setError(''); await qc.invalidateQueries({ queryKey: ['packs', key] }) }, onError: err => setError(message(err)) })
  const upload = useMutation({ mutationFn: async () => {
    if (source === 'file' && file) { const body = new FormData(); body.append('file', file); return (await api.post(`/server-details/${key}/packs`, body)).data }
    return (await api.post(`/server-details/${key}/packs`, { url })).data
  }, onSuccess: async (data: { installed: number }) => { setNotice(`Installed ${data.installed} pack${data.installed === 1 ? '' : 's'}. Activate below, save, then restart the server.`); setError(''); setFile(null); setURL(''); setOrder(null); await qc.invalidateQueries({ queryKey: ['packs', key] }) }, onError: err => setError(message(err)) })
  const current = order || { resource: [], behavior: [] }
  const saved = (kind: Pack['kind']) => listing.data?.packs.filter(p => p.kind === kind && p.active).sort((a, b) => a.order - b.order).map(p => p.id) || []
  const dirty = (['resource', 'behavior'] as const).some(kind => JSON.stringify(current[kind]) !== JSON.stringify(saved(kind)))
  const sensors = useSensors(useSensor(PointerSensor, { activationConstraint: { distance: 6 } }), useSensor(TouchSensor, { activationConstraint: { delay: 180, tolerance: 8 } }), useSensor(KeyboardSensor, { coordinateGetter: sortableKeyboardCoordinates }))
  function change(next: Order) { setOrder(next); setNotice('') }
  function reorder(kind: Pack['kind'], event: DragEndEvent) {
    const { active, over } = event
    if (!over || active.id === over.id) return
    const ids = [...current[kind]], source = ids.indexOf(String(active.id)), destination = ids.indexOf(String(over.id))
    if (source < 0 || destination < 0) return
    ids.splice(source, 1); ids.splice(destination, 0, String(active.id))
    change({ ...current, [kind]: ids })
  }
  function toggle(pack: Pack) {
    const ids = current[pack.kind]
    change({ ...current, [pack.kind]: ids.includes(pack.id) ? ids.filter(id => id !== pack.id) : [...ids, pack.id] })
  }
  function dropFile(event: DragEvent<HTMLElement>) { event.preventDefault(); setDraggingFile(false); if (event.dataTransfer.files[0]) setFile(event.dataTransfer.files[0]) }
  function submit(event: FormEvent<HTMLFormElement>) { event.preventDefault(); upload.mutate() }
  return <section className="packs-page">
    <div className="packs-heading"><p className="kicker">BEDROCK ADD-ONS</p><h2>Resource & behavior packs</h2><p className="muted">Drag active packs to set their order. Handles work with a mouse, touch, or keyboard.</p></div>
    {notice && <p className="notice" role="status">{notice}</p>}{error && <p className="error" role="alert">{error}</p>}
    {listing.isLoading && <p className="muted">Loading packs…</p>}{listing.isError && <p className="error" role="alert">{message(listing.error)}</p>}
    {listing.data && <><p className="muted pack-world">World: {listing.data.world} · Load status comes from recent Pack Stack logs.</p>
      {(['resource', 'behavior'] as const).map(kind => {
        const packs = listing.data!.packs.filter(pack => pack.kind === kind)
        const byID = new Map(packs.map(pack => [pack.id, pack]))
        const active = current[kind].map(id => byID.get(id)).filter((pack): pack is Pack => Boolean(pack))
        const inactive = packs.filter(pack => !current[kind].includes(pack.id) && !pack.builtIn).sort((a, b) => a.name.localeCompare(b.name))
        const builtIn = packs.filter(pack => !current[kind].includes(pack.id) && pack.builtIn).sort((a, b) => a.name.localeCompare(b.name))
        return <section className="panel pack-section" key={kind}><div className="pack-section-heading"><h3>{kind === 'resource' ? 'Resource packs' : 'Behavior packs'}</h3><span>{active.length} active</span></div>
          {active.length ? <DndContext sensors={sensors} collisionDetection={closestCenter} onDragEnd={event => reorder(kind, event)}><SortableContext items={current[kind]} strategy={verticalListSortingStrategy}><div className="pack-list">{active.map(pack => <SortablePack key={pack.id} pack={pack} running={listing.data!.running} onToggle={() => toggle(pack)} />)}</div></SortableContext></DndContext> : <p className="muted">No active packs.</p>}
          {inactive.length > 0 && <details className="pack-disclosure"><summary>Inactive packs <span>{inactive.length}</span></summary><div className="pack-list">{inactive.map(pack => <PackCard key={pack.id} pack={pack} active={false} running={listing.data!.running} onToggle={() => toggle(pack)} />)}</div></details>}
          {builtIn.length > 0 && <details className="pack-disclosure pack-system"><summary>Built-in Minecraft packs <span>{builtIn.length}</span></summary><p className="muted">Versioned vanilla and editor packs supplied with Bedrock.</p><div className="pack-list">{builtIn.map(pack => <PackCard key={pack.id} pack={pack} active={false} running={listing.data!.running} onToggle={() => toggle(pack)} />)}</div></details>}
        </section>
      })}
      <div className="pack-save-row"><button className="primary" type="button" disabled={save.isPending || !dirty} onClick={() => save.mutate(current)}>{save.isPending ? 'Saving…' : 'Save changes'}</button><p>Writes activation and order to the world’s pack JSON files. It does not restart the server; restart it to apply changes.</p></div>
    </>}
    <section className="panel pack-section pack-install"><h3>Add packs</h3><p className="muted">Install one or more packs from a local archive or a public HTTPS link.</p><div className="pack-source-tabs" role="group" aria-label="Pack source"><button type="button" className={source === 'file' ? 'selected' : ''} onClick={() => setSource('file')}>Upload file</button><button type="button" className={source === 'url' ? 'selected' : ''} onClick={() => setSource('url')}>Download URL</button></div><form onSubmit={submit}>
      {source === 'file' ? <label className={`pack-dropzone ${draggingFile ? 'dragging' : ''}`} onDragEnter={event => { event.preventDefault(); setDraggingFile(true) }} onDragOver={event => event.preventDefault()} onDragLeave={event => { if (!event.currentTarget.contains(event.relatedTarget as Node)) setDraggingFile(false) }} onDrop={dropFile}><input type="file" accept=".zip,.mcaddon,.mcpack,.tar.gz,.tgz" onChange={event => setFile(event.target.files?.[0] || null)} /><span className="pack-upload-icon" aria-hidden="true">↑</span><strong>{file ? file.name : 'Drop an archive here'}</strong><small>{file ? 'Click to choose a different file' : 'or click to choose a file · ZIP, MCADDON, MCPACK, tar.gz, tgz'}</small></label>
        : <label className="pack-url-label">Direct download link<input type="url" required placeholder="https://example.com/addon.mcaddon" value={url} onChange={event => setURL(event.target.value)} /><small>Public HTTPS links only. The archive is removed after installation.</small></label>}
      <button className="primary" disabled={upload.isPending || (source === 'file' ? !file : !url)}>{upload.isPending ? 'Installing…' : 'Install packs'}</button>
    </form></section>
  </section>
}
