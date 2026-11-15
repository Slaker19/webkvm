import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { mount, unmount, flushSync } from 'svelte';

// Guards three audited defects in ClusterModuleCard:
//  A10 — the LINSTOR checkbox stayed editable after preflight and the
//        install sent the *current* flag, not the one the plan used.
//  M20 — onMount read the store before refresh resolved, so a reload
//        during an install never started polling.
//  M22 — a 409 `uninstall_blocked` only surfaced as a toast; the
//        blockers list was lost.
const fx = vi.hoisted(() => ({
  module: { enabled: false, busy: false, status: { state: 'not_installed' } },
  getCalls: 0,
  tier: '',
  family: '',
  planExtra: null,
  preflightCalls: [],
  installCalls: [],
  uninstallError: null,
  getError: null,
  getFailAfter: Infinity,
}));

vi.mock('$lib/stores/auth.svelte.js', () => {
  const api = {
    getHAModule: () => {
      fx.getCalls++;
      if (fx.getError || fx.getCalls > fx.getFailAfter) {
        return Promise.reject(fx.getError || new Error('boom'));
      }
      return Promise.resolve(fx.module);
    },
    preflightHAModule: (opts) => {
      fx.preflightCalls.push(opts.linstor);
      return Promise.resolve({
        ...(fx.planExtra || {}),
        preflight: {
          ok: true,
          distro: 'ubuntu',
          version: '26.04',
          tier: fx.tier || 'supported',
          family: fx.family || undefined,
          checks: [],
        },
        packages: [{ name: 'pacemaker', origin: 'ubuntu', installed: false }],
      });
    },
    installHAModule: (opts) => {
      fx.installCalls.push(opts);
      return Promise.resolve({ state: 'installing', steps: [] });
    },
    uninstallHAModule: () =>
      fx.uninstallError
        ? Promise.reject(fx.uninstallError)
        : Promise.resolve({ state: 'uninstalling' }),
    verifyHAModule: () => Promise.resolve(fx.module.status),
  };
  return { api, auth: { user: { role: 'admin' } } };
});

import ClusterModuleCard from './components/ClusterModuleCard.svelte';
import { clusterModule } from './stores/clusterModule.svelte.js';

// Captured before fake timers are installed so settle() keeps working.
const realSetTimeout = globalThis.setTimeout;
const settle = async () => {
  for (let i = 0; i < 6; i++) {
    await new Promise((r) => realSetTimeout(r, 0));
    flushSync();
  }
};

const buttonByText = (root, text) =>
  [...root.querySelectorAll('button')].find((b) => (b.textContent || '').trim().startsWith(text));

const openAdvanced = async (root) => {
  buttonByText(root, 'Advanced options').click();
  await settle();
};

const linstorBox = (root) =>
  [...root.querySelectorAll('input[type=checkbox]')].find((i) =>
    (i.closest('label')?.textContent || '').includes('LINSTOR')
  );

function setChecked(input, value) {
  input.checked = value;
  input.dispatchEvent(new Event('change', { bubbles: true }));
}

let mounted = null;

beforeEach(() => {
  fx.module = { enabled: false, busy: false, status: { state: 'not_installed' } };
  fx.getCalls = 0;
  fx.preflightCalls = [];
  fx.installCalls = [];
  fx.uninstallError = null;
  fx.getError = null;
  fx.getFailAfter = Infinity;
  fx.tier = '';
  fx.family = '';
  fx.planExtra = null;
  clusterModule.error = '';
  clusterModule.loaded = false;
  clusterModule.loading = false;
  clusterModule.status = null;
  clusterModule.busy = false;
  clusterModule.enabled = false;
});

afterEach(() => {
  if (mounted) {
    unmount(mounted.app);
    mounted.target.remove();
    mounted = null;
  }
  vi.useRealTimers();
  document.body.innerHTML = '';
});

async function mountCard() {
  const target = document.createElement('div');
  document.body.appendChild(target);
  const app = mount(ClusterModuleCard, { target });
  await settle();
  mounted = { target, app };
  return target;
}

