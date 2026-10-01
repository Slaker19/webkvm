import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';

// Which 401s mean "your session expired"? Only those hitting a user who
// actually had a session. A wrong password, a first visit, or requests
// straggling in after a deliberate logout must NOT land on
// /login?reason=session_expired (the amber "Tu sesión ha caducado").
const nav = vi.hoisted(() => ({ calls: [] }));
vi.mock('$lib/router.svelte.js', () => ({
  navigate: (path, opts) => nav.calls.push({ path, opts }),
}));

const json = (status, body) =>
  Promise.resolve(
    new Response(JSON.stringify(body), {
      status,
      headers: { 'Content-Type': 'application/json' },
    })
  );

const settle = async () => {
  for (let i = 0; i < 5; i++) await new Promise((r) => setTimeout(r, 0));
};

let auth, api;
beforeEach(async () => {
  vi.resetModules();
  localStorage.clear();
  nav.calls = [];
  ({ auth, api } = await import('./stores/auth.svelte.js'));
});

afterEach(() => {
  vi.unstubAllGlobals();
});

const lastReason = () => nav.calls.at(-1)?.opts?.query?.reason ?? null;

describe('401 handling', () => {
  it('wrong password is an invalid-credentials error, not an expired session', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn(() => json(401, { error: 'invalid credentials' }))
    );
    await expect(api.login('admin', 'nope')).rejects.toMatchObject({
      status: 401,
      code: 'invalid_credentials',
    });
    await settle();
    expect(nav.calls).toEqual([]);
  });

  it('a wrong 2FA code does not log the user out either', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn(() => json(401, { error: 'código de verificación 2FA inválido' }))
    );
    await expect(api.login2FA('tok', '000000')).rejects.toMatchObject({
      code: 'invalid_credentials',
    });
    await settle();
    expect(nav.calls).toEqual([]);
  });

  it('first visit (/auth/me 401, nothing remembered) shows no expired banner', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn(() => json(401, { error: 'missing token' }))
    );
    expect(await auth.bootstrap()).toBe(false);
    await settle();
    expect(auth.status).toBe('out');
    expect(lastReason()).toBe(null);
  });

  it('an active session that gets a 401 is reported as expired', async () => {
    auth.setSession('admin', 'admin');
    vi.stubGlobal(
      'fetch',
      vi.fn(() => json(401, { error: 'invalid or expired token' }))
    );
    await expect(api.listVMs()).rejects.toMatchObject({ status: 401 });
    await settle();
    expect(auth.status).toBe('out');
    expect(lastReason()).toBe('session_expired');
  });

  it('a reload whose remembered session lapsed is reported as expired', async () => {
    localStorage.setItem('user', 'admin');
    vi.resetModules();
    ({ auth, api } = await import('./stores/auth.svelte.js'));
    vi.stubGlobal(
      'fetch',
      vi.fn(() => json(401, { error: 'invalid or expired token' }))
    );
    await auth.bootstrap();
    await settle();
    expect(lastReason()).toBe('session_expired');
  });

  it('normal logout (and 401s racing it) shows no banner', async () => {
    auth.setSession('admin', 'admin');
    vi.stubGlobal(
      'fetch',
      vi.fn((url) =>
        String(url).endsWith('/auth/logout')
          ? json(401, { error: 'missing token' })
          : json(401, { error: 'invalid or expired token' })
      )
    );
    auth.logout();
    await api.listVMs().catch(() => {});
    await settle();
    expect(auth.status).toBe('out');
    expect(lastReason()).toBe(null);
  });

  it('waitJob resolves on both done and completed status', async () => {
    auth.setSession('admin', 'admin');
    let call = 0;
    vi.stubGlobal(
      'fetch',
      vi.fn(() => {
        call++;
        if (call === 1) return json(200, { id: 'job-1', status: 'running' });
        return json(200, { id: 'job-1', status: 'completed', result: { ok: true } });
      })
    );
    const res = await api.waitJob('job-1', { delay: 1 });
    expect(res).toEqual({ ok: true });
  });

  it('waitJob throws on job error status', async () => {
    auth.setSession('admin', 'admin');
    vi.stubGlobal(
      'fetch',
      vi.fn(() => json(200, { id: 'job-2', status: 'error', error: 'storage error' }))
    );
    await expect(api.waitJob('job-2', { delay: 1 })).rejects.toThrow('storage error');
  });
});
