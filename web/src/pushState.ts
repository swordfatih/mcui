export function applicationKey(value: string): ArrayBuffer {
  const raw = atob(value.replace(/-/g, '+').replace(/_/g, '/'))
  return Uint8Array.from(raw, char => char.charCodeAt(0)).buffer
}
export function currentKey(subscription: PushSubscription, publicKey: string) {
  const current = subscription.options.applicationServerKey
  const expected = new Uint8Array(applicationKey(publicKey))
  return !!current && current.byteLength === expected.length && new Uint8Array(current).every((byte, index) => byte === expected[index])
}
export async function readBrowserPush(registration: ServiceWorkerRegistration, publicKey: string) {
  const subscription = await registration.pushManager.getSubscription()
  let permission: NotificationPermission = Notification.permission
  // Safari versions differ in permissionState support. Notification.permission
  // and getSubscription remain the fallback; neither detects every OS setting.
  if (registration.pushManager.permissionState) {
    try {
      const pushPermission = await registration.pushManager.permissionState({ userVisibleOnly: true, applicationServerKey: applicationKey(publicKey) })
      if (pushPermission !== 'granted') permission = pushPermission === 'prompt' ? 'default' : 'denied'
    } catch { /* Use Notification.permission when the optional API is unavailable. */ }
  }
  const usable = permission === 'granted' && !!subscription && currentKey(subscription, publicKey)
    && (subscription.expirationTime === null || subscription.expirationTime > Date.now())
  return { registration, subscription, permission, usable }
}