describe('ClusterModuleCard LINSTOR flag (A10)', () => {
  it('locks the checkbox once a plan exists and installs with the planned flag', async () => {
    const target = await mountCard();
    await openAdvanced(target);
    const box = linstorBox(target);
    expect(box.disabled).toBe(false);

    setChecked(box, true);
    await settle();
    buttonByText(target, 'Install').click();
    await settle();

    expect(fx.preflightCalls).toEqual([true]);
    expect(linstorBox(target).disabled).toBe(true);

    buttonByText(target, 'Install now').click();
    await settle();
    expect(fx.installCalls).toEqual([{ linstor: true }]);
  });

  it('invalidates the plan when the flag changes', async () => {
    const target = await mountCard();
    await openAdvanced(target);
    buttonByText(target, 'Install').click();
    await settle();
    expect(fx.preflightCalls).toEqual([false]);
    expect(buttonByText(target, 'Install now')).toBeTruthy();

    // Simulates a change that slipped past the disabled control.
    setChecked(linstorBox(target), true);
    await settle();

    expect(buttonByText(target, 'Install now')).toBeUndefined();
    expect(fx.installCalls).toEqual([]);
  });
});

describe('ClusterModuleCard polling after reload (M20)', () => {
  it('starts polling when the refreshed status is installing', async () => {
    vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout'] });
    fx.module = { enabled: false, busy: true, status: { state: 'installing', steps: [] } };
    await mountCard();
    const before = fx.getCalls;
    expect(before).toBeGreaterThanOrEqual(1);

    vi.advanceTimersByTime(1600);
    await settle();
    expect(fx.getCalls).toBeGreaterThan(before);
  });

  it('starts polling when the refreshed status is uninstalling', async () => {
    vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout'] });
    fx.module = { enabled: false, busy: true, status: { state: 'uninstalling', steps: [] } };
    await mountCard();
    const before = fx.getCalls;

    vi.advanceTimersByTime(1600);
    await settle();
    expect(fx.getCalls).toBeGreaterThan(before);
  });
});

describe('ClusterModuleCard uninstall blockers (M22)', () => {
  it('lists translated blockers when the backend refuses', async () => {
    fx.module = { enabled: true, busy: false, status: { state: 'installed', steps: [] } };
    const err = new Error('refusing to remove the module');
    err.status = 409;
    err.code = 'uninstall_blocked';
    err.data = {
      error: 'refusing to remove the module',
      code: 'uninstall_blocked',
      blockers: [
        'pacemaker_cluster_configured',
        'config_present:/etc/corosync/corosync.conf',
        'config_check_failed:/var/lib/pacemaker/cib/cib.xml',
      ],
    };
    fx.uninstallError = err;

    const target = await mountCard();
    buttonByText(target, 'Uninstall').click();
    await settle();

    const confirm = [...document.body.querySelectorAll('button')]
      .filter((b) => (b.textContent || '').trim().startsWith('Uninstall'))
      .pop();
    expect(confirm).toBeTruthy();
    confirm.click();
    await settle();

    const text = document.body.textContent || '';
    expect(text).toContain('Uninstall blocked');
    expect(text).toContain('A Pacemaker cluster is configured on this node.');
    expect(text).toContain('/etc/corosync/corosync.conf');
    expect(text).toContain('/var/lib/pacemaker/cib/cib.xml');
    expect(text).toContain('Could not check');
  });
});

