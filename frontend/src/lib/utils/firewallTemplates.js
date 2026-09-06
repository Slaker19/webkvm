/**
 * Firewall templates (V13-C-02).
 *
 * Presets the operator can load into the host-firewall editor. Each
 * template builds a set of Input (host) and Forward (to a VM) rules.
 * Forward rules reference a GUEST_IP placeholder the operator must
 * replace with the real VM address before applying.
 *
 * The builders are pure and unit-tested (vitest) — no DOM, no api.
 */

const GUEST_IP = '10.0.0.10'; // placeholder the operator must edit

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
];

export function buildTemplate(id) {
  const tpl = FIREWALL_TEMPLATES.find((t) => t.id === id);
  if (!tpl) return { input: [], forwards: [] };
  return tpl.build();
}
