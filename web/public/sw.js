/* Notifications only: dashboard/API responses are never cached. */
self.addEventListener('install', () => self.skipWaiting())
self.addEventListener('activate', event => event.waitUntil(self.clients.claim()))
self.addEventListener('push', event => {
  let data = {}
  try { data = event.data?.json() || {} } catch { /* Still display a visible notification. */ }
  event.waitUntil(self.registration.showNotification(data.title || 'MCUI', {
    body: data.body || 'New server activity',
    icon: '/icons/icon-192.png',
    badge: '/icons/badge.png',
    data: { url: data.url || '/' },
  }))
})
self.addEventListener('notificationclick', event => {
  event.notification.close()
  const target = new URL(event.notification.data?.url || '/', self.location.origin)
  if (target.origin !== self.location.origin || !target.pathname.startsWith('/servers/')) target.href = self.location.origin + '/'
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
