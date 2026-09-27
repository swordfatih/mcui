import { useMemo, useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import axios from 'axios'

type Layer = { id: string; name: string; selected: boolean }
type Asset = { path: string; layer: string; size: number; archived: boolean }
type Listing = { layers: Layer[]; assets: Asset[] }
type Preview = { count: number; edits: number; changes: string[]; warnings: string[] }
const api = axios.create({ baseURL: '/api' })
const errorText = (error: unknown) => axios.isAxiosError(error) ? error.response?.data?.error || error.message : String(error)
const managedFiles = new Set(['manifest.json', 'pack_icon.png', 'blocks.json', 'textures/terrain_texture.json', 'textures/flipbook_textures.json', 'textures/flipbook_texture.json', 'textures/textures_list.json'])
const assetType = (path: string) => /\.(png|tga|jpe?g|gif|webp)$/i.test(path) ? 'Images' : /\.(ogg|mp3|wav|fsb)$/i.test(path) ? 'Audio' : /\.json$/i.test(path) ? 'JSON' : 'Other'
const assetFolder = (path: string) => path.includes('/') ? path.split('/')[0] : '(root)'

export default function PackAssets({ server, folder, stopped }: { server: string; folder: string; stopped: boolean }) {
  const key = encodeURIComponent(server)
  const url = `/pack-assets/${key}/resource/${encodeURIComponent(folder)}`
  const qc = useQueryClient()
  const listing = useQuery({ queryKey: ['pack-assets', key, folder], queryFn: async () => (await api.get<Listing>(url)).data })
  const [layer, setLayer] = useState('main')
  const [query, setQuery] = useState('')
  const [state, setState] = useState<'all' | 'present' | 'archived'>('all')
  const [type, setType] = useState('all')
  const [folderFilter, setFolderFilter] = useState('all')
  const [selected, setSelected] = useState<string[]>([])
  const [notice, setNotice] = useState('')
  const [preview, setPreview] = useState<Preview | null>(null)
  const inspect = useMutation({ mutationFn: async (action: 'archive' | 'restore') => (await api.post<Preview>(url, { action, layer, paths: selected, preview: true })).data, onSuccess: data => setPreview(data) })
  const change = useMutation({ mutationFn: async (action: 'archive' | 'restore') => (await api.post(url, { action, layer, paths: selected })).data, onSuccess: async () => { setNotice('Assets updated. Restart the server to apply changes.'); setSelected([]); await qc.invalidateQueries({ queryKey: ['pack-assets', key, folder] }) } })
  const layers = listing.data?.layers || []
  const folders = useMemo(() => [...new Set((listing.data?.assets || []).filter(asset => asset.layer === layer).map(asset => assetFolder(asset.path)))].sort(), [listing.data, layer])
  const assets = useMemo(() => (listing.data?.assets || []).filter(asset => asset.layer === layer && (state === 'all' || (state === 'archived') === asset.archived) && (type === 'all' || assetType(asset.path) === type) && (folderFilter === 'all' || assetFolder(asset.path) === folderFilter) && asset.path.toLowerCase().includes(query.toLowerCase())), [listing.data, layer, state, type, folderFilter, query])
  const selectedAssets = (listing.data?.assets || []).filter(asset => asset.layer === layer && selected.includes(asset.path))
  const allArchived = selectedAssets.length > 0 && selectedAssets.every(asset => asset.archived)
  const allPresent = selectedAssets.length > 0 && selectedAssets.every(asset => !asset.archived)
  const action = allArchived ? 'restore' : 'archive'
  function switchLayer(next: string) { setLayer(next); setSelected([]); setPreview(null); setFolderFilter('all') }
  function toggle(path: string) { setPreview(null); setSelected(current => current.includes(path) ? current.filter(item => item !== path) : [...current, path]) }
  return <section className="panel pack-assets"><div className="pack-assets-heading"><div><h2>Assets</h2><p className="muted">Browse and archive files in one pack layer at a time. Archived files are kept in the server’s .mcui folder.</p></div><span>{listing.data?.assets.length || 0} files</span></div>
    {listing.isLoading && <p className="muted">Loading assets…</p>}{listing.isError && <p className="error" role="alert">{errorText(listing.error)}</p>}
    {listing.data && <><div className="pack-assets-toolbar"><label>Pack layer<select value={layer} onChange={event => switchLayer(event.target.value)}>{layers.map(item => <option key={item.id} value={item.id}>{item.name} ({item.id}){item.selected ? ' · selected by world' : ''}</option>)}</select></label><label>Search files<input type="search" placeholder="Name or path" value={query} onChange={event => setQuery(event.target.value)} /></label><label>Folder<select value={folderFilter} onChange={event => setFolderFilter(event.target.value)}><option value="all">All folders</option>{folders.map(folder => <option key={folder} value={folder}>{folder}</option>)}</select></label><label>Type<select value={type} onChange={event => setType(event.target.value)}><option value="all">All types</option>{['Images', 'Audio', 'JSON', 'Other'].map(item => <option key={item}>{item}</option>)}</select></label><label>Status<select value={state} onChange={event => setState(event.target.value as typeof state)}><option value="all">All</option><option value="present">Present</option><option value="archived">Archived</option></select></label></div>
      <p className="muted pack-assets-layer">{layer === 'main' ? 'The main pack provides the base files for every subpack.' : layers.find(item => item.id === layer)?.selected ? 'This subpack is selected in the world JSON.' : 'This subpack is not selected in the world JSON.'} Archiving a subpack file may reveal a file at the same path in the main pack.</p>
      {selected.length > 0 && <div className="pack-assets-selection"><strong>{selected.length} selected</strong><button type="button" className="secondary-action" onClick={() => { setSelected([]); setPreview(null) }}>Clear</button><button type="button" className="secondary-action" disabled={!stopped || inspect.isPending || (!allArchived && !allPresent)} onClick={() => inspect.mutate(action)}>{inspect.isPending ? 'Checking…' : 'Preview changes'}</button>{preview && <button type="button" className="primary" disabled={change.isPending} onClick={() => { change.mutate(action); setPreview(null) }}>{change.isPending ? 'Working…' : action === 'archive' ? 'Archive selected' : 'Restore selected'}</button>}</div>}
      {preview && <div className="pack-assets-preview"><strong>{preview.count} file{preview.count === 1 ? '' : 's'} will be {action === 'archive' ? 'archived' : 'restored'}</strong><p>{preview.edits} JSON file{preview.edits === 1 ? '' : 's'} will be updated.</p>{preview.changes.map((change, index) => <p key={`${change}-${index}`}>{change}</p>)}{preview.warnings.map(warning => <p key={warning}>Review: {warning}</p>)}</div>}
      {!stopped && <p className="muted">Stop the server to archive or restore assets.</p>}{inspect.isError && <p className="error" role="alert">{errorText(inspect.error)}</p>}{change.isError && <p className="error" role="alert">{errorText(change.error)}</p>}{notice && <p className="notice" role="status">{notice}</p>}
      <div className="pack-assets-list">{assets.map(asset => <label className="pack-asset" key={asset.path}><input type="checkbox" disabled={managedFiles.has(asset.path)} checked={selected.includes(asset.path)} onChange={() => toggle(asset.path)} />{/\.(png|jpe?g|gif|webp)$/i.test(asset.path) ? <img className="pack-asset-thumb" src={`${url}?content=1&layer=${encodeURIComponent(layer)}&path=${encodeURIComponent(asset.path)}`} alt="" loading="lazy" /> : <span className="pack-asset-thumb pack-asset-file" aria-hidden="true">{asset.path.split('.').pop()?.slice(0, 4).toUpperCase()}</span>}<span className="pack-asset-copy"><strong>{asset.path.split('/').pop()}</strong><small>{asset.path}</small></span><span className={asset.archived ? 'pack-asset-archived' : 'pack-asset-present'}>{managedFiles.has(asset.path) ? 'Managed catalog' : asset.archived ? 'Archived' : 'Present'}</span><small>{(asset.size / 1024).toFixed(1)} KB</small></label>)}{assets.length === 0 && <p className="muted">No files match these filters.</p>}</div>
    </>}
  </section>
}
