/**
 * Firewall templates (V13-C-02) + helpers (2.5.0).
 *
 * Presets the operator can load into the host-firewall editor. Each
 * template builds a set of Input (host) and Forward (to a VM) rules.
 * Forward rules reference a GUEST_IP placeholder the operator must
 * replace with the real VM address before applying (the editor offers
 * a VM→IP picker for that).
 *
 * Everything here is pure and unit-tested (vitest) — no DOM, no api:
 * builders, merge (append skipping exact duplicates), conflict
 * detection (duplicates + shadowed rows, nft evaluates top-down) and
 * the per-VM presets (inbound allow rules for the VmDetail editor).
 */

const GUEST_IP = '10.0.0.10'; // placeholder the operator must edit

// Management rails a host forward must never steal (SSH + noVNC range;
// the web UI port is dynamic and checked separately). Kept in sync with
// backend HostPorts (minus webPort). Used by tests to pin the template.
export const PROTECTED_RAIL_PORTS = [22, 5900, 5901, 5902, 5903];

let seq = 0;
export function uid(prefix = 'r') {
  seq += 1;
  return `${prefix}_${Date.now().toString(36)}_${seq.toString(36)}`;
}

export function newInputRule() {
  return { id: uid('in'), proto: 'tcp', port: 0, src: '', action: 'allow', name: '' };
}

export function newForwardRule() {
  return { id: uid('fwd'), proto: 'tcp', host_port: 0, guest_ip: '', guest_port: 0, name: '' };
}

/** Keep input rules in the order the operator sees them (top = first). */
export function moveRule(list, index, dir) {
  const target = index + dir;
  if (target < 0 || target >= list.length) return list;
  const next = [...list];
  const [item] = next.splice(index, 1);
  next.splice(target, 0, item);
  return next;
}

