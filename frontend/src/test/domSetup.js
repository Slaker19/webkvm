// happy-dom's value getters dereference internal state that is null on
// some Svelte bindings and throw. That is a harness bug, not app
// behaviour, and the throw aborts the whole reactive render — so it has
// to be neutralised at the source rather than swallowed around flushSync,
// which would hide real failures along with it.
for (const Ctor of [
  globalThis.HTMLInputElement,
  globalThis.HTMLSelectElement,
  globalThis.HTMLTextAreaElement,
]) {
  const proto = Ctor?.prototype;
  if (!proto) continue;
  const desc = Object.getOwnPropertyDescriptor(proto, 'value');
  if (!desc?.get) continue;
  Object.defineProperty(proto, 'value', {
    configurable: true,
    get() {
      try {
        return desc.get.call(this);
      } catch {
        return '';
      }
    },
    set: desc.set,
  });
}

// happy-dom rejects animation.finished on Animation.cancel(), which in
// node surfaces as an unhandled rejection rather than browser silence.
if (typeof process !== 'undefined') {
  process.on('unhandledRejection', (reason) => {
    if (reason?.name === 'AbortError' || reason?.message?.includes('animation was canceled')) {
      return;
    }
  });
}
if (typeof window !== 'undefined') {
  window.addEventListener('unhandledrejection', (e) => {
    if (e.reason?.name === 'AbortError' || e.reason?.message?.includes('animation was canceled')) {
      e.preventDefault();
    }
  });
}
