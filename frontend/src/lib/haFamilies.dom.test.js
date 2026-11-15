import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { mount, unmount, flushSync } from 'svelte';

// Per-family rendering of the Cluster/HA install plan (dnf, pacman), the
// compatibility panel and the per-node install hints.
const fx = vi.hoisted(() => ({
  module: { enabled: false, busy: false, status: { state: 'not_installed' } },
  plan: null,
  compat: null,
  compatError: false,
  installCalls: [],
}));

vi.mock('$lib/stores/auth.svelte.js', () => {
  const api = {
    getHAModule: () => Promise.resolve(fx.module),
    preflightHAModule: () => Promise.resolve(fx.plan),
    installHAModule: (opts) => {
      fx.installCalls.push(opts);
      return Promise.resolve({ state: 'installing', steps: [] });
    },
    verifyHAModule: () => Promise.resolve(fx.module.status),
    getHAModuleCompat: () =>
      fx.compatError ? Promise.reject(new Error('boom')) : Promise.resolve(fx.compat),
  };
  return { api, auth: { user: { role: 'admin' } } };
});

import ClusterModuleCard from './components/ClusterModuleCard.svelte';
import HaCompatPanel from './components/HaCompatPanel.svelte';
import NodeInstallGuide from './components/NodeInstallGuide.svelte';
import { clusterModule } from './stores/clusterModule.svelte.js';

const realSetTimeout = globalThis.setTimeout;
const settle = async () => {
  for (let i = 0; i < 6; i++) {
    await new Promise((r) => realSetTimeout(r, 0));
    flushSync();
  }
};
const buttonByText = (root, text) =>
  [...root.querySelectorAll('button')].find((b) => (b.textContent || '').trim().startsWith(text));

let mounted = null;
async function mountComp(Comp, props = {}) {
  const target = document.createElement('div');
  document.body.appendChild(target);
  const app = mount(Comp, { target, props });
  await settle();
  mounted = { target, app };
  return target;
}
async function openPlan(plan) {
  fx.plan = plan;
  const target = await mountComp(ClusterModuleCard);
  buttonByText(target, 'Install cluster/HA dependencies').click();
  await settle();
  return target;
}

const pf = (over = {}) => ({
  ok: true,
  distro: 'fedora',
  version: '41',
  family: 'dnf',
  tier: 'experimental',
  checks: [],
  watchdog: { type: 'hardware', devices: ['/dev/watchdog0'] },
  virt: 'none',
  fencing_hint: 'physical',
  ...over,
});
const pkg = (name, over = {}) => ({
  name,
  origin: 'official',
  layer: 'base',
  guided: false,
  installed: false,
  ...over,
});
const basePlan = (over = {}) => ({
  preflight: pf(),
  family: 'dnf',
  tier: 'experimental',
  accept_required: true,
  can_install: true,
  guided: false,
  guided_steps: [],
  repos_needed: [],
  manual_commands: [],
  packages: [pkg('pacemaker'), pkg('sbd', { layer: 'fencing' })],
  ...over,
});

beforeEach(() => {
  fx.module = { enabled: false, busy: false, status: { state: 'not_installed' } };
  fx.installCalls = [];
  fx.compatError = false;
  Object.assign(clusterModule, {
    error: '',
    loaded: false,
    loading: false,
    status: null,
    busy: false,
    enabled: false,
  });
  Object.defineProperty(navigator, 'clipboard', {
    value: { writeText: vi.fn(() => Promise.resolve()) },
    configurable: true,
  });
});
afterEach(() => {
  if (mounted) {
    unmount(mounted.app);
    mounted.target.remove();
    mounted = null;
  }
  document.body.innerHTML = '';
});