describe('ClusterModuleCard units and load errors', () => {
  it('renders units_active/unknown_units with the exact command and no mask copy', async () => {
    fx.module = {
      enabled: false,
      busy: false,
      status: {
        state: 'partial',
        failed_step: 'install_packages',
        units_active: ['corosync.service'],
        unknown_units: ['sbd.service'],
        commands: ['sudo systemctl disable --now corosync.service'],
        steps: [
          { id: 'mask_services', state: 'done' },
          { id: 'snapshot_units', state: 'done' },
        ],
      },
    };
    const target = await mountCard();
    const text = target.textContent || '';
    expect(text).toContain('corosync.service');
    expect(text).toContain('sudo systemctl disable --now corosync.service');
    expect(text).toContain('sbd.service');
    expect(text).not.toContain('mask_services');
    expect(text.toLowerCase()).not.toContain('mask');
    expect(text).toContain('Record the state of cluster services');
  });

  it('keeps a failed install diagnosis and offers copy/retry actions', async () => {
    fx.module = {
      enabled: false,
      busy: false,
      status: {
        state: 'not_installed',
        failed_step: 'install_packages',
        cause: 'sandbox_readonly',
        error: "dpkg: unable to create '/usr/bin/x': Read-only file system",
        missing: ['pacemaker', 'corosync'],
        commands: ['sudo apt-get install -y pacemaker corosync'],
        steps: [{ id: 'install_packages', state: 'failed' }],
      },
    };
    const target = await mountCard();
    const text = target.textContent || '';
    expect(text).toContain('Install packages');
    expect(text).toContain('read-only filesystem');
    expect(text).toContain('Read-only file system');
    expect(buttonByText(target, 'Retry')).toBeTruthy();
    expect(buttonByText(target, 'Copy diagnostics')).toBeTruthy();
    expect(buttonByText(target, 'Copy error')).toBeTruthy();
    expect(buttonByText(target, 'Copy commands')).toBeTruthy();
  });

  it('shows an error with Retry, never "Not installed", when the status request fails', async () => {
    fx.getError = Object.assign(new Error('forbidden'), { status: 403 });
    const target = await mountCard();
    const text = target.textContent || '';
    expect(text).toContain('Could not read the Cluster/HA module status');
    expect(text).not.toContain('Not installed');
    expect(buttonByText(target, 'Install cluster/HA dependencies')).toBeUndefined();
    fx.getError = null;
    buttonByText(target, 'Retry').click();
    await settle();
    expect(target.textContent).toContain('Not installed');
  });

  it('disabling the module asks for confirmation first', async () => {
    fx.module = { enabled: true, busy: false, status: { state: 'installed', steps: [] } };
    const target = await mountCard();
    buttonByText(target, 'Disable module').click();
    await settle();
    expect(document.body.textContent).toContain('Disable the Cluster/HA module?');
    expect(document.body.textContent).toContain('/api/ha');
  });

  it('stops polling after 5 consecutive errors and offers Retry', async () => {
    vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout'] });
    fx.module = { enabled: false, busy: true, status: { state: 'installing', steps: [] } };
    const target = await mountCard();
    fx.getFailAfter = fx.getCalls;
    for (let i = 0; i < 8; i++) {
      vi.advanceTimersByTime(1600);
      await settle();
    }
    expect(fx.getCalls).toBe(fx.getFailAfter + 5);
    expect(target.textContent).toContain('Lost contact while following the operation');
    expect(buttonByText(target, 'Retry')).toBeTruthy();
  });

  it('does not report success when an uninstall ends in a failed state', async () => {
    vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout'] });
    fx.module = { enabled: false, busy: true, status: { state: 'uninstalling', steps: [] } };
    const target = await mountCard();
    fx.module = { enabled: false, busy: false, status: { state: 'installed', steps: [] } };
    vi.advanceTimersByTime(1600);
    await settle();
    expect(buttonByText(target, 'Uninstall packages')).toBeTruthy();
    expect(target.textContent).not.toContain('Disable module');
  });
});

describe('ClusterModuleCard plan variants', () => {
  it('guided plan hides the install button and shows copyable manual commands', async () => {
    fx.planExtra = { guided: true, manual_commands: ['sudo dnf install pacemaker'] };
    const target = await mountCard();
    buttonByText(target, 'Install cluster/HA dependencies').click();
    await settle();
    expect(buttonByText(target, 'Install now')).toBeUndefined();
    expect(target.textContent).toContain('sudo dnf install pacemaker');
    expect(buttonByText(target, 'Copy commands')).toBeTruthy();
    expect(buttonByText(target, 'Verify again')).toBeTruthy();
  });

  it('accept_required gates the install behind the checkbox and sends accept_experimental', async () => {
    fx.planExtra = { accept_required: true };
    const target = await mountCard();
    buttonByText(target, 'Install cluster/HA dependencies').click();
    await settle();
    const install = buttonByText(target, 'Install now');
    expect(install.disabled).toBe(true);
    const box = [...target.querySelectorAll('input[type=checkbox]')].find((i) =>
      (i.closest('label')?.textContent || '').includes('experimental')
    );
    setChecked(box, true);
    await settle();
    expect(buttonByText(target, 'Install now').disabled).toBe(false);
    buttonByText(target, 'Install now').click();
    await settle();
    expect(fx.installCalls).toEqual([{ linstor: false, accept_experimental: true }]);
  });

  it('shows the exact command from status.commands for units_active', async () => {
    fx.module = {
      enabled: false,
      busy: false,
      status: {
        state: 'partial',
        cause: 'services_active',
        failed_step: 'verify',
        units_active: ['corosync.service'],
        unknown_units: [],
        commands: ['sudo systemctl disable --now corosync.service'],
        steps: [],
      },
    };
    const target = await mountCard();
    const pres = [...target.querySelectorAll('pre')].map((p) => p.textContent);
    expect(pres).toContain('sudo systemctl disable --now corosync.service');
  });
});

