import type { CSSProperties } from 'react'

const colors: Record<string, string> = {
  '0': '#000000', '1': '#0000aa', '2': '#00aa00', '3': '#00aaaa',
  '4': '#aa0000', '5': '#aa00aa', '6': '#ffaa00', '7': '#aaaaaa',
  '8': '#555555', '9': '#5555ff', a: '#55ff55', b: '#55ffff',
  c: '#ff5555', d: '#ff55ff', e: '#ffff55', f: '#ffffff',
  g: '#ddd605',
}
const formatCode = /§([0-9a-z])/gi

export function plainMinecraftText(value: string): string {
  return value.replace(formatCode, '')
}

export default function MinecraftText({ value }: { value: string }) {
  const parts: { text: string; style: CSSProperties }[] = []
  let style: CSSProperties = {}
  let offset = 0
  for (const match of value.matchAll(formatCode)) {
    if (match.index > offset) parts.push({ text: value.slice(offset, match.index), style })
    const code = match[1].toLowerCase()
    if (code in colors) style = { color: colors[code] }
    else if (code === 'r') style = {}
    else if (code === 'l') style = { ...style, fontWeight: 700 }
    else if (code === 'o') style = { ...style, fontStyle: 'italic' }
    else if (code === 'm') style = { ...style, textDecoration: 'line-through' }
    else if (code === 'n') style = { ...style, textDecoration: 'underline' }
    offset = match.index + match[0].length
  }
  if (offset < value.length) parts.push({ text: value.slice(offset), style })
  return <>{parts.map((part, index) => <span key={index} style={part.style}>{part.text}</span>)}</>
}
