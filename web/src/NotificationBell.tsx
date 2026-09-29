import { useEffect, useState } from 'react'
import { useIsMutating, useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import axios from 'axios'
import { applicationKey, currentKey, readBrowserPush } from './pushState'

const api = axios.create({ baseURL: '/api/push', headers: { 'X-MCUI-Push': '1' } })
type PushConfig = { enabled: boolean; publicKey: string }
type Device = Awaited<ReturnType<typeof readBrowserPush>> & { servers: string[] }
const deviceKey = ['push-device']
const supported = () => window.isSecureContext && 'serviceWorker' in navigator && 'PushManager' in window && 'Notification' in window
const iosNeedsInstall = () => /iPad|iPhone|iPod/.test(navigator.userAgent) || navigator.platform === 'MacIntel' && navigator.maxTouchPoints > 1
const standalone = () => window.matchMedia('(display-mode: standalone)').matches || (navigator as Navigator & { standalone?: boolean }).standalone
let registration: Promise<ServiceWorkerRegistration> | undefined
function register() {
  registration ??= navigator.serviceWorker.register('/sw.js', { updateViaCache: 'none' }).then(() => navigator.serviceWorker.ready).catch(error => { registration = undefined; throw error })
  return registration
}
async function loadDevice(publicKey: string): Promise<Device> {
  const browser = await readBrowserPush(await register(), publicKey)
  const servers = browser.subscription ? (await api.post<{ servers: string[] }>('/subscription', { action: 'status', endpoint: browser.subscription.endpoint, ...(browser.usable ? { subscription: browser.subscription.toJSON() } : {}) })).data.servers : []
  return { ...browser, servers }
}
const errorText = (error: unknown) => axios.isAxiosError(error) ? error.response?.data?.error ?? error.message : error instanceof Error ? error.message : String(error)

export default function NotificationBell({ server, label = server, inline = false }: { server: string; label?: string; inline?: boolean }) {
  const qc = useQueryClient()
  const [message, setMessage] = useState('')
  const config = useQuery({ queryKey: ['push-config'], queryFn: async () => (await api.get<PushConfig>('/config')).data, staleTime: 60_000 })
  const canSubscribe = supported() && !(iosNeedsInstall() && !standalone())
  const device = useQuery({ queryKey: deviceKey, queryFn: () => loadDevice(config.data!.publicKey), enabled: canSubscribe && !!config.data?.enabled, staleTime: 0, refetchInterval: 15_000, refetchOnWindowFocus: 'always', retry: false })
  const busy = useIsMutating({ mutationKey: ['push-preferences'] }) > 0
  const subscribed = !device.isError && NotificationPermissionGranted() && !!device.data?.usable && !!device.data?.servers.includes(server)
  useEffect(() => {
    if (!canSubscribe || !config.data?.enabled) return
    const refresh = () => { if (document.visibilityState === 'visible') void qc.invalidateQueries({ queryKey: deviceKey }) }
    document.addEventListener('visibilitychange', refresh)
    window.addEventListener('focus', refresh)
    let status: PermissionStatus | undefined
    let disposed = false
    if (navigator.permissions?.query) void navigator.permissions.query({ name: 'notifications' as PermissionName }).then(value => {
      if (!disposed) { status = value; status.addEventListener('change', refresh) }
    }).catch(() => {})
    return () => { disposed = true; document.removeEventListener('visibilitychange', refresh); window.removeEventListener('focus', refresh); status?.removeEventListener('change', refresh) }
  }, [canSubscribe, config.data?.enabled, qc])
  const test = useMutation({
    mutationFn: async () => {
      const current = await loadDevice(config.data!.publicKey)
      if (!current.usable || !current.servers.includes(server)) throw new Error('Notifications are off on this device. Enable them before testing.')
      await api.post('/subscription', { action: 'test', server, endpoint: current.subscription!.endpoint })
    },
    onSuccess: () => setMessage('Push service accepted the test. If it does not appear, check notification settings and Focus / Do Not Disturb on this phone.'),
    onError: error => setMessage(errorText(error)),
  })
  const change = useMutation({
    mutationKey: ['push-preferences'],
    mutationFn: async ({ permission }: { permission: Promise<NotificationPermission> }) => {
      if (!device.data || !config.data) throw new Error('Notification setup is still loading. Try again.')
      const state = await readBrowserPush(device.data.registration, config.data.publicKey)
      if (subscribed && state.usable && state.subscription) {
        const result = await api.post<{ servers: string[] }>('/subscription', { action: 'unsubscribe', server, endpoint: state.subscription.endpoint })
        // Keep the browser subscription: other server bells use the same device.
        return { ...state, servers: result.data.servers }
      }
      if (await permission !== 'granted') throw new Error('Notifications are blocked. Allow notifications for MCUI in your browser or device settings, then try again.')
      let subscription = await state.registration.pushManager.getSubscription()
      if (subscription && (!currentKey(subscription, config.data.publicKey) || subscription.expirationTime !== null && subscription.expirationTime <= Date.now())) {
        await subscription.unsubscribe()
        subscription = null
      }
      subscription ??= await state.registration.pushManager.subscribe({ userVisibleOnly: true, applicationServerKey: applicationKey(config.data.publicKey) })
      const result = await api.post<{ servers: string[] }>('/subscription', { action: 'subscribe', server, subscription: subscription.toJSON() })
      return { ...await readBrowserPush(state.registration, config.data.publicKey), servers: result.data.servers }
    },
    onSuccess: state => { qc.setQueryData(deviceKey, state); setMessage(state.usable && state.servers.includes(server) ? 'Join and leave notifications enabled on this device. They stay on after sign-out.' : 'Notifications disabled for this server on this device.') },
    onError: error => setMessage(errorText(error)),
    onSettled: () => { void qc.invalidateQueries({ queryKey: deviceKey }) },
  })
  function toggle() {
    setMessage('')
    if (!window.isSecureContext) { setMessage('Open MCUI over HTTPS to enable notifications.'); return }
    if (iosNeedsInstall() && !standalone()) { setMessage('On iPhone or iPad (iOS 16.4+), use Share → Add to Home Screen, open MCUI from that icon, then tap the bell.'); return }
    if (!supported()) { setMessage('Push notifications are not supported in this browser. Try a current Chrome, Firefox, Safari, or Samsung Internet browser.'); return }
    if (config.isError) { setMessage('Could not load notification settings. Refresh the page to try again.'); return }
    if (!config.data?.enabled) { setMessage('Notifications need server setup: configure MCUI_PUSH_CONTACT with an admin mailto address or public HTTPS URL.'); return }
    if (device.isError) { setMessage('Could not prepare notifications. Refresh the page and check your connection and browser settings.'); void device.refetch(); return }
    // Request permission inside the click handler, before any network awaits.
    const permission = subscribed ? Promise.resolve(Notification.permission) : Notification.requestPermission()
    change.mutate({ permission })
  }
  return <div className={`server-notifications ${inline ? 'notification-inline' : ''}`}>
    <button type="button" className={`notification-bell ${subscribed ? 'is-subscribed' : ''}`} aria-pressed={subscribed}
      aria-label={`${subscribed ? 'Disable' : 'Enable'} player notifications for ${label}`} title={`${subscribed ? 'Disable' : 'Enable'} join/leave notifications`}
      disabled={busy || config.isLoading || canSubscribe && !!config.data?.enabled && device.isLoading} onClick={toggle}>
      <svg viewBox="0 0 24 24" width="21" height="21" fill="none" stroke="currentColor" strokeWidth="1.7" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true"><path d="M18 8a6 6 0 0 0-12 0c0 7-3 7-3 9h18c0-2-3-2-3-9M10 21h4" />{subscribed && <path d="m9 10 2 2 4-4" />}</svg>
    </button>
    {inline && <span>{device.isLoading && canSubscribe && config.data?.enabled ? 'Checking notifications…' : subscribed ? 'Notifications on' : 'Notifications off'}</span>}
    {inline && subscribed && <button className="secondary-action" type="button" disabled={test.isPending} onClick={() => test.mutate()}>{test.isPending ? 'Sending…' : 'Test notification'}</button>}
    {message && <div className="notification-message" role="status"><p>{message}</p><button type="button" onClick={() => setMessage('')} aria-label="Dismiss notification message">×</button></div>}
  </div>
}

function NotificationPermissionGranted() { return typeof Notification !== 'undefined' && Notification.permission === 'granted' }