export const FIREWALL_TEMPLATES = [
  {
    id: 'web-server',
    labelKey: 'firewall.tplWebServer',
    descKey: 'firewall.tplWebServerDesc',
    build: () => ({
      input: [
        { id: uid('in'), name: 'HTTP', proto: 'tcp', port: 80, src: '', action: 'allow' },
        { id: uid('in'), name: 'HTTPS', proto: 'tcp', port: 443, src: '', action: 'allow' },
      ],
      forwards: [
        {
          id: uid('fwd'),
          name: 'HTTP → VM',
          proto: 'tcp',
          host_port: 80,
          guest_ip: GUEST_IP,
          guest_port: 80,
        },
        {
          id: uid('fwd'),
          name: 'HTTPS → VM',
          proto: 'tcp',
          host_port: 443,
          guest_ip: GUEST_IP,
          guest_port: 443,
        },
      ],
    }),
  },
  {
    id: 'ssh-gateway',
    labelKey: 'firewall.tplSshGateway',
    descKey: 'firewall.tplSshGatewayDesc',
    build: () => ({
      input: [],
      forwards: [
        {
          id: uid('fwd'),
          name: 'SSH → VM',
          proto: 'tcp',
          host_port: 2222,
          guest_ip: GUEST_IP,
          guest_port: 22,
        },
      ],
    }),
  },
  {
    id: 'media',
    labelKey: 'firewall.tplMedia',
    descKey: 'firewall.tplMediaDesc',
    build: () => ({
      input: [
        { id: uid('in'), name: 'Jellyfin', proto: 'tcp', port: 8096, src: '', action: 'allow' },
        { id: uid('in'), name: 'WireGuard', proto: 'udp', port: 51820, src: '', action: 'allow' },
      ],
      forwards: [
        {
          id: uid('fwd'),
          name: 'Jellyfin → VM',
          proto: 'tcp',
          host_port: 8096,
          guest_ip: GUEST_IP,
          guest_port: 8096,
        },
        {
          id: uid('fwd'),
          name: 'WireGuard → VM',
          proto: 'udp',
          host_port: 51820,
          guest_ip: GUEST_IP,
          guest_port: 51820,
        },
      ],
    }),
  },
  {
    id: 'database',
    labelKey: 'firewall.tplDatabase',
    descKey: 'firewall.tplDatabaseDesc',
    build: () => ({
      input: [],
      forwards: [
        {
          id: uid('fwd'),
          name: 'PostgreSQL → VM',
          proto: 'tcp',
          host_port: 5432,
          guest_ip: GUEST_IP,
          guest_port: 5432,
        },
        {
          id: uid('fwd'),
          name: 'MySQL/MariaDB → VM',
          proto: 'tcp',
          host_port: 3306,
          guest_ip: GUEST_IP,
          guest_port: 3306,
        },
      ],
    }),
  },
  {
    id: 'mail',
    labelKey: 'firewall.tplMail',
    descKey: 'firewall.tplMailDesc',
    build: () => ({
      input: [
        { id: uid('in'), name: 'SMTP', proto: 'tcp', port: 25, src: '', action: 'allow' },
        { id: uid('in'), name: 'Submission', proto: 'tcp', port: 587, src: '', action: 'allow' },
        { id: uid('in'), name: 'IMAPS', proto: 'tcp', port: 993, src: '', action: 'allow' },
      ],
      forwards: [
        {
          id: uid('fwd'),
          name: 'SMTP → VM',
          proto: 'tcp',
          host_port: 25,
          guest_ip: GUEST_IP,
          guest_port: 25,
        },
        {
          id: uid('fwd'),
          name: 'Submission → VM',
          proto: 'tcp',
          host_port: 587,
          guest_ip: GUEST_IP,
          guest_port: 587,
        },
        {
          id: uid('fwd'),
          name: 'IMAPS → VM',
          proto: 'tcp',
          host_port: 993,
          guest_ip: GUEST_IP,
          guest_port: 993,
        },
      ],
    }),
  },
  {
    id: 'dns',
    labelKey: 'firewall.tplDns',
    descKey: 'firewall.tplDnsDesc',
    build: () => ({
      input: [
        { id: uid('in'), name: 'DNS (TCP)', proto: 'tcp', port: 53, src: '', action: 'allow' },
        { id: uid('in'), name: 'DNS (UDP)', proto: 'udp', port: 53, src: '', action: 'allow' },
      ],
      forwards: [
        {
          id: uid('fwd'),
          name: 'DNS TCP → VM',
          proto: 'tcp',
          host_port: 53,
          guest_ip: GUEST_IP,
          guest_port: 53,
        },
        {
          id: uid('fwd'),
          name: 'DNS UDP → VM',
          proto: 'udp',
          host_port: 53,
          guest_ip: GUEST_IP,
          guest_port: 53,
        },
      ],
    }),
  },
  {
    id: 'monitoring',
    labelKey: 'firewall.tplMonitoring',
    descKey: 'firewall.tplMonitoringDesc',
    build: () => ({
      input: [
        { id: uid('in'), name: 'Grafana', proto: 'tcp', port: 3000, src: '', action: 'allow' },
        { id: uid('in'), name: 'Prometheus', proto: 'tcp', port: 9090, src: '', action: 'allow' },
      ],
      forwards: [
        {
          id: uid('fwd'),
          name: 'Grafana → VM',
          proto: 'tcp',
          host_port: 3000,
          guest_ip: GUEST_IP,
          guest_port: 3000,
        },
        {
          id: uid('fwd'),
          name: 'Prometheus → VM',
          proto: 'tcp',
          host_port: 9090,
          guest_ip: GUEST_IP,
          guest_port: 9090,
        },
      ],
    }),
  },
  {
    id: 'remote-desktop',
    labelKey: 'firewall.tplRemoteDesktop',
    descKey: 'firewall.tplRemoteDesktopDesc',
    build: () => ({
      input: [],
      forwards: [
        {
          id: uid('fwd'),
          name: 'RDP → VM',
          proto: 'tcp',
          host_port: 3389,
          guest_ip: GUEST_IP,
          guest_port: 3389,
        },
        {
          id: uid('fwd'),
          name: 'VNC → VM',
          proto: 'tcp',
          // NOTE: never 5900-5903 — those are the host's own protected
          // noVNC rails; a DNAT forward on them would steal the host's
          // console traffic in prerouting, before the input chain.
          host_port: 15900,
          guest_ip: GUEST_IP,
          guest_port: 5900,
        },
      ],
    }),
  },
  {
    id: 'minecraft',
    labelKey: 'firewall.tplMinecraft',
    descKey: 'firewall.tplMinecraftDesc',
    build: () => ({
      input: [
        { id: uid('in'), name: 'Minecraft', proto: 'tcp', port: 25565, src: '', action: 'allow' },
      ],
      forwards: [
        {
          id: uid('fwd'),
          name: 'Minecraft → VM',
          proto: 'tcp',
          host_port: 25565,
          guest_ip: GUEST_IP,
          guest_port: 25565,
        },
      ],
    }),
  },
  {
    id: 'home-assistant',
    labelKey: 'firewall.tplHomeAssistant',
    descKey: 'firewall.tplHomeAssistantDesc',
    build: () => ({
      input: [
        {
          id: uid('in'),
          name: 'Home Assistant',
          proto: 'tcp',
          port: 8123,
          src: '',
          action: 'allow',
        },
      ],
      forwards: [
        {
          id: uid('fwd'),
          name: 'Home Assistant → VM',
          proto: 'tcp',
          host_port: 8123,
          guest_ip: GUEST_IP,
          guest_port: 8123,
        },
      ],
    }),
  },
  {
    id: 'openvpn',
    labelKey: 'firewall.tplOpenVpn',
    descKey: 'firewall.tplOpenVpnDesc',
    build: () => ({
      input: [
        { id: uid('in'), name: 'OpenVPN', proto: 'udp', port: 1194, src: '', action: 'allow' },
      ],
      forwards: [
        {
          id: uid('fwd'),
          name: 'OpenVPN → VM',
          proto: 'udp',
          host_port: 1194,
          guest_ip: GUEST_IP,
          guest_port: 1194,
        },
      ],
    }),
  },
];

