import { api } from './auth.svelte.js';
import { getLocale } from '../i18n.svelte.js';

function supported() {
  return (
    typeof window !== 'undefined' &&
    'serviceWorker' in navigator &&
    'PushManager' in window &&
    'Notification' in window
  );
}

let isSupported = $state(supported());
let permission = $state(supported() ? Notification.permission : 'denied');
let subscribed = $state(false);
let busy = $state(false);
let error = $state('');

function urlBase64ToUint8Array(base64String) {
  const padding = '='.repeat((4 - (base64String.length % 4)) % 4);
  const base64 = (base64String + padding).replace(/-/g, '+').replace(/_/g, '/');
  const raw = atob(base64);
  const output = new Uint8Array(raw.length);
  for (let i = 0; i < raw.length; i++) output[i] = raw.charCodeAt(i);
  return output;
}

async function refresh() {
  isSupported = supported();
  if (!isSupported) return;
  permission = Notification.permission;
  try {
    const reg = await navigator.serviceWorker.getRegistration('/');
    const sub = await reg?.pushManager.getSubscription();
    subscribed = !!sub;
  } catch {
    subscribed = false;
  }
}

async function enable() {
  busy = true;
  error = '';
  try {
    if (!isSupported) throw new Error('unsupported');
    if (Notification.permission === 'denied') throw new Error('blocked');
    const reg = await navigator.serviceWorker.register('/sw.js');
    await navigator.serviceWorker.ready;
    const { public_key: publicKey } = await api.getWebPushKey();
    const sub = await reg.pushManager.subscribe({
      userVisibleOnly: true,
      applicationServerKey: urlBase64ToUint8Array(publicKey),
    });
    const json = sub.toJSON();
    await api.subscribeWebPush({
      endpoint: json.endpoint,
      keys: json.keys,
      locale: getLocale(),
    });
    subscribed = true;
    permission = Notification.permission;
  } catch (e) {
    error = e?.message || String(e);
  } finally {
    busy = false;
  }
}

async function disable() {
  busy = true;
  error = '';
  try {
    const reg = await navigator.serviceWorker.getRegistration('/');
    const sub = await reg?.pushManager.getSubscription();
    if (sub) {
      await api.unsubscribeWebPush(sub.endpoint);
      await sub.unsubscribe();
    }
    subscribed = false;
  } catch (e) {
    error = e?.message || String(e);
  } finally {
    busy = false;
  }
}

async function test() {
  busy = true;
  error = '';
  try {
    await api.testWebPush(getLocale());
  } catch (e) {
    error = e?.message || String(e);
  } finally {
    busy = false;
  }
}

export const push = {
  get supported() {
    return isSupported;
  },
  get permission() {
    return permission;
  },
  get subscribed() {
    return subscribed;
  },
  get busy() {
    return busy;
  },
  get error() {
    return error;
  },
  refresh,
  enable,
  disable,
  test,
};
