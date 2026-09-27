import { useEffect, useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import axios from 'axios'

type Pack = { id: string; name: string; uuid: string; version: number[] | string; kind: 'resource' | 'behavior'; active: boolean; order: number; loadState: string }
type Listing = { packs: Pack[]; world: string; running: boolean }
type Order = { resource: string[]; behavior: string[] }
const api = axios.create({ baseURL: '/api' })
const message = (error: unknown) => axios.isAxiosError(error) ? error.response?.data?.error || error.message : String(error)

export default function Packs({ name }: { name: string }) {
  const key = encodeURIComponent(name)
  const qc = useQueryClient()
  const [order, setOrder] = useState<Order | null>(null)
  const [file, setFile] = useState<File | null>(null)
  const [url, setURL] = useState('')
  const [notice, setNotice] = useState('')
  const [error, setError] = useState('')
  const [dragged, setDragged] = useState<string | null>(null)
  const [touchStart, setTouchStart] = useState<{ id: string; kind: Pack['kind']; y: number } | null>(null)
  const listing = useQuery({ queryKey: ['packs', key], queryFn: async () => (await api.get<Listing>(`/server-details/${key}/packs`)).data, refetchInterval: 10000 })
  useEffect(() => {
    if (!listing.data || order) return
    const selected = (kind: Pack['kind']) => listing.data!.packs.filter(p => p.kind === kind && p.active).sort((a, b) => a.order - b.order).map(p => p.id)
    setOrder({ resource: selected('resource'), behavior: selected('behavior') })
  }, [listing.data, order])
  const save = useMutation({ mutationFn: async (value: Order) => (await api.put(`/server-details/${key}/packs`, value)).data, onSuccess: async () => { setNotice('Pack order saved. Restart the server to apply changes.'); setError(''); await qc.invalidateQueries({ queryKey: ['packs', key] }) }, onError: err => setError(message(err)) })
  const upload = useMutation({ mutationFn: async () => {
    if (file) { const body = new FormData(); body.append('file', file); return (await api.post(`/server-details/${key}/packs`, body)).data }
    return (await api.post(`/server-details/${key}/packs`, { url })).data
  }, onSuccess: async (data: { installed: number }) => { setNotice(`Installed ${data.installed} pack${data.installed === 1 ? '' : 's'}. Activate below, then restart the server.`); setError(''); setFile(null); setURL(''); setOrder(null); await qc.invalidateQueries({ queryKey: ['packs', key] }) }, onError: err => setError(message(err)) })
  const current = order || { resource: [], behavior: [] }
  function change(next: Order) { setOrder(next); setNotice('') }
  function move(kind: Pack['kind'], id: string, destination: number) {
    const ids = [...current[kind]]; const source = ids.indexOf(id)
    if (source < 0 || destination < 0 || destination >= ids.length || source === destination) return
    ids.splice(source, 1); ids.splice(destination, 0, id)
    change({ ...current, [kind]: ids })
  }
  function toggle(pack: Pack) {
    const ids = current[pack.kind]
    change({ ...current, [pack.kind]: ids.includes(pack.id) ? ids.filter(id => id !== pack.id) : [...ids, pack.id] })
  }
  return <section className="packs-page">
    <div className="packs-heading"><div><p className="kicker">BEDROCK ADD-ONS</p><h2>Resources & behavior packs</h2><p className="muted">Activate packs and drag their handles to set priority. Changes apply after a server restart.</p></div></div>
    {notice && <p className="notice" role="status">{notice}</p>}{error && <p className="error" role="alert">{error}</p>}
    {listing.isLoading && <p className="muted">Loading packs…</p>}{listing.isError && <p className="error" role="alert">{message(listing.error)}</p>}
    {listing.data && <><p className="muted">World: {listing.data.world}. Live load status uses the latest available Pack Stack logs.</p>
      {(['resource', 'behavior'] as const).map(kind => {
        const packs = listing.data!.packs.filter(pack => pack.kind === kind)
        const sorted = [...packs].sort((a, b) => {
          const ai = current[kind].indexOf(a.id), bi = current[kind].indexOf(b.id)
          return ai < 0 ? bi < 0 ? a.name.localeCompare(b.name) : 1 : bi < 0 ? -1 : ai - bi
        })
        return <section className="panel pack-section" key={kind}><h3>{kind === 'resource' ? 'Resource packs' : 'Behavior packs'}</h3>{!packs.length && <p className="muted">No packs installed.</p>}
          <div className="pack-list">{sorted.map(pack => { const index = current[kind].indexOf(pack.id); const active = index >= 0
            return <article className={`pack-card ${active ? 'pack-active' : ''}`} data-pack-id={pack.id} key={pack.id} onDragOver={event => { if (dragged && dragged !== pack.id) event.preventDefault() }} onDrop={event => { event.preventDefault(); if (dragged) move(kind, dragged, index); setDragged(null) }}>
              <button className="pack-handle" type="button" draggable={active} onDragStart={event => { event.dataTransfer.effectAllowed = 'move'; setDragged(pack.id) }} onDragEnd={() => setDragged(null)} onPointerDown={event => { if (event.pointerType === 'touch' && active) { event.currentTarget.setPointerCapture(event.pointerId); setTouchStart({ id: pack.id, kind, y: event.clientY }) } }} onPointerUp={event => { if (!touchStart || touchStart.id !== pack.id) return; const target = document.elementFromPoint(event.clientX, event.clientY)?.closest<HTMLElement>('[data-pack-id]'); const targetIndex = current[kind].indexOf(target?.dataset.packId || ''); if (Math.abs(event.clientY - touchStart.y) > 12) move(kind, pack.id, targetIndex); setTouchStart(null) }} onPointerCancel={() => setTouchStart(null)} aria-label={`Drag ${pack.name} to reorder`} title="Drag to reorder">⠿</button>
              <div className="pack-copy"><strong>{pack.name}</strong><small>{pack.uuid} · v{Array.isArray(pack.version) ? pack.version.join('.') : pack.version}</small><span className="pack-load">{active ? pack.loadState : 'Inactive'}{active && !listing.data!.running ? ' · server stopped' : ''}</span></div>
              <div className="pack-controls"><label><input type="checkbox" checked={active} onChange={() => toggle(pack)} /> Active</label><div><button type="button" disabled={!active || index === 0} onClick={() => move(kind, pack.id, index - 1)} aria-label={`Move ${pack.name} up`}>↑</button><button type="button" disabled={!active || index === current[kind].length - 1} onClick={() => move(kind, pack.id, index + 1)} aria-label={`Move ${pack.name} down`}>↓</button></div></div>
            </article> })}</div></section>
      })}
      <button className="primary pack-save" type="button" disabled={save.isPending} onClick={() => save.mutate(current)}>{save.isPending ? 'Saving…' : 'Save pack order'}</button>
    </>}
    <section className="panel pack-section"><h3>Add packs</h3><p className="muted">Upload a ZIP, MCADDON, MCPACK, tar.gz or tgz archive, or download one from a public HTTPS URL. Multi-pack add-ons are supported.</p><form onSubmit={event => { event.preventDefault(); upload.mutate() }}><label>Archive file<input type="file" accept=".zip,.mcaddon,.mcpack,.tar.gz,.tgz" onChange={event => setFile(event.target.files?.[0] || null)} /></label><span className="muted">or</span><label>Download URL<input type="url" placeholder="https://example.com/addon.mcaddon" value={url} onChange={event => setURL(event.target.value)} /></label><button className="primary" disabled={upload.isPending || (!file && !url)}>{upload.isPending ? 'Installing…' : 'Install packs'}</button></form></section>
  </section>
}
