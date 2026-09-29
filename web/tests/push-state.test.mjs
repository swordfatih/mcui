import test from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import ts from 'typescript'
const source = ts.transpileModule(readFileSync(new URL('../src/pushState.ts', import.meta.url), 'utf8'), { compilerOptions: { module: ts.ModuleKind.ESNext, target: ts.ScriptTarget.ES2022 } }).outputText
const { readBrowserPush, applicationKey } = await import(`data:text/javascript;base64,${Buffer.from(source).toString('base64')}`)
const publicKey = 'AQIDBA'
function setup({ permission = 'granted', pushPermission = 'granted', expirationTime = null, key = publicKey, missing = false, optionalAPI = true, throws = false } = {}) {
  globalThis.Notification = { permission }
  const subscription = missing ? null : { options: { applicationServerKey: applicationKey(key) }, expirationTime }
  let reads = 0
  const registration = { pushManager: {
    getSubscription: async () => { reads++; return subscription },
    ...(optionalAPI ? { permissionState: async options => { assert.equal(options.userVisibleOnly, true); if (throws) throw new Error('Unsupported'); return pushPermission } } : {}),
  } }
  return { registration, reads: () => reads }
}
test('on requires a live subscription and actual granted permission', async () => {
  const s = setup()
  assert.equal((await readBrowserPush(s.registration, publicKey)).usable, true)
  assert.equal(s.reads(), 1)
  Notification.permission = 'denied'
  assert.equal((await readBrowserPush(s.registration, publicKey)).usable, false)
  assert.equal(s.reads(), 2)
})
test('blocked/default permissions, removed subscriptions, expired subscriptions and rotated keys show off', async () => {
  for (const options of [{ permission: 'denied' }, { permission: 'default' }, { pushPermission: 'denied' }, { pushPermission: 'prompt' }, { missing: true }, { expirationTime: Date.now() - 10 }, { key: 'BQYHCA' }]) {
    const s = setup(options)
    assert.equal((await readBrowserPush(s.registration, publicKey)).usable, false, JSON.stringify(options))
  }
})
test('Safari fallback uses Notification.permission when permissionState is absent or unsupported', async () => {
  for (const options of [{ optionalAPI: false }, { throws: true }]) {
    const s = setup(options)
    assert.equal((await readBrowserPush(s.registration, publicKey)).usable, true)
    Notification.permission = 'denied'
    assert.equal((await readBrowserPush(s.registration, publicKey)).usable, false)
  }
})