describe('ClusterModuleCard guided plan and layout', () => {
  const openGuidedPlan = async (family, distro = 'ubuntu') => {
    fx.family = family;
    fx.tier = 'guided';
    fx.planExtra = { guided: true, manual_commands: ['sudo x'] };
    const target = await mountCard();
    buttonByText(target, 'Install cluster/HA dependencies').click();
    await settle();
    void distro;
    return target;
  };

  it('guided plan shows ONE warning alert (not error) with dnf wording and no AUR text', async () => {
    const target = await openGuidedPlan('dnf');
    const alerts = [...target.querySelectorAll('[role=status],[role=alert]')];
    expect(alerts.length).toBe(1);
    expect(alerts[0].getAttribute('role')).toBe('status');
    expect(alerts[0].className).toContain('warning');
    expect(alerts[0].className).not.toContain('destructive');
    expect(alerts[0].textContent.replace(/\s+/g, ' ')).toContain('Support level: Guided (manual)');
    expect(alerts[0].textContent).toContain('dnf');
    expect(alerts[0].textContent).not.toContain('AUR');
    expect(target.textContent).not.toContain('Guided installation: WebKVM will not');
  });

  it('guided plan on pacman mentions AUR helpers; unknown family stays neutral', async () => {
    let target = await openGuidedPlan('pacman');
    expect(target.textContent).toContain('AUR');
    unmount(mounted.app);
    mounted.target.remove();
    mounted = null;
    clusterModule.loaded = false;
    target = await openGuidedPlan('something-new');
    expect(target.textContent).not.toContain('AUR');
    expect(target.textContent).not.toContain('dnf');
    expect(target.textContent).toContain('cannot install packages on this distribution');
  });

  it('hides the "N package(s) to install" line for guided plans only', async () => {
    let target = await openGuidedPlan('dnf');
    expect(target.textContent).not.toContain('to install,');
    unmount(mounted.app);
    mounted.target.remove();
    mounted = null;
    clusterModule.loaded = false;
    fx.tier = '';
    fx.family = '';
    fx.planExtra = null;
    target = await mountCard();
    buttonByText(target, 'Install cluster/HA dependencies').click();
    await settle();
    expect(target.textContent).toContain('1 package(s) to install, 0 already installed');
  });

  it('advanced options toggle is an outline button with a chevron and aria-expanded', async () => {
    const target = await mountCard();
    const toggle = buttonByText(target, 'Advanced options');
    expect(toggle.getAttribute('aria-expanded')).toBe('false');
    expect(toggle.className).toContain('border-border');
    expect(toggle.querySelector('svg')).toBeTruthy();
    expect(toggle.className).not.toContain('min-h-11');
    toggle.click();
    await settle();
    const open = buttonByText(target, 'Hide advanced options');
    expect(open.getAttribute('aria-expanded')).toBe('true');
  });

  it('keeps the status badge and the enabled tag together and wraps long commands', async () => {
    fx.module = {
      enabled: false,
      busy: false,
      status: {
        state: 'partial',
        failed_step: 'packages',
        commands: ['sudo apt-get install -y a'],
      },
    };
    const target = await mountCard();
    const group = target.querySelector('[data-testid=module-badges]');
    expect(group.textContent).toContain('Module disabled');
    expect(group.textContent).toContain('Partial failure');
    const pre = [...target.querySelectorAll('pre')].find((p) =>
      p.textContent.includes('apt-get install')
    );
    expect(pre.className).toContain('whitespace-pre-wrap');
    expect(pre.className).toContain('break-all');
  });
});
