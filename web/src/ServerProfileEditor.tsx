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
  const [dragging, setDragging] = useState(false)
  useEffect(() => {
    if (!icon) { setPreview(remove ? '' : server.iconUrl || ''); setValidating(false); return }
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
  function selectPicture(files: FileList | null) {
    if (saving || !files?.length) return
    setError('')
    if (files.length !== 1) { setError('Choose one picture at a time.'); return }
    const file = files[0]
    if (!['image/png', 'image/jpeg'].includes(file.type) || file.size > 2 * 1024 * 1024) {
      setError('Choose a PNG or JPEG no larger than 2 MiB.'); return
    }
    if (file === icon) return
    setRemove(false); setValidating(true); setIcon(file)
  }
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
      <div className="profile-fields">
        <div className="profile-field">
          <label htmlFor="server-display-name">Display name</label>
          <div className="profile-name-control">
            <svg aria-hidden="true" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.7" strokeLinecap="round" strokeLinejoin="round"><path d="m15 5 4 4M4 20l4-1L20 7a2.8 2.8 0 0 0-4-4L4 15l-1 6z" /></svg>
            <input id="server-display-name" type="text" value={name} onChange={event => setName(event.target.value)} maxLength={80} placeholder="Give your world a name" autoComplete="off" aria-describedby="profile-name-hint" required disabled={saving} autoFocus />
          </div>
          <div className="profile-field-hint"><span id="profile-name-hint">Shown on server cards and notifications.</span><span>{name.length}/80</span></div>
        </div>
        <div className="profile-field">
          <span className="profile-field-label" id="profile-picture-label">Server picture</span>
          <label className={`pack-dropzone profile-picture-dropzone ${dragging ? 'dragging' : ''} ${saving ? 'is-disabled' : ''}`}
            onDragEnter={event => { event.preventDefault(); if (!saving) setDragging(true) }}
            onDragOver={event => { event.preventDefault(); event.dataTransfer.dropEffect = saving ? 'none' : 'copy' }}
            onDragLeave={event => { if (!event.currentTarget.contains(event.relatedTarget as Node)) setDragging(false) }}
            onDrop={event => { event.preventDefault(); setDragging(false); selectPicture(event.dataTransfer.files) }}>
            <input id="server-icon-file" type="file" accept="image/png,image/jpeg" disabled={saving} aria-labelledby="profile-picture-label" aria-describedby="profile-picture-hint" onChange={event => { selectPicture(event.target.files); event.target.value = '' }} />
            {preview ? <img className="profile-picture-preview" src={preview} alt="Server icon preview" width="72" height="72" /> : <span className="pack-upload-icon" aria-hidden="true"><svg viewBox="0 0 24 24" width="24" height="24" fill="none" stroke="currentColor" strokeWidth="1.7" strokeLinecap="round" strokeLinejoin="round"><path d="M12 16V4m-4 4 4-4 4 4M4 15v4a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2v-4" /></svg></span>}
            <strong>{dragging ? 'Drop your picture here' : validating ? 'Checking your picture…' : icon?.name || (preview ? 'Drop a new picture to replace it' : 'Drop your server picture here')}</strong>
            <small>or click to browse</small>
          </label>
          <div className="profile-picture-footer">
            <p id="profile-picture-hint" className="profile-field-hint">PNG or JPEG · Up to 2 MiB<br />Square, 64×64 to 1024×1024 pixels</p>
            {(preview || icon) && <button type="button" className="profile-remove-picture" disabled={saving} onClick={() => { setIcon(null); setRemove(true); setValidating(false); setError('') }}>Remove picture</button>}
          </div>
        </div>
      </div>
      {error && <p role="alert" className="error">{error}</p>}
      <div className="profile-actions"><button type="submit" className="primary" disabled={saving || validating || !name.trim()}>{saving ? 'Saving…' : validating ? 'Checking image…' : 'Save changes'}</button><button type="button" className="secondary-action" disabled={saving} onClick={onClose}>Cancel</button></div>
    </form>
  </section>
}
