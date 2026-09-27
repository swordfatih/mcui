import { useState } from 'react'
import { Link, useNavigate } from 'react-router'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import axios from 'axios'
import { packIconPath, type Pack } from './Packs'
import PackAssets from './PackAssets'

const api = axios.create({ baseURL: '/api' })
const errorMessage = (error: unknown) => axios.isAxiosError(error) ? error.response?.data?.error || error.message : String(error)

export default function PackDetail({ server, kind, folder, stopped }: { server: string; kind: 'resource' | 'behavior'; folder: string; stopped: boolean }) {
  const key = encodeURIComponent(server)
  const folderKey = encodeURIComponent(folder)
  const path = `/server-packs/${key}/${kind}/${folderKey}`
  const back = `/servers/${key}?tab=resources`
  const navigate = useNavigate()
  const qc = useQueryClient()
  const [confirming, setConfirming] = useState(false)
  const pack = useQuery({ queryKey: ['pack', key, kind, folder], queryFn: async () => (await api.get<Pack>(path)).data, refetchInterval: 10000 })
  const deletion = useMutation({ mutationFn: async () => (await api.delete(path)).data, onSuccess: async () => { await qc.invalidateQueries({ queryKey: ['packs', key] }); navigate(back) } })
  const version = pack.data ? Array.isArray(pack.data.version) ? pack.data.version.join('.') : pack.data.version : ''

  return <main className="pack-detail-page">
    <Link className="back-button" to={back}>← Resources</Link>
    {pack.isLoading && <p className="muted">Loading pack…</p>}
    {pack.isError && <p className="error" role="alert">{errorMessage(pack.error)}</p>}
    {pack.data && <>
      <section className="panel pack-detail-hero"><span className="pack-detail-icon" aria-hidden="true">{pack.data.hasIcon ? <img src={packIconPath(server, pack.data)} alt="" /> : kind === 'resource' ? 'R' : 'B'}</span><div><p className="kicker">{kind === 'resource' ? 'RESOURCE PACK' : 'BEHAVIOR PACK'}</p><h1>{pack.data.name}</h1><p className="muted">Version {version} · {pack.data.active ? 'Active in world JSON' : 'Not selected in world JSON'}</p></div></section>
      <section className="panel pack-detail-info"><h2>Pack details</h2><dl><div><dt>UUID</dt><dd>{pack.data.uuid}</dd></div><div><dt>Folder</dt><dd>{folder}</dd></div><div><dt>World position</dt><dd>{pack.data.active ? `#${pack.data.order + 1}` : 'Not in world order'}</dd></div><div><dt>{kind === 'resource' ? 'World selection' : 'Live load'}</dt><dd>{pack.data.active ? pack.data.loadState : 'Not checked for inactive packs'}</dd></div><div><dt>Source</dt><dd>{pack.data.builtIn ? 'Bedrock-provided folder' : 'Installed pack'}</dd></div></dl></section>
      {kind === 'resource' && !pack.data.builtIn && <PackAssets server={server} folder={folder} stopped={stopped} />}
      <section className="panel pack-detail-danger"><h2>Delete pack</h2><p className="muted">Permanently removes this pack’s folder. If selected, its entry is also removed from the world JSON. Restart the server after deletion.</p>
        {pack.data.builtIn ? <p className="muted">Bedrock-provided packs cannot be deleted here.</p> : !stopped ? <p className="muted">Stop the server before deleting this pack.</p> : confirming ? <div className="pack-delete-confirm"><p>Delete <strong>{pack.data.name}</strong> permanently?</p><button type="button" className="pack-delete-button" disabled={deletion.isPending} onClick={() => deletion.mutate()}>{deletion.isPending ? 'Deleting…' : 'Yes, delete pack'}</button><button type="button" className="secondary-action" onClick={() => setConfirming(false)}>Cancel</button></div> : <button type="button" className="pack-delete-button" onClick={() => setConfirming(true)}>Delete pack</button>}
        {deletion.isError && <p className="error" role="alert">{errorMessage(deletion.error)}</p>}
      </section>
    </>}
  </main>
}