export function buildTemplate(id) {
  const tpl = FIREWALL_TEMPLATES.find((t) => t.id === id);
  if (!tpl) return { input: [], forwards: [] };
  return tpl.build();
}

const norm = (v) => (v === undefined || v === null ? '' : String(v));

/** Identity of an input/VM rule for merge + conflict detection. */
export function inputKey(r) {
  return ['in', norm(r.proto), norm(r.port), norm(r.src), norm(r.action)].join('|');
}

/** Identity of a forward (host guest_ip or per-VM target_ip). */
export function forwardKey(f) {
  return [
    'fwd',
    norm(f.proto),
    norm(f.host_port),
    norm(f.guest_ip || f.target_ip),
    norm(f.guest_port),
  ].join('|');
}

/**
 * Merge: append template rows that are not exact duplicates of what
 * the editor already holds. Never mutates the inputs. Returns the
 * merged lists plus how many rows were actually added (for the toast).
 */
export function mergeFirewall(current, incoming) {
  const seenInput = new Set((current.input || []).map(inputKey));
  const seenFwd = new Set((current.forwards || []).map(forwardKey));
  const addedInput = (incoming.input || []).filter((r) => !seenInput.has(inputKey(r)));
  const addedForwards = (incoming.forwards || []).filter((f) => !seenFwd.has(forwardKey(f)));
  return {
    input: [...(current.input || []), ...addedInput],
    forwards: [...(current.forwards || []), ...addedForwards],
    addedInput: addedInput.length,
    addedForwards: addedForwards.length,
  };
}

/** Duplicate a row with a fresh id (duplicate-row buttons). */
export function duplicateItem(item) {
  return { ...item, id: uid('r') };
}

function protoSet(p) {
  if (p === 'both') return ['tcp', 'udp'];
  return [p || 'tcp'];
}

function protosOverlap(a, b) {
  return protoSet(a).some((p) => protoSet(b).includes(p));
}

