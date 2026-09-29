import test from 'node:test'
import assert from 'node:assert/strict'
import vm from 'node:vm'
import { readFileSync } from 'node:fs'
const source = readFileSync(new URL('../public/sw.js', import.meta.url), 'utf8')
function worker(clients = []) {
  const handlers = {}, notices = [], opened = []
  const self = {
    addEventListener: (name, handler) => { handlers[name] = handler },
    location: { origin: 'https://mc.example.com' },
    registration: { showNotification: async (title, options) => { notices.push({ title, ...options }) } },
    clients: { matchAll: async () => clients, openWindow: async url => { opened.push(url) } },
  }
  vm.runInNewContext(source, { self, URL })
  async function dispatch(name, event) { let work; handlers[name]({ ...event, waitUntil: promise => { work = promise } }); await work }
  return { notices, opened, dispatch }
}
test('push shows visible notification with correct destination', async () => {
  const w = worker()
  await w.dispatch('push', { data: { json: () => ({ title: 'Survival', body: '✨ Alex joined the adventure!', url: '/servers/survival' }) } })
  assert.equal(w.notices[0].title, 'Survival')
  assert.equal(w.notices[0].body, '✨ Alex joined the adventure!')
  assert.equal(w.notices[0].data.url, '/servers/survival')
})
test('malformed or missing payload still displays a notification', async () => {
  const w = worker()
  await w.dispatch('push', {})
  await w.dispatch('push', { data: { json: () => { throw new Error('invalid') } } })
  assert.equal(w.notices.length, 2)
  assert.equal(w.notices[1].title, 'Your world')
})
test('notification click opens server, never an external site', async () => {
  const w = worker()
  let closed = false
  await w.dispatch('notificationclick', { notification: { data: { url: '/servers/survival' }, close: () => { closed = true } } })
  await w.dispatch('notificationclick', { notification: { data: { url: 'https://evil.example/' }, close() {} } })
  assert.equal(closed, true)
  assert.deepEqual(w.opened, ['https://mc.example.com/servers/survival', 'https://mc.example.com/'])
})
test('notification click reuses and focuses an existing MCUI window', async () => {
  let destination, focused = false
  const w = worker([{ url: 'https://mc.example.com/', navigate: async url => { destination = url }, focus: async () => { focused = true } }])
  await w.dispatch('notificationclick', { notification: { data: { url: '/servers/one' }, close() {} } })
  assert.equal(destination, 'https://mc.example.com/servers/one')
  assert.equal(focused, true)
  assert.equal(w.opened.length, 0)
})

test('uses server icon for notifications, rejecting external icon URLs', async () => {
  const w = worker()
  const icon = `/server-icons/one/${'a'.repeat(64)}.png`
  await w.dispatch('push', { data: { json: () => ({ title: 'Family', icon }) } })
  await w.dispatch('push', { data: { json: () => ({ icon: 'https://untrusted.example/image.png' }) } })
  assert.equal(w.notices[0].icon, icon)
  assert.equal(w.notices[1].icon, '/icons/icon-192.png?v=grass-block-1')
})
test('invalid click URL falls back to dashboard', async () => {
  const w = worker()
  await w.dispatch('notificationclick', { notification: { data: { url: 'https://[' }, close() {} } })
  assert.deepEqual(w.opened, ['https://mc.example.com/'])
})
