import { useState, type DragEvent } from 'react'

export default function ArchiveDropzone({ file, accept, formats, disabled = false, onFile }: {
  file: File | null
  accept: string
  formats: string
  disabled?: boolean
  onFile: (file: File | null) => void
}) {
  const [dragging, setDragging] = useState(false)
  function drop(event: DragEvent<HTMLLabelElement>) {
    event.preventDefault()
    setDragging(false)
    if (!disabled && event.dataTransfer.files[0]) onFile(event.dataTransfer.files[0])
  }

  return <label className={`pack-dropzone ${dragging ? 'dragging' : ''}`} onDragEnter={event => { event.preventDefault(); if (!disabled) setDragging(true) }} onDragOver={event => { event.preventDefault(); event.dataTransfer.dropEffect = disabled ? 'none' : 'copy' }} onDragLeave={event => { if (!event.currentTarget.contains(event.relatedTarget as Node)) setDragging(false) }} onDrop={drop}>
    <input type="file" accept={accept} disabled={disabled} onChange={event => onFile(event.target.files?.[0] || null)} />
    <span className="pack-upload-icon" aria-hidden="true">↑</span>
    <strong>{file ? file.name : 'Drop an archive here'}</strong>
    <small>{file ? 'Click to choose a different file' : `or click to choose a file · ${formats}`}</small>
  </label>
}