// Conflict kinds: 'duplicate' (same row twice — harmless but noisy) and
// 'shadowed' (same proto+port+src with a different action below an
// earlier row — nft evaluates top-down, so it can never match).
// Disabled rows and incomplete drafts (port 0) are ignored: they do not
// render and must not warn.
export function findConflicts(input, forwards) {
  const out = [];
  const seen = new Map();
  (input || []).forEach((r, i) => {
    if (r.disabled || !Number(r.port)) return;
    const k = inputKey(r);
    if (seen.has(k)) {
      out.push({ kind: 'duplicate', scope: 'input', index: i, proto: r.proto, port: r.port });
      return;
    }
    seen.set(k, i);
    for (let j = 0; j < i; j++) {
      const o = input[j];
      if (!o || o.disabled || !Number(o.port)) continue;
      if (String(o.port) !== String(r.port) || norm(o.src) !== norm(r.src)) continue;
      if (!protosOverlap(o.proto, r.proto)) continue;
      if (o.action !== r.action) {
        out.push({
          kind: 'shadowed',
          scope: 'input',
          index: i,
          proto: r.proto,
          port: r.port,
          byIndex: j,
        });
        break;
      }
    }
  });
  const seenF = new Map();
  (forwards || []).forEach((f, i) => {
    if (f.disabled || !Number(f.host_port)) return;
    const k = forwardKey(f);
    if (seenF.has(k)) {
      out.push({
        kind: 'duplicate',
        scope: 'forward',
        index: i,
        proto: f.proto,
        port: f.host_port,
      });
    } else {
      seenF.set(k, i);
    }
  });
  return out;
}

// --- Per-VM presets (2.5.0) -----------------------------------------
// Inbound allow rules for the VmDetail firewall editor. The per-VM model
// has no name/src fields, so presets are port+action only; forwards stay
// manual (the operator picks a free host port per VM).

function vmRules(...ports) {
  return ports.map(([proto, port]) => ({ id: uid('in'), proto, port, action: 'allow' }));
}

export const VM_TEMPLATE_PRESETS = [
  {
    id: 'vm-web',
    labelKey: 'firewall.vmTplWeb',
    descKey: 'firewall.vmTplWebDesc',
    build: () => ({ rules: vmRules(['tcp', 80], ['tcp', 443]), forwards: [] }),
  },
  {
    id: 'vm-ssh',
    labelKey: 'firewall.vmTplSsh',
    descKey: 'firewall.vmTplSshDesc',
    build: () => ({ rules: vmRules(['tcp', 22]), forwards: [] }),
  },
  {
    id: 'vm-database',
    labelKey: 'firewall.vmTplDatabase',
    descKey: 'firewall.vmTplDatabaseDesc',
    build: () => ({ rules: vmRules(['tcp', 5432], ['tcp', 3306]), forwards: [] }),
  },
  {
    id: 'vm-mail',
    labelKey: 'firewall.vmTplMail',
    descKey: 'firewall.vmTplMailDesc',
    build: () => ({ rules: vmRules(['tcp', 25], ['tcp', 587], ['tcp', 993]), forwards: [] }),
  },
  {
    id: 'vm-dns',
    labelKey: 'firewall.vmTplDns',
    descKey: 'firewall.vmTplDnsDesc',
    build: () => ({ rules: vmRules(['tcp', 53], ['udp', 53]), forwards: [] }),
  },
  {
    id: 'vm-minecraft',
    labelKey: 'firewall.vmTplMinecraft',
    descKey: 'firewall.vmTplMinecraftDesc',
    build: () => ({ rules: vmRules(['tcp', 25565]), forwards: [] }),
  },
  {
    id: 'vm-home-assistant',
    labelKey: 'firewall.vmTplHomeAssistant',
    descKey: 'firewall.vmTplHomeAssistantDesc',
    build: () => ({ rules: vmRules(['tcp', 8123]), forwards: [] }),
  },
  {
    id: 'vm-remote-desktop',
    labelKey: 'firewall.vmTplRemoteDesktop',
    descKey: 'firewall.vmTplRemoteDesktopDesc',
    build: () => ({ rules: vmRules(['tcp', 3389]), forwards: [] }),
  },
  {
    id: 'vm-vpn',
    labelKey: 'firewall.vmTplVpn',
    descKey: 'firewall.vmTplVpnDesc',
    build: () => ({ rules: vmRules(['udp', 1194]), forwards: [] }),
  },
];

export function buildVMTemplate(id) {
  const tpl = VM_TEMPLATE_PRESETS.find((t) => t.id === id);
  if (!tpl) return { rules: [], forwards: [] };
  return tpl.build();
}
