import { useState, type DragEvent } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import axios from 'axios'

type Entry = { name: string; path: string; directory: boolean; size: number; modified: string }
type Listing = { path: string; entries: Entry[] }
type Cleanup = { path: string; name: string; kind: string; reason: string; size: number }
const api = axios.create({ baseURL: '/api' })
const errorText = (error: unknown) => axios.isAxiosError(error) ? error.response?.data?.error || error.message : String(error)
const sizeText = (bytes: number) => bytes < 1024 ? `${bytes} B` : bytes < 1024 ** 2 ? `${(bytes / 1024).toFixed(1)} KB` : bytes < 1024 ** 3 ? `${(bytes / 1024 ** 2).toFixed(1)} MB` : `${(bytes / 1024 ** 3).toFixed(2)} GB`

export default function Files({ server, stopped }: { server: string; stopped: boolean }) {
  const qc = useQueryClient()
  const base = `/server-files/${encodeURIComponent(server)}`
  const [path, setPath] = useState('')
  const [view, setView] = useState<'browse' | 'cleanup'>('browse')
  const [search, setSearch] = useState('')
  const [file, setFile] = useState<File | null>(null)
  const [dragging, setDragging] = useState(false)
  const [deleting, setDeleting] = useState<{ path: string; name: string; directory: boolean } | null>(null)
  const [confirmName, setConfirmName] = useState('')
  const [notice, setNotice] = useState('')
  const query = useQuery({ queryKey: ['files', server, path], queryFn: async () => (await api.get<Listing>(base, { params: { path } })).data, enabled: view === 'browse' })
  const cleanup = useQuery({ queryKey: ['file-cleanup', server], queryFn: async () => (await api.get<{ items: Cleanup[] }>(base, { params: { cleanup: 1 } })).data, enabled: view === 'cleanup' })
  const upload = useMutation({ mutationFn: async () => { const body = new FormData(); body.append('file', file!); return (await api.post(base, body, { params: { path } })).data }, onSuccess: async () => { setFile(null); setNotice('File uploaded.'); await qc.invalidateQueries({ queryKey: ['files', server, path] }) } })
  const remove = useMutation({ mutationFn: async (target: string) => (await api.delete(base, { params: { path: target } })).data, onSuccess: async () => { setNotice('File or folder deleted.'); setDeleting(null); setConfirmName(''); await Promise.all([qc.invalidateQueries({ queryKey: ['files', server] }), qc.invalidateQueries({ queryKey: ['file-cleanup', server] })]) } })
  const download = (target: string) => `/api${base}?path=${encodeURIComponent(target)}&download=1`
  const breadcrumbs = path ? path.split('/') : []
  function browse(next: string) { setPath(next); setSearch(''); setFile(null); setNotice('') }
  function askDelete(target: { path: string; name: string; directory: boolean }) { setDeleting(target); setConfirmName('') }
  function drop(event: DragEvent<HTMLLabelElement>) { event.preventDefault(); setDragging(false); if (event.dataTransfer.files[0]) setFile(event.dataTransfer.files[0]) }
  const entries = (query.data?.entries || []).filter(entry => entry.name.toLowerCase().includes(search.toLowerCase()))

  return <section className="files-page">
    <div className="files-heading"><div><p className="kicker">SERVER STORAGE</p><h2>Files</h2><p className="muted">Browse the server folder, transfer files, and review old copies.</p></div><div className="files-view-tabs" role="group" aria-label="File view"><button className={view === 'browse' ? 'selected' : ''} onClick={() => setView('browse')}>Browse</button><button className={view === 'cleanup' ? 'selected' : ''} onClick={() => setView('cleanup')}>Cleanup review</button></div></div>
    {notice && <p className="notice" role="status">{notice}</p>}
    {view === 'browse' && <><div className="files-toolbar"><nav className="files-breadcrumbs" aria-label="Folder path"><button onClick={() => browse('')}>{server}</button>{breadcrumbs.map((part, index) => <span key={`${part}-${index}`}>/ <button onClick={() => browse(breadcrumbs.slice(0, index + 1).join('/'))}>{part}</button></span>)}</nav><input type="search" aria-label="Search this folder" placeholder="Search this folder" value={search} onChange={event => setSearch(event.target.value)} /></div>
      {query.isLoading && <p className="muted">Loading files…</p>}{query.isError && <p className="error" role="alert">{errorText(query.error)}</p>}
      {query.data && <section className="panel files-list"><div className="files-list-head"><span>Name</span><span>Size</span><span>Modified</span><span>Actions</span></div>{entries.map(entry => <div className="files-row" key={entry.path}><button className="files-name" onClick={() => entry.directory ? browse(entry.path) : window.location.assign(download(entry.path))}><span aria-hidden="true">{entry.directory ? '▣' : '▤'}</span><strong>{entry.name}</strong></button><span>{entry.directory ? 'Folder' : sizeText(entry.size)}</span><span>{new Date(entry.modified).toLocaleString()}</span><div className="files-actions"><a href={download(entry.path)} download aria-label={`Download ${entry.name}`}>Download</a><button disabled={!stopped} onClick={() => askDelete(entry)}>Delete</button></div></div>)}{entries.length === 0 && <p className="muted files-empty">{search ? 'No matching files.' : 'This folder is empty.'}</p>}</section>}
      <section className="panel files-upload"><div><h3>Upload file</h3><p className="muted">Files are added to the current folder. Existing files are never overwritten.</p></div><label className={`files-dropzone ${dragging ? 'dragging' : ''}`} onDragOver={event => event.preventDefault()} onDragEnter={event => { event.preventDefault(); setDragging(true) }} onDragLeave={event => { if (!event.currentTarget.contains(event.relatedTarget as Node)) setDragging(false) }} onDrop={drop}><input type="file" onChange={event => setFile(event.target.files?.[0] || null)} /><strong>{file?.name || 'Drop a file here or choose one'}</strong></label><button className="primary" disabled={!stopped || !file || upload.isPending} onClick={() => upload.mutate()}>{upload.isPending ? 'Uploading…' : 'Upload'}</button>{!stopped && <p className="muted">Stop the server to upload or delete files.</p>}{upload.isError && <p className="error" role="alert">{errorText(upload.error)}</p>}</section>
    </>}
    {view === 'cleanup' && <section className="panel files-cleanup"><h3>Cleanup review</h3><p className="muted">These folders look like saved copies. Bedrock’s vanilla and versioned pack folders are excluded from this list.</p>{cleanup.isLoading && <p className="muted">Scanning backup folders…</p>}{cleanup.isError && <p className="error" role="alert">{errorText(cleanup.error)}</p>}{cleanup.data?.items.length === 0 && <p className="muted">No named backup folders found.</p>}{cleanup.data?.items.map(item => <article className="files-cleanup-item" key={item.path}><div><small>{item.kind} · {sizeText(item.size)}</small><h4>{item.name}</h4><p>{item.reason}</p><code>{item.path}</code></div><div className="files-actions"><a href={download(item.path)} download>Download ZIP</a><button disabled={!stopped} onClick={() => askDelete({ ...item, directory: true })}>Delete</button></div></article>)}{!stopped && <p className="muted">Stop the server to delete a backup folder.</p>}</section>}
    {deleting && <div className="files-dialog-backdrop" role="presentation"><section className="files-dialog panel" role="dialog" aria-modal="true" aria-labelledby="files-delete-title"><h3 id="files-delete-title">Delete {deleting.directory ? 'folder' : 'file'}?</h3><p><strong>{deleting.path}</strong> will be permanently removed{deleting.directory ? ' with everything inside it' : ''}.</p>{deleting.directory && <label>Type <strong>{deleting.name}</strong> to confirm<input value={confirmName} onChange={event => setConfirmName(event.target.value)} autoFocus /></label>}{remove.isError && <p className="error" role="alert">{errorText(remove.error)}</p>}<div className="files-dialog-actions"><button className="secondary-action" onClick={() => setDeleting(null)}>Cancel</button><button className="pack-delete-button" disabled={remove.isPending || (deleting.directory && confirmName !== deleting.name)} onClick={() => remove.mutate(deleting.path)}>{remove.isPending ? 'Deleting…' : 'Delete permanently'}</button></div></section></div>}
  </section>
}