describe('Fedora / RHEL-like (dnf)', () => {
  it('shows the install button behind the experimental confirmation', async () => {
    const target = await openPlan(basePlan());
    expect(target.textContent).toContain('Experimental');
    expect(target.querySelectorAll('[data-testid=origin-badge]')[0].textContent).toBe(
      'Official repo'
    );
    const install = buttonByText(target, 'Install now');
    expect(install.disabled).toBe(true);
    const box = [...target.querySelectorAll('input[type=checkbox]')].find((i) =>
      (i.closest('label')?.textContent || '').includes('experimental')
    );
    box.checked = true;
    box.dispatchEvent(new Event('change', { bubbles: true }));
    await settle();
    buttonByText(target, 'Install now').click();
    await settle();
    expect(fx.installCalls).toEqual([{ linstor: false, accept_experimental: true }]);
  });

  it('RHEL without the HA repo: exact enable command, copy button, no install', async () => {
    const cmd = 'sudo dnf config-manager --set-enabled highavailability';
    const target = await openPlan(
      basePlan({
        preflight: pf({
          ok: false,
          distro: 'rocky',
          checks: [
            {
              id: 'ha_repo',
              ok: false,
              severity: 'blocker',
              detail: 'High Availability repository not enabled',
              hint: cmd,
            },
          ],
        }),
        guided: true,
        repos_needed: [{ id: 'highavailability', command: cmd }],
        guided_steps: [{ id: 'enable_ha_repo', run_as: 'root', commands: [cmd] }],
      })
    );
    expect(target.querySelector('[data-testid=manual-summary]').textContent).toContain(
      '1 manual step(s) required'
    );
    const step = target.querySelector('[data-testid=guided-step-enable_ha_repo]');
    expect(step.textContent).toContain(cmd);
    expect(step.querySelector('[data-testid=run-as]').textContent).toContain('root (sudo)');
    expect(target.textContent).toContain('High Availability repository');
    expect(buttonByText(target, 'Install now').disabled).toBe(true);
    step.querySelector('button').click();
    await settle();
    expect(navigator.clipboard.writeText).toHaveBeenCalledWith(cmd);
  });
});

describe('Arch (pacman, partial)', () => {
  const archPlan = () =>
    basePlan({
      preflight: pf({ distro: 'arch', version: '', family: 'pacman', tier: 'partial' }),
      family: 'pacman',
      tier: 'partial',
      can_install: false,
      guided: true,
      packages: [
        pkg('corosync', { origin: 'aur', guided: true }),
        pkg('sbd', { origin: 'aur', guided: true, layer: 'fencing' }),
      ],
      guided_steps: [
        {
          id: 'aur_build',
          run_as: 'user',
          commands: [
            '# Run as a NORMAL user, never as root.',
            'yay -S --needed corosync sbd',
            'git clone https://aur.archlinux.org/corosync.git && cd corosync && makepkg -si',
          ],
        },
      ],
    });

  it('two-part card: WebKVM part, AUR part, per-command copy, no install button', async () => {
    const target = await openPlan(archPlan());
    expect(target.querySelector('[data-testid=part-webkvm]').textContent).toContain(
      'WebKVM installs these (official repositories)'
    );
    expect(target.querySelector('[data-testid=part-webkvm]').textContent).toContain(
      'Nothing here can be installed by WebKVM'
    );
    const you = target.querySelector('[data-testid=part-you]');
    expect(you.textContent).toContain('You install these as a normal user (AUR)');
    expect(you.textContent).toContain('never builds as root');
    expect(target.querySelector('[data-testid=run-as]').textContent).toContain('never as root');
    expect(buttonByText(target, 'Install now')).toBeUndefined();
    const copyButtons = [...target.querySelectorAll('[data-testid=guided-step-aur_build] button')];
    expect(copyButtons.length).toBe(2); // the comment line has no copy button
    copyButtons[0].click();
    await settle();
    expect(navigator.clipboard.writeText).toHaveBeenCalledWith('yay -S --needed corosync sbd');
    expect(target.textContent).toContain('Partial (part automatic, part manual)');
  });

  it('partial_guided state: pending alert and a Verify button', async () => {
    fx.module = {
      enabled: false,
      busy: false,
      status: {
        state: 'partial_guided',
        missing: ['corosync'],
        guided_missing: ['corosync', 'sbd'],
        units_active: [],
        unknown_units: [],
        steps: [],
      },
    };
    const target = await mountComp(ClusterModuleCard);
    expect(target.textContent).toContain('Manual steps pending');
    expect(target.querySelector('[data-testid=guided-pending]').textContent).toContain(
      'corosync, sbd'
    );
    expect(buttonByText(target, 'I have done the manual steps: verify')).toBeTruthy();
    expect(buttonByText(target, 'Show the manual steps')).toBeTruthy();
  });
});

