/* Notifications only: dashboard/API responses are never cached. */
self.addEventListener('install', () => self.skipWaiting())
self.addEventListener('activate', event => event.waitUntil(self.clients.claim()))
self.addEventListener('push', event => {
  let data = {}
  try { data = event.data?.json() || {} } catch { /* Still display a visible notification. */ }
  event.waitUntil(self.registration.showNotification(data.title || 'Your world', {
    body: data.body || '✨ Something new in your world!',
    icon: typeof data.icon === 'string' && /^\/server-icons\/[^/]+\/[a-f0-9]{64}\.png$/.test(data.icon) ? data.icon : '/icons/icon-192.png?v=grass-block-1',
    badge: '/icons/badge.png?v=grass-block-1',
    data: { url: data.url || '/' },
  }))
})
self.addEventListener('notificationclick', event => {
  event.notification.close()
  let target
  try { target = new URL(event.notification.data?.url || '/', self.location.origin) } catch { target = new URL('/', self.location.origin) }
  if (target.origin !== self.location.origin || !target.pathname.startsWith('/servers/')) target.href = self.location.origin + '/'
  // Open an HTTPS page first so it can hand off to Minecraft and offer a fallback.
  if (target.pathname.startsWith('/servers/')) target.searchParams.set('join', '1')
  event.waitUntil((async () => {
    const windows = await self.clients.matchAll({ type: 'window', includeUncontrolled: true })
    for (const client of windows) {
      if (new URL(client.url).origin === target.origin && 'navigate' in client) {
        await client.navigate(target.href)
        return client.focus()
      }
    }
    return self.clients.openWindow(target.href)
  })())
})
