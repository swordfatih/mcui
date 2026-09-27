import { useState } from 'react'
import { Link, useNavigate } from 'react-router'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import axios from 'axios'
import { packIconPath, type Pack } from './Packs'
import PackAssets from './PackAssets'
import MinecraftText, { plainMinecraftText } from './MinecraftText'

const api = axios.create({ baseURL: '/api' })
const errorMessage = (error: unknown) => axios.isAxiosError(error) ? error.response?.data?.error || error.message : String(error)
type Detail = Pack & { subpacks: { folder: string; name: string }[]; selectedSubpack: string }

export default function PackDetail({ server, kind, folder, stopped }: { server: string; kind: 'resource' | 'behavior'; folder: string; stopped: boolean }) {
  const key = encodeURIComponent(server)
  const folderKey = encodeURIComponent(folder)
  const path = `/server-packs/${key}/${kind}/${folderKey}`
  const back = `/servers/${key}?tab=resources`
  const navigate = useNavigate()
  const qc = useQueryClient()
  const [confirming, setConfirming] = useState(false)
  const [draftSubpack, setDraftSubpack] = useState<string | null>(null)
  const pack = useQuery({ queryKey: ['pack', key, kind, folder], queryFn: async () => (await api.get<Detail>(path)).data, refetchInterval: 10000 })
  const selection = draftSubpack ?? pack.data?.selectedSubpack ?? ''
  const savedSubpack = pack.data?.subpacks.find(option => option.folder === pack.data.selectedSubpack)
  const saveSubpack = useMutation({ mutationFn: async (subpack: string) => (await api.put(path, { subpack })).data, onSuccess: async () => { setDraftSubpack(null); await Promise.all([qc.invalidateQueries({ queryKey: ['pack', key, kind, folder] }), qc.invalidateQueries({ queryKey: ['packs', key] }), qc.invalidateQueries({ queryKey: ['pack-assets', server, folder] })]) } })
  const deletion = useMutation({ mutationFn: async () => (await api.delete(path)).data, onSuccess: async () => { await qc.invalidateQueries({ queryKey: ['packs', key] }); navigate(back) } })
  const version = pack.data ? Array.isArray(pack.data.version) ? pack.data.version.join('.') : pack.data.version : ''

  return <main className="pack-detail-page">
    <Link className="back-button" to={back}>← Resources</Link>
    {pack.isLoading && <p className="muted">Loading pack…</p>}
    {pack.isError && <p className="error" role="alert">{errorMessage(pack.error)}</p>}
    {pack.data && <>
      <section className="panel pack-detail-hero"><span className="pack-detail-icon" aria-hidden="true">{pack.data.hasIcon ? <img src={packIconPath(server, pack.data)} alt="" /> : kind === 'resource' ? 'R' : 'B'}</span><div><p className="kicker">{kind === 'resource' ? 'RESOURCE PACK' : 'BEHAVIOR PACK'}</p><h1><MinecraftText value={pack.data.name} /></h1><p className="muted">Version {version} · {pack.data.active ? 'Active in world JSON' : 'Not selected in world JSON'}</p></div></section>
      <section className="panel pack-detail-info"><h2>Pack details</h2><dl><div><dt>UUID</dt><dd>{pack.data.uuid}</dd></div><div><dt>Folder</dt><dd>{folder}</dd></div><div><dt>World position</dt><dd>{pack.data.active ? `#${pack.data.order + 1}` : 'Not in world order'}</dd></div><div><dt>{kind === 'resource' ? 'World selection' : 'Live load'}</dt><dd>{pack.data.active ? pack.data.loadState : 'Not checked for inactive packs'}</dd></div><div><dt>Source</dt><dd>{pack.data.builtIn ? 'Bedrock-provided folder' : 'Installed pack'}</dd></div></dl></section>
      <section className="panel pack-detail-subpacks"><h2>Subpack</h2>{pack.data.subpacks.length ? <><p className="muted">Choose which variation this pack uses in the world. Restart the server after saving.</p><p className="pack-subpack-status">{!pack.data.active ? 'This pack is not active in the world.' : pack.data.selectedSubpack ? savedSubpack ? <>Saved in world JSON: <MinecraftText value={savedSubpack.name} /> ({savedSubpack.folder})</> : `Saved in world JSON: ${pack.data.selectedSubpack} (no longer available)` : 'No subpack is saved in the world JSON. Minecraft chooses automatically based on the pack and device; MCUI cannot determine that runtime choice from these files.'}</p><div className="pack-subpack-controls"><label>World selection<select value={selection} onChange={event => { setDraftSubpack(event.target.value); saveSubpack.reset() }} disabled={!stopped || !pack.data.active || saveSubpack.isPending}><option value="">No explicit choice (Minecraft chooses)</option>{selection && !pack.data.subpacks.some(option => option.folder === selection) && <option value={selection} disabled>{selection} (no longer available)</option>}{pack.data.subpacks.map(option => <option key={option.folder} value={option.folder}>{plainMinecraftText(option.name)} ({option.folder})</option>)}</select></label><button type="button" className="primary" disabled={!stopped || !pack.data.active || selection === pack.data.selectedSubpack || saveSubpack.isPending} onClick={() => saveSubpack.mutate(selection)}>{saveSubpack.isPending ? 'Saving…' : 'Save subpack'}</button></div>{!pack.data.active && <p className="muted">Activate this pack in Resources and save its order before choosing a subpack.</p>}{!stopped && <p className="muted">Stop the server to change its subpack.</p>}{saveSubpack.isSuccess && <p className="notice" role="status">Subpack saved. Restart the server to apply it.</p>}{saveSubpack.isError && <p className="error" role="alert">{errorMessage(saveSubpack.error)}</p>}</> : <p className="muted">This pack does not declare any available subpacks.</p>}</section>
      {kind === 'resource' && !pack.data.builtIn && <PackAssets server={server} folder={folder} stopped={stopped} />}
      <section className="panel pack-detail-danger"><h2>Delete pack</h2><p className="muted">Permanently removes this pack’s folder. If selected, its entry is also removed from the world JSON. Restart the server after deletion.</p>
        {pack.data.builtIn ? <p className="muted">Bedrock-provided packs cannot be deleted here.</p> : !stopped ? <p className="muted">Stop the server before deleting this pack.</p> : confirming ? <div className="pack-delete-confirm"><p>Delete <strong><MinecraftText value={pack.data.name} /></strong> permanently?</p><button type="button" className="pack-delete-button" disabled={deletion.isPending} onClick={() => deletion.mutate()}>{deletion.isPending ? 'Deleting…' : 'Yes, delete pack'}</button><button type="button" className="secondary-action" onClick={() => setConfirming(false)}>Cancel</button></div> : <button type="button" className="pack-delete-button" onClick={() => setConfirming(true)}>Delete pack</button>}
        {deletion.isError && <p className="error" role="alert">{errorMessage(deletion.error)}</p>}
      </section>
    </>}
  </main>
}