describe('watchdog and fencing guidance', () => {
  it('warns about softdog and recommends a VM watchdog', async () => {
    const target = await openPlan(
      basePlan({
        preflight: pf({
          watchdog: { type: 'softdog', devices: ['/dev/watchdog'] },
          virt: 'kvm',
          fencing_hint: 'vm',
        }),
      })
    );
    expect(target.querySelector('[data-testid=watchdog-state]').textContent).toContain(
      'does not protect against a frozen kernel'
    );
    expect(target.textContent).toContain('virtual machine (kvm)');
  });
});

describe('HaCompatPanel', () => {
  it('lists members with versions and explains mixed warnings with what to do', async () => {
    fx.compat = {
      clustered: true,
      incus_available: true,
      local: { hostname: 'n1' },
      recommended: false,
      warnings: ['mixed_distro', 'mixed_incus_version', 'mixed_kernel_major', 'members_unknown'],
      members: [
        {
          name: 'n1',
          status: 'Online',
          os_name: 'Ubuntu',
          os_version: '24.04',
          kernel_version: '6.8.0',
          incus_version: '6.0.3',
        },
        {
          name: 'n2',
          status: 'Online',
          os_name: 'Debian',
          os_version: '12',
          kernel_version: '6.1.0',
          incus_version: '6.0.2',
        },
        { name: 'n3', status: 'Offline', error: 'member_unreachable' },
      ],
    };
    const target = await mountComp(HaCompatPanel);
    expect(target.querySelector('[data-testid=mixed-banner]').textContent).toContain(
      'not recommended'
    );
    for (const w of [
      'mixed_distro',
      'mixed_incus_version',
      'mixed_kernel_major',
      'members_unknown',
    ]) {
      expect(target.querySelector(`[data-testid=warn-${w}]`)).toBeTruthy();
    }
    expect(target.textContent).toContain('What to do:');
    const n2 = target.querySelector('[data-testid=compat-row-n2]').textContent;
    expect(n2).toContain('Debian 12');
    expect(n2).toContain('Different distro');
    expect(n2).toContain('Different Incus');
    expect(target.querySelector('[data-testid=compat-row-n3]').textContent).toContain('Unknown');
    expect(target.querySelector('[data-testid=compat-row-n1]').textContent).toContain('this node');
  });

  it('shows the recommended note when everything agrees and survives a load error', async () => {
    fx.compat = {
      clustered: true,
      incus_available: true,
      local: { hostname: 'n1' },
      recommended: true,
      warnings: [],
      members: [
        {
          name: 'n1',
          status: 'Online',
          os_name: 'Ubuntu',
          os_version: '24.04',
          kernel_version: '6.8.0',
          incus_version: '6.0.3',
        },
        {
          name: 'n2',
          status: 'Online',
          os_name: 'Ubuntu',
          os_version: '24.04',
          kernel_version: '6.8.1',
          incus_version: '6.0.3',
        },
      ],
    };
    let target = await mountComp(HaCompatPanel);
    expect(target.querySelector('[data-testid=compat-recommended]')).toBeTruthy();
    expect(target.querySelector('[data-testid=compat-row-n2]').textContent).toContain('Compatible');
    unmount(mounted.app);
    mounted.target.remove();
    mounted = null;
    fx.compatError = true;
    target = await mountComp(HaCompatPanel);
    expect(target.textContent).toContain('Could not read member compatibility');
  });
});

describe('NodeInstallGuide hints', () => {
  it('uses webkvm-cli ha deps and keeps the helper script as fallback', async () => {
    const target = await mountComp(NodeInstallGuide, {
      nodes: [{ id: 'r1', name: 'node-2', type: 'remote', uri: 'qemu+ssh://10.0.0.2/system' }],
    });
    const text = target.textContent;
    expect(text).toContain('sudo webkvm-cli ha deps install --yes');
    expect(text).toContain('sudo webkvm-cli ha deps check');
    expect(text).toContain('sudo webkvm-ha-stack --install --yes');
  });
});
