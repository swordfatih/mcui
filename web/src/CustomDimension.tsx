import { useEffect, useRef, useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import axios from 'axios'
import MinecraftText from './MinecraftText'
import ArchiveDropzone from './ArchiveDropzone'

type InstalledDimension = { name: string; id: number; revision: string; state: string; packs: { name: string; kind: string; uuid: string }[] }
type Status = {
  installed?: InstalledDimension[]
  id?: string; state: string; message?: string; completed?: number; total?: number
  dimensions?: { name: string; id: number; chunks: number }[]
  packs?: { name: string; kind: string; uuid: string }[]
}
const errorMessage = (error: unknown) => axios.isAxiosError(error) ? error.response?.data?.error || error.message : String(error)

export default function CustomDimension({ name, disabled, onBusy }: { name: string; disabled: boolean; onBusy: (busy: boolean) => void }) {
  const path = `/api/server-details/${encodeURIComponent(name)}/dimensions`
  const key = ['dimensions', name]
  const qc = useQueryClient()
  const [open, setOpen] = useState(false)
  const [removing, setRemoving] = useState<InstalledDimension | null>(null)
  const [file, setFile] = useState<File | null>(null)
  const [selected, setSelected] = useState('')
  const [error, setError] = useState('')
  const [uploadPercent, setUploadPercent] = useState(0)
  const dialog = useRef<HTMLDialogElement>(null)
  const completedID = useRef('')
  const status = useQuery({ queryKey: key, queryFn: async () => (await axios.get<Status>(path)).data, refetchInterval: 2000 })
  const state = status.data?.state
  const busy = ['analyzing', 'ready', 'importing', 'removing'].includes(state || '')
  const working = ['analyzing', 'importing', 'removing'].includes(state || '')
  const upload = useMutation({
    mutationFn: async () => {
      if (!file) throw new Error('Choose an archive')
      const body = new FormData(); body.append('file', file)
      return (await axios.post<Status>(path, body, { onUploadProgress: event => setUploadPercent(Math.round((event.progress || 0) * 100)) })).data
    },
    onSuccess: data => { qc.setQueryData(key, data); setError(''); setSelected('') },
    onError: err => setError(errorMessage(err)),
  })
  const apply = useMutation({
    mutationFn: async () => (await axios.post<Status>(path, { id: status.data?.id, dimension: selected, backupAcknowledged: true })).data,
    onSuccess: data => { qc.setQueryData(key, data); setError('') }, onError: err => setError(errorMessage(err)),
  })
  const cancel = useMutation({
    mutationFn: async () => (await axios.delete<Status>(path, { params: { id: status.data?.id } })).data,
    onSuccess: data => { qc.setQueryData(key, data); setOpen(false); setError('') }, onError: err => setError(errorMessage(err)),
  })
  const remove = useMutation({
    mutationFn: async () => (await axios.post<Status>(path, { action: 'remove', dimension: removing?.name, revision: removing?.revision, backupAcknowledged: true })).data,
    onSuccess: data => { qc.setQueryData(key, data); setError(''); setRemoving(null); setOpen(false) },
    onError: err => setError(errorMessage(err)),
  })
  useEffect(() => { onBusy(busy || upload.isPending) }, [busy, upload.isPending, onBusy])
  useEffect(() => {
    if (state === 'ready' && status.data?.dimensions?.length === 1) setSelected(status.data.dimensions[0].name)
    if (state === 'complete' && status.data?.id !== completedID.current) {
      completedID.current = status.data?.id || ''
      void qc.invalidateQueries({ queryKey: ['packs', encodeURIComponent(name)] })
    }
  }, [state, status.data, qc, name])
  useEffect(() => { if (open) dialog.current?.showModal(); else dialog.current?.close() }, [open])
  const close = () => { if (state === 'ready') cancel.mutate(); else setOpen(false) }
  const pending = upload.isPending || apply.isPending || cancel.isPending || remove.isPending
  return <section className="panel pack-section dimension-import">
    <div className="pack-section-heading"><div><h3>Custom dimensions</h3><p className="muted">Import a dimension and its behavior and resource packs from a world archive.</p></div><button type="button" className="secondary-action" disabled={!busy && disabled} onClick={() => { setRemoving(null); setOpen(true); setError('') }}>{busy ? 'View progress' : 'Add Custom Dimension'}</button></div>
    {disabled && !busy && <small>Stop the server and save any pack changes before importing.</small>}
    {status.data?.message && state !== 'idle' && <p className={state === 'failed' ? 'error' : 'muted'} role={state === 'failed' ? 'alert' : 'status'}>{status.data.message}</p>}
    {status.data?.installed?.map(dimension => <article className="pack-card" key={dimension.name}><div className="pack-copy"><strong>{dimension.name}</strong><small>{dimension.packs.map(pack => pack.name).join(' · ')}</small></div><button type="button" className="secondary-action" disabled={disabled || busy || pending} onClick={() => { setRemoving(dimension); setError(''); setOpen(true) }}>Remove dimension + packs</button></article>)}
    {status.isError && <p className="error" role="alert">{errorMessage(status.error)}</p>}
    <dialog ref={dialog} className="files-dialog panel dimension-dialog" aria-labelledby="dimension-title" onCancel={event => { event.preventDefault(); if (!pending) close() }}>
      <h3 id="dimension-title">{removing ? `Remove ${removing.name}?` : 'Custom Dimension'}</h3>
      {(state === 'ready' || !busy) && <p className="dimension-warning"><strong>Back up your world before continuing.</strong> {removing ? 'This permanently deletes the dimension, its builds, entities, and its behavior/resource packs. Move all players and their spawn points out first. Pack content used elsewhere will become unavailable.' : 'This imports a custom dimension and its required packs.'} A failed operation could damage your world. MCUI will not create a backup.</p>}
      {error && <p className="error" role="alert">{error}</p>}
      {status.isError && <p className="error" role="alert">{errorMessage(status.error)}</p>}
      {!busy && !removing && <div className="dimension-archive-field"><strong>World archive</strong><ArchiveDropzone file={file} accept=".zip,.mcworld,.mctemplate" formats="ZIP, MCWORLD, MCTEMPLATE · up to 4 GiB" disabled={pending} onFile={selectedFile => { setFile(selectedFile); setError('') }} /></div>}
      {removing && <ul>{removing.packs.map(pack => <li key={pack.uuid}><MinecraftText value={pack.name} /> · {pack.kind}</li>)}</ul>}
      {state === 'ready' && <>
        <label>Custom dimension<select value={selected} onChange={event => setSelected(event.target.value)} disabled={pending}><option value="">Choose a dimension</option>{status.data?.dimensions?.map(dimension => <option key={dimension.name} value={dimension.name}>{dimension.name} · {dimension.chunks.toLocaleString()} chunks</option>)}</select></label>
        <p>The following packs will be installed and activated:</p><ul>{status.data?.packs?.map(pack => <li key={pack.uuid}><MinecraftText value={pack.name} /> · {pack.kind}</li>)}</ul>
        <p>Existing dimensions are preserved. The server must remain stopped until the import finishes.</p>
      </>}
      {(working || upload.isPending) && <div role="status"><p>{upload.isPending ? `Uploading archive… ${uploadPercent}%` : status.data?.message}</p><progress aria-label="Dimension import progress" max={upload.isPending ? 100 : status.data?.total || 1} value={upload.isPending ? uploadPercent : status.data?.total ? status.data.completed || 0 : undefined} /></div>}
      {!busy && state !== 'idle' && <p role="status">{status.data?.message}</p>}
      <div className="files-dialog-actions"><button type="button" className="secondary-action" disabled={pending} onClick={close}>{working ? 'Close' : 'Cancel'}</button>{removing ? <button type="button" className="primary" disabled={pending || disabled || busy} onClick={() => remove.mutate()}>{remove.isPending ? 'Starting…' : 'Remove dimension + packs'}</button> : state === 'ready' ? <button type="button" className="primary" disabled={!selected || pending || disabled} onClick={() => apply.mutate()}>{apply.isPending ? 'Starting…' : 'Import Dimension'}</button> : !busy && <button type="button" className="primary" disabled={!file || pending || disabled} onClick={() => { setUploadPercent(0); upload.mutate() }}>{upload.isPending ? 'Uploading…' : 'Review archive'}</button>}</div>
    </dialog>
  </section>
}
