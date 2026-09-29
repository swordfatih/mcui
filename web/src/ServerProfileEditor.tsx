import { useEffect, useState, type FormEvent } from 'react'
import { useQueryClient } from '@tanstack/react-query'
import axios from 'axios'

type ProfileServer = { name: string; displayName?: string; iconUrl?: string }
export default function ServerProfileEditor({ server, onClose }: { server: ProfileServer; onClose: () => void }) {
  const qc = useQueryClient()
  const [name, setName] = useState(server.displayName || server.name)
  const [icon, setIcon] = useState<File | null>(null)
  const [preview, setPreview] = useState(server.iconUrl || '')
  const [remove, setRemove] = useState(false)
  const [error, setError] = useState('')
  const [saving, setSaving] = useState(false)
  const [validating, setValidating] = useState(false)
  useEffect(() => {
    if (!icon) { setPreview(remove ? '' : server.iconUrl || ''); return }
    const url = URL.createObjectURL(icon)
    let active = true
    setValidating(true)
    const image = new Image()
    image.onload = () => {
      if (!active) return
      setValidating(false)
      if (image.naturalWidth !== image.naturalHeight || image.naturalWidth < 64 || image.naturalWidth > 1024) {
        setError('Choose a square image between 64×64 and 1024×1024 pixels.'); setIcon(null)
      } else setPreview(url)
    }
    image.onerror = () => { if (active) { setValidating(false); setError('Could not open this image. Choose a valid PNG or JPEG.'); setIcon(null) } }
    image.src = url
    return () => { active = false; URL.revokeObjectURL(url) }
  }, [icon, remove, server.iconUrl])
  async function save(event: FormEvent) {
    event.preventDefault()
    if (saving || validating) return
    setSaving(true); setError('')
    try {
      const body = new FormData()
      body.set('displayName', name.trim())
      if (icon) body.set('icon', icon)
      if (remove) body.set('removeIcon', 'true')
      await axios.put(`/api/server-profile/${encodeURIComponent(server.name)}`, body, { headers: { 'X-MCUI-Profile': '1' } })
      await qc.invalidateQueries({ queryKey: ['servers'] })
      onClose()
    } catch (error) { setError(axios.isAxiosError(error) ? error.response?.data?.error || error.message : String(error)) }
    finally { setSaving(false) }
  }
  return <section className="panel server-profile-editor" aria-label="Edit server appearance">
    <h2>Edit server</h2>
    <p className="muted">Choose the name and picture shown in your server list and player notifications.</p>
    <form onSubmit={save}>
      <label htmlFor="server-display-name">Display name</label>
      <input id="server-display-name" value={name} onChange={event => setName(event.target.value)} maxLength={80} required disabled={saving} autoFocus />
      <div className="server-icon-editor">
        {preview && <img src={preview} alt="Server icon preview" width="80" height="80" />}
        <div><label htmlFor="server-icon-file">Server picture</label>
          <input id="server-icon-file" type="file" accept="image/png,image/jpeg" disabled={saving} onChange={event => {
            const file = event.target.files?.[0]; event.target.value = ''; setError('')
            if (!file) return
            if (!['image/png', 'image/jpeg'].includes(file.type) || file.size > 2 * 1024 * 1024) { setError('Choose a PNG or JPEG no larger than 2 MiB.'); return }
            setRemove(false); setIcon(file)
          }} />
          <p className="muted">PNG or JPEG, up to 2 MiB. Square, 64×64 to 1024×1024 pixels.</p>
          {(preview || icon) && <button type="button" className="secondary-action" disabled={saving} onClick={() => { setIcon(null); setRemove(true); setValidating(false) }}>Remove picture</button>}
        </div>
      </div>
      {error && <p role="alert" className="error">{error}</p>}
      <div className="profile-actions"><button type="submit" className="primary" disabled={saving || validating || !name.trim()}>{saving ? 'Saving…' : validating ? 'Checking image…' : 'Save changes'}</button><button type="button" className="secondary-action" disabled={saving} onClick={onClose}>Cancel</button></div>
    </form>
  </section>
}
