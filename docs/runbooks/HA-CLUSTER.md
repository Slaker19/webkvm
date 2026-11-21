# HA cluster runbook

Operator guide for WebKVM's optional Cluster/HA feature: installing the
stack, creating the cluster, fencing, managed SSH access to other nodes,
opening and editing VMs from any node, and HA per workload (KVM and Incus).

Evidence labels used in this document:

- **measured** — observed in a private test environment; not a production capacity claim.
- **simulated only** — covered by unit tests, mocks or a temporary CIB copy.
- **not tested live** — implemented and reviewed, never run on a real node.

Companion guide: [GUIDED-VALIDATION.md](GUIDED-VALIDATION.md) (step-by-step UI
checklist for the operator). Spanish version: [HA-CLUSTER.es.md](HA-CLUSTER.es.md).

## Contents

1. [Choose your path](#1-choose-your-path)
2. [Supported distributions](#2-supported-distributions)
3. [Install the dependencies](#3-install-the-dependencies)
4. [Create the cluster from the panel](#4-create-the-cluster-from-the-panel)
5. [Fencing](#5-fencing)
6. [Managed SSH access for remote KVM nodes](#6-managed-ssh-access-for-remote-kvm-nodes)
7. [Open and edit VMs and Incus from any node](#7-open-and-edit-vms-and-incus-from-any-node)
8. [HA per workload (KVM and Incus)](#8-ha-per-workload-kvm-and-incus)
9. [Validation results](#9-validation-results)
10. [Known limits and what is not validated live](#10-known-limits-and-what-is-not-validated-live)
11. [Troubleshooting](#11-troubleshooting)
12. [Reference topology and day-2 operations](#12-reference-topology-and-day-2-operations)

## 1. Choose your path

| | EASY (UI wizard) | ADVANCED (manual commands) |
|---|---|---|
| Dependencies | Nodes page, card "Cluster & HA dependencies", button "Install cluster/HA dependencies" | `webkvm-cli ha deps ...` or `webkvm-ha-stack` |
| Cluster | Nodes page, wizard "Create the cluster" (Easy mode) | `pcs host auth`, `pcs cluster setup`, `pcs stonith sbd enable` by hand |
| SSH to other nodes | Card "Cluster access (managed SSH)" | `ssh-keygen` / `authorized_keys` by hand, then register the node |
| HA per workload | Switch "High availability" on a VM or container, panel "HA workloads" | `pcs` against the CIB (see section 12) |

Both cards and the wizard also have an Advanced mode (package list, optional
layers, two-node clusters, extra fencing). Pick one path per cluster; do not
mix UI-created resources (`ha-kvm-*`, `ha-incus-*`) with hand-written ones
for the same workload.

What each node needs before you start:

1. The same distribution and version on every node (section 2).
2. WebKVM installed on every node, with `modules.cluster_ha` still off until
   the packages are installed.
3. A resolvable hostname and a private cluster network between the nodes.
4. A watchdog device (`ls /dev/watchdog*`): hardware, `i6300esb` in a VM, or
   `softdog` for tests only (section 5).
5. For Incus workloads: an Incus cluster with a `linstor` or `ceph` pool.
6. For remote KVM nodes: an SSH account on each node that can reach libvirt.
7. Root or `sudo` on each node. The wizard and the installer never ask for
   the `hacluster` password.

## 2. Supported distributions

| Distribution | Level | What WebKVM does |
|---|---|---|
| Ubuntu 22.04 or newer | Supported | Automatic install from the distro repositories |
| Debian 12 or newer | Supported | Automatic install from the distro repositories |
| Debian/Ubuntu derivatives | Experimental | Same as above, but you must accept the experimental level |
| Fedora | Experimental | `dnf install` after your confirmation. Run for real on Fedora 44 (DNF5) in a temporary LXC: install, idempotent re-run, failure and restore all passed |
| RHEL-likes (RHEL, Rocky, Alma, CentOS, Oracle) | Experimental | Same as Fedora, but you must enable the High Availability repository by hand (WebKVM never enables repositories). Run for real on Rocky 10 and Alma 9 (repo id `highavailability`; Rocky 9 has the same id). `drbd-utils` is not packaged there: it is shown as manual and never blocks the install |
| Arch, Manjaro and derivatives | Guided | No package of the stack is in core/extra (`pacman -Si` on a real Arch with fresh sync databases: all six are `not found`; all six exist in the AUR): WebKVM only reports the AUR commands (exit 13), never installs, never `-Sy` |

Rules that apply everywhere:

- Never mix distributions in one cluster: one distro and one Incus version
  per cluster. The members table flags mixed distributions, distribution
  versions and Incus versions as a warning.
- DRBD 9 / LINSTOR: only on apt (Ubuntu/Debian, from LINBIT) or guided. The
  LINBIT repository must already be configured by you; WebKVM never adds
  third-party repositories and never pipes a script into a shell. On
  Fedora/RHEL-likes LINSTOR is manual. On Arch `drbd-dkms` is an AUR package
  and LINSTOR is not packaged.
- Arch: WebKVM never runs AUR helpers or `makepkg`, never builds as root and
  never runs a bare `pacman -Sy`. If the package databases are missing it asks
  you to run `sudo pacman -Syu` yourself.
- RHEL-likes: the repository command is shown, for example
  `sudo dnf config-manager --set-enabled highavailability` (needs
  `dnf-plugins-core`, which minimal Rocky/Alma images lack: the guided step
  installs it first; the id can differ on your release: `dnf repolist --all`).
  Real ids: Rocky 9/10 and Alma 9 `highavailability`; Rocky 8 `ha` is not
  verified live.
  RHEL itself uses `subscription-manager repos --enable` and needs a
  subscription with High Availability.

Verified live in temporary LXC containers (install, unit restore, idempotent
re-run, package-manager failure, interrupt): Ubuntu 24.04, Fedora 44 (DNF5),
Rocky 10, Alma 9; Arch (`check`, install flow = guided only). An unprivileged
Arch container needs `DisableSandbox` in `pacman.conf` before any `pacman -S`.

Verified on real nodes: Ubuntu 26.04 (`ha deps check` reported
`apt`/`supported`, watchdog `i6300esb`) and Arch (`ha deps check` reported
`pacman`/`partial`, six AUR packages; nothing was installed). Debian 13 was
not verified live.

## 3. Install the dependencies

The base WebKVM install does not include clustering. Install the stack on
**every** member, one node at a time. `webkvm-ha-stack` is one of three
equivalent entry points, not the only installer.

### 3.1 From the UI

1. Open the Nodes page, card "Cluster & HA dependencies".
2. Easy mode: press "Install cluster/HA dependencies", read the plan and
   confirm. Advanced mode: choose the optional layers (DRBD/LINSTOR, power
   fencing agents), review each package and copy the exact commands.
3. On Fedora/RHEL-likes tick the experimental acknowledgement. On Arch there
   is no install button: run the shown commands, then press "Verify again".
4. Repeat on every node (the "Install on each node" block links to each
   node's own web UI), then press "Enable module" on each one.

### 3.2 From the CLI

```bash
webkvm-cli ha deps check                         # read-only, no root needed
sudo webkvm-cli ha deps install                  # shows the plan, asks to confirm
sudo webkvm-cli ha deps install --yes            # no terminal: --yes is required
sudo webkvm-cli ha deps install --storage        # also DRBD/LINSTOR where packaged
sudo webkvm-cli ha deps install --accept-experimental --yes   # Fedora, RHEL-likes, Arch
webkvm-cli ha deps check --json                  # for automation
```

Exit codes (shared with `webkvm-ha-stack`):

| Code | Meaning |
|---|---|
| 0 | OK |
| 1 | `check` found missing components |
| 2 | Usage or privileges (for example `install` without root or without `--yes`) |
| 10 | Distribution, tier or repository not installable as-is (unsupported, experimental not accepted, HA repository missing, pacman databases missing) |
| 11 | Package-manager or preflight failure (the unit cleanup already ran) |
| 12 | Some cluster units are running and need your action |
| 13 | WebKVM's part is done; manual steps (for example the AUR packages) remain |

### 3.3 With the `webkvm-ha-stack` script

```bash
sudo webkvm-ha-stack --check       # installed by the WebKVM installer; read-only
sudo webkvm-ha-stack --install --yes
sudo webkvm-ha-stack --doctor      # services, DRBD, quorum, ports, watchdog, kernel errors
webkvm-ha-stack --json --check
```

From a source checkout or the release tarball use
`scripts/install-ha-stack.sh` with the same flags. Extra options:
`--accept-experimental`, `--with-incus` and `--with-drbd-dkms` (apt only),
`--load-drbd`. Incus and DKMS are no longer installed by default.

### 3.4 What the installer does and never does

The install is fail-closed and does not mask systemd units:

1. Snapshot: it records whether `corosync`, `pacemaker`, `pcsd` and `sbd`
   (and the LINSTOR units with `--storage`) are enabled/active.
2. Install: it installs the packages; a failure is an error, never ignored.
3. Restore: on every exit path it disables and stops ONLY the units that were
   definitely disabled and inactive before. Units that were enabled or
   active, or whose state could not be read, are never touched.
4. Verify: it re-reads the unit states.

If a unit that was not running before is still running, the result lists it
in `units_active` with the exact command to stop it. Units whose previous state
could not be read are listed in `unknown_units` and left alone: check them with
`systemctl status` yourself.

It never forms a cluster, formats disks, writes SBD/fencing configuration or
starts Pacemaker. Status: the installer logic is exercised by tests with a
mocked command runner; it was **not run for real on a live node** in the
first validation; later run live in LXC containers on Ubuntu 24.04, Fedora 44,
Rocky 10 and Alma 9 (see the matrix above).

## 4. Create the cluster from the panel

Status: the wizard is **not tested live**. Its plan checks and the host/package
probes are unit-tested; the `pcs` commands of the setup were not run end to
end on a real cluster. Review the plan before confirming.

### 4.1 The 4-step wizard

1. **Nodes and network**: every member with the IP the other nodes use. The
   local node and registered nodes are prefilled. Three nodes are recommended;
   two are only allowed explicitly in Advanced mode.
2. **Fencing and SBD**: watchdog type and SBD timeout (5 to 120 s).
3. **Review the plan**: blockers, warnings and the exact commands.
4. **Create the cluster**: confirm by ticking the checkbox and typing the
   cluster name.

Commands the wizard runs (shown in step 3, as `sudo` lines):

```bash
sudo pcs cluster setup CLUSTER node1 addr=IP1 node2 addr=IP2 node3 addr=IP3
sudo pcs stonith sbd enable watchdog=/dev/watchdog SBD_WATCHDOG_TIMEOUT=5
sudo pcs cluster start --all
sudo pcs cluster enable --all            # only if "Start the cluster services at boot"
sudo pcs property set stonith-enabled=true stonith-watchdog-timeout=10s no-quorum-policy=stop
```

The values of the watchdog device and timeout come from your choices (the
timeout above is an example). The SBD file is edited by you on each node
(`/etc/default/sbd` or `/etc/sysconfig/sbd`: `SBD_WATCHDOG_DEV`,
`SBD_WATCHDOG_TIMEOUT`, `SBD_PACEMAKER=yes`).

### 4.2 Why `pcs host auth` and the hacluster password stay manual

A password on a command line shows up in the process list. The wizard never
asks for it: it shows you the steps and checks `pcsd`'s known-hosts file
afterwards (blocker `host_auth_missing` until done). On every node:

```bash
sudo passwd hacluster                         # same strong password everywhere
sudo systemctl enable --now pcsd
```

Then on one node (pcs asks for the password interactively):

```bash
sudo pcs host auth node1 node2 node3 -u hacluster
```

### 4.3 Watchdog types

| Type | Meaning |
|---|---|
| hardware | Best: a hardware timer resets the machine even if the kernel is frozen |
| `i6300esb` | Good inside a VM: the hypervisor emulates the watchdog and resets the VM |
| `softdog` | Last resort, tests only: it lives in the kernel and does not protect against a hung kernel (the wizard shows a warning) |
| none | SBD cannot be set up: load a module or add a virtual watchdog |

Proxmox VM: Hardware, Add, Watchdog, model `i6300esb`, action `reset`, then
restart the VM.

### 4.4 Safety

- A cluster that already exists on the node is never touched: the wizard
  stops with "A cluster already exists on this node". Remove it by hand first
  if you really want to start over.
- Nothing is changed until the last step; an interrupted setup lists the
  commands still to run.
- After creation, run "Check HA readiness", then the fencing test (section 5).

## 5. Fencing

Fencing here is **SBD in watchdog-only mode** (`stonith-watchdog-timeout`, no
stonith devices): a node that loses the cluster resets itself through its
watchdog. Automatic failover and HA per workload are refused until fencing is
proven.

### 5.1 Measured result

Crashing a node (kernel panic by sysrq, see below) in the test environment: the loss was
detected and fencing completed in about 20 s (equal to
`stonith-watchdog-timeout=20s`), the VM was reset by its `i6300esb` watchdog
and came back on its own, and the cluster kept quorum (2/3) the whole time.
Pacemaker then recorded a `fence_event ... status="success"` in
`pcs status xml`, which is what the product reads as "fencing proven". A second
run measured ~18 s. Both: **measured** in the test environment.

### 5.2 Test it honestly: simulate a failure

With watchdog-only fencing, `pcs stonith fence NODE` on a **healthy** node does
not guarantee a reboot: with no fence device, Pacemaker just assumes the node
fenced itself after the timeout. It proves nothing. The reliable test is to
crash the target for real. On the node to be fenced, as root:

```bash
echo 1 > /proc/sys/kernel/sysrq
echo c > /proc/sysrq-trigger          # kernel panic: the watchdog resets the node
```

Rules: use a node that does not run the WebKVM control plane, do it in a
maintenance window, expect the node to be gone for 20 to 30 s and to return by
itself.

In the UI (Nodes page, "Test fencing"): the panel detects how the cluster
fences. With a fence device it offers the direct test ("Fence this node now",
with written confirmation). With watchdog-only it offers "Simulate a failure":
pick the node, copy the command (`sudo sh -c 'echo 1 > /proc/sys/kernel/sysrq;
echo c > /proc/sysrq-trigger'`), run it yourself on that node, press "I ran it:
wait for evidence". WebKVM crashes nothing. The page polls every 5 s for up to
10 minutes. The direct test on a healthy node is refused with
`watchdog_only_unreliable`. UI flow: **not tested live** (the crash itself was
done by hand and measured).

Read-only evidence from any node or the API:

```bash
pcs stonith history show NODE
pcs status xml | grep fence_event
curl -k -H "Authorization: Bearer $TOKEN" https://VIP:8080/api/ha/fencing/evidence
```

`GET /api/ha/fencing/evidence` returns the mode (`watchdog_only`, `device` or
`none`), whether it is proven, the events and the crash command template.

**Evidence persists across cluster restarts.** Pacemaker keeps `fence_history`
in memory only: after a full cluster restart (found in the test environment, where the
two deliberate real fencing tests of the previous day vanished from
`pcs stonith history` and only the cold-start fence remained) fencing would
silently flip back to "untested". WebKVM therefore merges every successful
`reboot`/`off` event it reads for a node of this cluster into a ledger,
`DATA_DIR/ha-fencing-events.json` (0600, 50 events max, newest first, cleared
when the cluster name changes). The ledger is also filled in the background every 60 s (first run ~10 s after start, backing off to 5 min on probe failures) while the HA module is enabled, so an event is captured even if nobody opens the readiness page. The evidence is history plus ledger; each event
carries `source` (`history` or `ledger`) and `recorded_by` may be `ledger`. The
direct fencing API never feeds the ledger in `watchdog_only` mode, and a
corrupt or non-0600 file is ignored.

### 5.3 Proxmox VM watchdog

For test nodes that are Proxmox VMs, the watchdog is a VM device:
`model=i6300esb,action=reset` (Hardware, Add, Watchdog). With `action` other
than `reset` the VM is not restarted when the watchdog fires.

### 5.4 Second layer

SBD with a VM watchdog works; hypervisor fencing (`fence_pve`) or BMC fencing
(Redfish/IPMI) is recommended on top. The installer only installs the agents;
configuration is a later step. A single-host test setup cannot protect from
losing that host.

## 6. Managed SSH access for remote KVM nodes

WebKVM opens and edits VMs on other KVM nodes over libvirt through SSH with
**one key of its own**. WebKVM never copies the key to a node: you run one
command per node. Status: unit-tested with mocks; the libvirt client of the test environment
(12.0.0) links libssh and sshd allows forwarding (read-only checks). The whole
flow in the UI was **not tested live**.

### 6.1 Set it up (Nodes page, card "Cluster access (managed SSH)")

1. **Cluster key**: "Generate cluster key" creates an ed25519 key pair under
   `DATA_DIR/cluster-ssh/`. The private key never leaves this server.
2. **Authorize**: for each remote node, copy the command and run it on that
   node as the node's SSH user. It installs this `authorized_keys` line:

   ```text
   restrict,port-forwarding,permitopen="127.0.0.1:*" ssh-ed25519 AAAA... comment
   ```

   `restrict` disables everything except what is listed; the key can only
   forward to `127.0.0.1` on that node (needed for the console tunnel).
3. **Verify the identity**: "Check host key" reads the node's SSH host key.
   Compare the SHA-256 fingerprint on the node, then pin it:

   ```bash
   ssh-keygen -lf /etc/ssh/ssh_host_ed25519_key.pub
   ```

4. **Test**: "Test connection" walks key, host key, SSH login and libvirt, and
   reports the first failure as a stable code (section 11).

Register the node first with a plain `qemu+ssh://[user@]host[:port]/system`
address (no password or query; `qemu+tcp` is rejected).

### 6.2 Key rotation (test before commit)

1. "Start rotation" creates a pending key. The current key keeps working.
2. Authorize the NEW key on every node (the card shows the command per node).
3. "Test all nodes" logs in to every enabled node with the pending key. The
   result is valid for 15 minutes.
4. "Switch to the new key" is refused (`rotation_unverified`) unless every
   enabled libvirt node accepted the new key; you can force it with an explicit
   confirmation, and then nodes that did not accept it answer `ssh_auth_denied`.
5. "Roll back to the previous key" restores the key active before the last
   switch (only one previous key is kept).

"Cancel rotation" discards the pending key.

### 6.3 Related settings and files

- `cluster.libvirt_transport`: `libssh2` (default, libvirt's built-in libssh2
  client with WebKVM's own pinned `known_hosts`, fail-closed on a mismatch),
  `libssh` (libvirt's libssh client, same checks) or `ssh` (system `ssh`
  binary). Measured live: `libssh` stalls exactly 5 s (or hangs) on every reply
  larger than ~4 KB (VM and snapshot lists, `ssh/test`); `libssh2` and `ssh`
  answer in under 5 ms. Use `libssh` or `ssh` if the test reports
  `transport_unsupported`. Known limit: libssh2 only verifies an ECDSA pinned
  host key (what WebKVM pins when the node offers one); a node with only an
  ed25519 host key fails closed as `host_key_changed`: use `libssh` there.
  Applies to new connections. Calls to a node slower than 3 s are logged as
  `libvirt_remote_slow`.
- `DATA_DIR/cluster-ssh/` (private key, pinned host keys) is **excluded from
  config backups** (covered by a test). After restoring a backup on a new
  server you must generate and authorize a key again.
- The managed key opens every node: keep the dedicated SSH user minimal and
  rotate the key if you suspect a leak.

### 6.4 Per-node `ui_url` and why private addresses fail from your PC

The node address WebKVM uses is often on the private cluster network (for
example `198.51.100.x`), which your PC cannot reach. For each node set "Address
on your network (URL)" (`ui_url`), the address where YOU reach that node, for
example `https://192.0.2.10:8080`. Only `scheme://host:port` is accepted (no
path, credentials or query). It is used for links and the "Open WebKVM" button,
and it is returned with `remote_unsupported` errors so the UI can offer to open
the node. Open the panel itself through the LAN VIP, not the private network.

## 7. Open and edit VMs and Incus from any node

Remote access is **administrators only**. Use `?node=` (and `hv=kvm|incus` when
the Incus member name equals a KVM hostname). Validation status (v0.1.12 lab,
three Ubuntu VMs under Proxmox): remote editing, the noVNC console and managed
SSH key rotation/test/rollback were exercised **live** through a real browser;
SPICE and the remote serial console remain **not tested live**.

What works remotely, by route class (79 routes: 37 A, 16 B, 26 L):

| Class | Meaning | Examples |
|---|---|---|
| A | Served unchanged | VM details, power actions, disks list, `/meta`, `/boot`, `/autostart`, `/snapshots`, guest agent, serial |
| B | Served with an adaptation | Migrate (orchestrated by the client), attach disk, networks, `/cover`, `/cloudinit`, `/logs`, console (`/vnc*`, `/spice*`, `/graphics`, `/console-ticket`), `GET /api/vms` (federated, with `node_id` and `stale`) |
| L | Local only: answers `501 remote_unsupported` | clone, batch clone, move storage, export, backup, firewall, alerts, schedule, `/metrics*`, `/disks/{dev}/probe`, `/vlan-support` |

For class L open that node in its own WebKVM (`ui_url`).

Remote console: VNC and serial go through an SSH `direct-tcpip` tunnel to
`127.0.0.1:<port>` on that node, with the pinned host key; tickets are bound to
(node, VM). In-browser SPICE is not available remotely: use noVNC or download
the `.vv` file (SPICE remote: not tested live).

Errors you can get (body has `code`, `node`, `node_id`; never raw ssh output):

| Code | HTTP | Meaning | Fix |
|---|---|---|---|
| `node_unreachable` | 502 | Cannot reach the node's SSH port | Check power, host, port, firewall |
| `node_timeout` | 504 | The node did not answer in time | Check network path and load; retry |
| `ssh_auth_denied` | 502 | The node refused the cluster key | Run the authorize command on the node, test again |
| `host_key_unpinned` | 409 | Host key not confirmed yet | "Check host key", compare, pin |
| `host_key_changed` | 409 | Host key differs from the pinned one | Reinstall? re-check and replace; otherwise treat as an incident |
| `node_disabled` | 404 | The node is disabled | Enable it in Nodes |
| `transport_unsupported` | 501 | libvirt client lacks the configured transport | Set `cluster.libvirt_transport` to `libssh`, `libssh2` or `ssh` |
| `libvirt_unavailable` | 502 | SSH works, libvirt does not answer | Start `libvirtd`/`virtqemud`; install `virt-ssh-helper` or `nc` |
| `no_key` | 409 | No cluster key generated | Generate it |
| `remote_unsupported` | 501 | Local-only action | Open the node's own WebKVM (`ui_url`) |
| `console_busy` | 429 | Too many consoles open on that node | Close one, retry |
| `console_forward_denied` | 502 | The node refused the tunnel | Check the `authorized_keys` options on the node |
| `vm_not_running` | 409 | No graphics port | Start the VM |
| `ticket_scope` | 403 | Console ticket is for another node or VM | Close and reopen the console |

Non-admins get 403 on any remote `?node=`. Incus members of the local cluster
are reached through the local socket; moving instances between members
(`POST /api/vms/{id}/move`) answers `member_offline`, `needs_stop`,
`same_member` or `not_clustered` (409) as appropriate.

## 8. HA per workload (KVM and Incus)

Pacemaker is the only brain: the CIB is the source of truth. Anything not
listed in "HA workloads" is never touched. All endpoints are admin-only and
need the module on. Validation status: the per-workload HA flow was validated
**live** on the v0.1.12 lab cluster (three Ubuntu VMs, Pacemaker with quorum)
via the product API — readiness, fencing evidence, workload adoption, enable,
move, failcount recovery, maintenance mode and guards passed 10/10
. Still **not tested live**: the cluster
creation wizard end-to-end, `pcs stonith sbd enable`, "Test fencing" from the
UI, enabling HA on a real **KVM VM through the UI** (the live pass used the
API with Incus workloads), bulk operations, snapshots with disks and node
migration.

### 8.1 Enable and disable in the UI

1. Turn on the switch "High availability" on the VM or container (or use
   the "HA workloads" panel).
2. The panel checks eligibility. If blocked, "Why?" lists each reason with
   "How to fix" (and copyable commands for Incus networks).
3. Easy mode enables with defaults. Advanced: "Placement options" (allowed
   nodes, preferred node, stickiness, migration threshold) and "Preview pcs
   commands" (dry run).
4. Confirm "Turn on HA".

Cluster-level requirements (blockers, HTTP 409 `precondition_failed`):
`fencing_test_required`, `sbd_not_active`, `quorum_missing`,
`not_enough_nodes`, `incus_healing_enabled`, `incus_member_not_online`,
`control_plane_unhealthy`, `drbd_not_uptodate`, `not_eligible`.

Eligibility reasons and fixes:

| Reason | Fix |
|---|---|
| `disk_not_on_replicated_storage` | Move disks to a replicated/shared pool (DRBD, LINSTOR, Ceph) |
| `xml_not_on_replicated_storage` | Keep the VM definition on the cluster's replicated file system |
| `drbd_not_uptodate` | Wait for the resync or fix the degraded node |
| `pool_not_shared` | Use a pool every node sees (Incus: driver `linstor`, `ceph` or `cephfs`) |
| `local_devices` | Detach USB/PCI/local-disk devices |
| `boot_autostart_enabled` | Turn off autostart (for Incus the enable path sets `boot.autostart=false` itself) |
| `vm_running` | Shut the KVM VM down first (v1); Pacemaker starts it |
| `bridge_missing_on_nodes` | Create the same bridge on the missing nodes |
| `nic_network_not_on_all_members` | Use a network that exists on every Incus member (8.3) |
| `name_collision` | Rename one of the workloads |
| `incus_member_not_online` | Bring the Incus member online |

KVM HA is for VMs on this node's DRBD volume; enabling it for a VM of a remote
node is refused. Other actions per workload: "Move", "Clear failures",
"Maintenance", "Disable HA".

Disable modes: **Pause HA** (default and safest: keep the resources, Pacemaker
stops acting, the VM stays defined and visible), **Stop it** (Pacemaker stops
it and removes the resources), **Forget it** (remove the resources of a
workload that is already stopped; refused with `workload_active` while any part
is running, or `workload_paused` if it is paused: resume then stop, or stop
directly; type the key to confirm; not available in bulk). Bulk enable/disable:
up to 50 workloads, each checked on its own.

#### Disabling HA on a KVM VM (Stop / Forget): what to expect

For KVM, Stop and Forget release the VM's shared storage. The VM stops, its
DRBD resource goes down on every node, the filesystem is unmounted and the
`/etc/libvirt/qemu/<name>.xml` link into the shared filesystem disappears
(Pacemaker's VirtualDomain ran the VM as a transient domain), so libvirt no
longer has the VM. If you only want to suspend automatic restarts, use
**Pause HA** instead.

**Before touching the cluster**, WebKVM saves a "released" inventory entry in
`DATA_DIR/ha-released-vms.json` (mode 0600, atomic write, never deleted
automatically): name, UUID, domain XML, DRBD resource, device, mount point,
filesystem type, last node and placement. The XML comes from, in order: the
definition on the replicated filesystem (only when the VM runs on the node
serving the panel), this node's libvirt, the libvirt of the member running it
(registered node), or a previous capture. If no valid XML with the same name
can be captured (for example the workload is paused, so nothing is running and
the filesystem is not mounted), the disable is **refused** with
`409 release_capture_failed` and nothing changes; resume the workload and try
again, or pass `accept_unrestorable=true` (UI: an explicit checkbox) to release
it without a restorable entry. If the disable then fails without changing
anything, the entry written by that request is reverted.

The VM then appears under **Released VMs** (HA workloads panel and VM list,
admins only) with the badge "Released · storage freed" and two actions:

- **Restore** (type the VM name; optional "start after restoring"). On the
  node serving the panel, and only after checking that Pacemaker no longer
  manages the VM nor its DRBD resource. Steps:
  1. **Precondition — bring the other replicas up.** After Stop/Forget the
     DRBD resource agent leaves the resource `down` on **every** node; with
     `quorum=majority` a promote on one node alone is refused. On each other
     node holding a replica run `drbdadm up <res>` (the entry lists them as
     `drbd_peers`, noted read-only when HA was turned off). WebKVM cannot do
     this itself: it has no command channel to other nodes.
  2. `drbdadm up` here (if down) and wait for the peers. If DRBD reports
     `quorum:no` (or, without that field, fewer than a majority of replicas
     connected) the restore stops **before promoting** with
     `409 drbd_peers_down`, listing each peer, its connection state and the
     exact command (`drbdadm up <res>`); the dialog shows them with a copy
     button and a **Re-check** button that retries. Nothing is left behind (a
     local `up` done by that call is undone; a resource that was already up
     stays up).
  3. Refuse if another node is Primary (`drbd_primary_elsewhere`) or the local
     disk is not UpToDate; `drbdadm primary`.
  4. `mkdir -p` + `mount` of the recorded device **in the host mount
     namespace** (`systemd-run --pipe --wait --collect`, like host updates:
     `webkvm.service` has its own mount namespace because of
     `ProtectSystem=full`/`PrivateTmp`, and a mount made there is invisible to
     libvirt and Pacemaker and keeps DRBD open). Then `findmnt` in the host
     namespace must show it, or everything is rolled back
     (`mount_not_visible`).
  5. `virsh define` of the saved XML (or of the definition on the filesystem
     when it has the same name and UUID; the response says `xml_differs`).
  6. Only after a successful define: the definition is put on the replicated
     filesystem (`<config path>` or `<mnt>/<name>.xml`: written atomically,
     0600, only if no file exists there — a file that is not this VM's
     definition is never overwritten, `xml_link_failed`) and
     `/etc/libvirt/qemu/<name>.xml` becomes a symlink to it (`ln -sfnT`, host
     namespace), which is what HA enable (`xml_not_on_replicated_storage`) and
     VirtualDomain `config=` expect. The response says `xml_linked`.
  7. Optional `virsh start`.

  Any failure rolls back what this call did, in reverse order (`undefine
  --keep-nvram`, remove the definition file it wrote, `umount` in the host
  namespace, `drbdadm secondary`, `drbdadm down`); nothing ever deletes data.
  A VM already defined with the saved UUID is a no-op (`changed:false`) only
  if the mount is visible to the host, DRBD is Primary here and the
  definition is linked; otherwise the missing pieces are repaired (mount,
  promote, link — the existing domain is never undefined). Another domain
  with the same name is refused (`domain_conflict`). The entry is kept and
  marked "Restored".
- **Discard entry** (type the name): removes only the inventory entry.

To protect it again afterwards, turn HA on from the VM page (the enable
eligibility checks still apply). Restore on another node is not offered:
open the panel served by that node, or restore by hand.

Restore by hand (fallback, e.g. no saved XML). On one node, with `<res>` the
DRBD resource and `<mnt>` the directory the VM's files lived in:

1. `drbdadm up <res>` (on the nodes that should hold a replica).
2. `drbdadm primary <res>` on the node that will run the VM.
3. Mount the filesystem again at `<mnt>` (same mount point as before).
4. `virsh define <mnt>/<name>.xml` (or re-create the
   `/etc/libvirt/qemu/<name>.xml` link) and `virsh start <name>`.

Restore by hand runs every command in a root shell of the host (not through
`nsenter` into the WebKVM service). If a VM was restored with v13 (mount only
inside the service namespace), unmount it there first:
`nsenter -t $(systemctl show -p MainPID --value webkvm) -m umount <mnt>`.

**Not tested live**: the capture and the Restore button were tested only with
fakes (internal lab report); the 8.22 fixes (host mount namespace,
`drbd_peers_down`, definition link) are also untested live.

A workload under HA owns its power state in Pacemaker: use Move, not Migrate;
the API answers `409 pacemaker_managed` to the normal power buttons.

### 8.2 Safe edits: maintenance flow

1. "Start maintenance" (window 15 min, 1 h or 4 h): Pacemaker stops reacting to
   the workload.
2. Edit it. Incus: from its page. KVM: shut it down first, edit, never redefine
   it by hand.
3. "End maintenance": Pacemaker takes it back and verifies it starts. If it
   does not, the configuration you edited may be wrong.

If the window expires nothing is re-enabled automatically: you only get the
warning `workload_unmanaged_expired`. Status: **simulated only**.

### 8.3 Incus requirements

- Root disk on a `linstor` or `ceph` pool (`cephfs` also accepted).
- The network of every NIC (profile or instance) must exist on EVERY member.
  Real finding: the `default` profile used `vmbr0`, which existed on one member
  only, so a moved container failed with `Parent device "vmbr0" doesn't exist`.
- `boot.autostart=false` (enabling HA sets it).
- `cluster.healing_threshold=0`: Pacemaker is the only brain. The API refuses to
  turn native healing on (`409 healing_managed_by_pacemaker`) and only accepts 0.
  Real finding: a leftover threshold of 60 s evacuated members by itself.

Recommended portable profile used in the test environment (macvlan on `eth0`, which exists on
all three members and gives a LAN address; adapt the parent NIC to your
hardware). The test environment's `default` profile was left untouched:

```bash
incus profile create ha-lan
incus profile device add ha-lan eth0 nic nictype=macvlan parent=eth0 name=eth0
incus profile device add ha-lan root disk path=/ pool=ha-shared
incus launch IMAGE NAME -p ha-lan          # a container with it started on all 3 members
```

The UI fix for the same reason offers a cluster-wide managed network
(`incus network create ha-net --target MEMBER` on each member, then
`incus network create ha-net`); that route was not run live.

### 8.4 Resource naming and agent

KVM: clone `ha-kvm-<name>-data-clone`, group `ha-kvm-<name>-grp`
(`-fs` + VM). Incus: one primitive `ha-incus-<project>-<name>` of
`ocf:webkvm:incus` (see section 12). Resources carry the meta attributes
`webkvm-kind`, `webkvm-name`, `webkvm-project`; ids are never parsed. Own
constraints are `webkvm-loc-*` / `webkvm-anti-*` / `webkvm-col-*`; only those
are ever deleted.

### 8.5 API summary (`/api/ha`, admin, module on)

| Method and path | Purpose |
|---|---|
| `GET /workloads`, `GET /workloads/{kind}/{name}` | List and detail (`kind` = `kvm` or `incus`) |
| `POST /workloads/{kind}/{name}/enable` | Enable (`dry_run`, `allowed_nodes`, `preferred_node`, ...) |
| `DELETE /workloads/{kind}/{name}?mode=stop\|pause\|forget` | Disable |
| `PUT .../placement`, `POST .../move`, `POST .../clear-failcount` | Placement, move, reset failures |
| `POST .../maintenance` `{action: begin\|end, ttl_s}` | Maintenance window |
| `POST /workloads/bulk` | Bulk enable/disable |
| `DELETE /workloads/kvm/{name}?mode=stop&accept_unrestorable=true` | Release without a restorable entry (explicit) |
| `GET /released` | Released KVM VMs (no XML in the response) |
| `POST /released/{name}/restore` `{confirm, start, node}` | Restore on the node serving the panel |
| `DELETE /released/{name}?confirm={name}` | Discard the inventory entry only |
| `GET /readiness`, `GET /fencing/evidence`, `POST /fencing/test` | Readiness and fencing |
| `POST /nodes/{name}/standby`, `.../unstandby` | Node maintenance with quorum guards |

Workload keys: `kvm:<name>`, `incus:<project>/<name>`.

## 9. Validation results

Results from a three-node test environment. They show behavior, not production capacity.

| Test | Result |
|---|---|
| Standby of the node running the control plane | Control plane back on another node in ~4 s; VIPs answered, DRBD UpToDate |
| Node crash (sysrq) without the control plane | Fenced in ~18 to 20 s, reset by the watchdog, quorum 2/3 all along, no failcounts afterwards |
| KVM HA VM (existing `kvm-ha` group, not created through the new UI) | Recovered after the fence |
| Incus container with `ocf:webkvm:incus` after a node crash | Fenced in ~18 s, started at ~31 s, `incus move` with the origin member OFFLINE, RUNNING at **~59 s** |
| Local failure of an Incus HA container (`stop --force`) | Recovered in ~15 s, failcount 1 |
| `pcs resource move` of an Incus resource | 10 s and 12 s |
| Incus move between ONLINE members, as a Pacemaker resource | ~5 s |
| `ocf-tester` on a throw-away container (pool LINSTOR) | PASS |

The Incus tests used a disposable instance as a plain Pacemaker resource
created by hand, not via the "High availability" switch.

## 10. Known limits and what is not validated live

Validation is cited with its environment: everything below was validated on
**three Ubuntu VMs under Proxmox** unless marked otherwise; a VM lab cannot
honestly validate real-hardware watchdog fencing.

Validated live in that lab (v0.1.12): standby failover, two real fence events
(kernel panic), the Incus OCF agent, per-workload HA via the product API
(10/10), managed SSH key rotation/test/rollback, remote VM editing and the
noVNC console through a real browser.

Not tested live:

- Debian 13, Rocky 8, RHEL with a subscription, an official-repo pacman install
  on Arch (no package of the stack is official there), real hardware watchdogs.
- The cluster wizard end to end and `pcs stonith sbd enable`.
- The remote serial console and SPICE (noVNC and remote editing are validated).
- Enabling HA on a real KVM VM **through the UI** (the live pass used the API
  with Incus workloads), `cib-push --config`, bulk operations.
- The UI "Test fencing" flows; snapshots with disks; migrating between nodes.
- Full LINSTOR database restore (see section 12).

Limits:

- A single-host test setup does not protect from losing that host or its root disk.
- No live migration of Incus instances: a move is stop + start.
- Creating a new 3-replica LINSTOR volume needs all three members online.
- Class L routes only work from the node's own WebKVM. Per-node local state:
  VM power/snapshot schedules and cover files are node-scoped (composite
  keys) since the collision fix; alert/metric history is still keyed by VM
  name and can conflate same-named VMs of different nodes in fleet views
  (read-side only, no action is executed on the wrong node).
- Alert delivery is not configured: `webkvm-ha-health-check` only writes to the
  journal (add `OnFailure=` to its unit).

## 11. Troubleshooting

| Symptom | Cause | Fix |
|---|---|---|
| UI blank or unreachable from your PC | You used a private cluster address | Use the LAN VIP or the node's `ui_url`; set `ui_url` per node |
| `ssh_auth_denied` right after authorizing | Command run as the wrong user or on the wrong node; `~/.ssh` or `authorized_keys` permissions; the pool cached an earlier failure | Run the command as the node user in the registered `user@host`; check `chmod 700 ~/.ssh; chmod 600 ~/.ssh/authorized_keys`; press "Test connection" again (an explicit test bypasses the failure cache) |
| `host_key_unpinned` | Host key never confirmed | "Check host key", compare with `ssh-keygen -lf`, pin |
| `host_key_changed` | Node reinstalled or SSH keys regenerated, or an impostor | If reinstalled: check again and "Replace pinned key" after verifying on the node. If not: treat as a security incident |
| `transport_unsupported` | libvirt client without the configured transport | Set `cluster.libvirt_transport` to `libssh`, `libssh2` or `ssh` |
| `libvirt_unavailable` | `libvirtd`/`virtqemud` down, no `virt-ssh-helper`/`nc` | Start the daemon, install the helper |
| `incus move` fails or HA move fails | Instance `RUNNING` on another online member (the agent never moves it), local root pool, member offline, or `Parent device ... doesn't exist` | Stop the instance first; use a `linstor`/`ceph` pool; bring the member online; fix the NIC network (below) |
| `Parent device "vmbr0" doesn't exist` | A profile NIC uses a bridge present on one member only | Use the `ha-lan` profile (8.3) or a cluster-wide network; `incus config device ...`/`incus profile ...` then start again |
| `nic_network_not_on_all_members` on enable | Same cause, caught before enabling | Apply the commands shown under "How to fix" |
| Incus member shows `Evacuated` | Native healing (or a manual evacuation) moved its instances | `incus cluster restore MEMBER`; make sure `cluster.healing_threshold` is `0` |
| `409 precondition_failed` on enable | Cluster requirements missing | Read `blockers`: usually run the fencing test (section 5) |
| `409 incus_healing_enabled` | Native healing is on | `incus config set cluster.healing_threshold=0` |
| `watchdog_only_unreliable` | Direct fence of a healthy node with watchdog-only SBD | Use "Simulate a failure" (5.2) |
| `host_auth_missing` in the wizard | `pcs host auth` not done | Set the `hacluster` password, then `pcs host auth` (4.2); "Re-check plan" |
| `cluster_exists` | A cluster is already configured here | The wizard will not touch it |
| Installer exit 12 / `units_active` | A cluster unit is running that was not before | Stop and disable it with the exact command shown |
| Installer exit 10 | Unsupported distro, experimental not accepted, HA repo or pacman databases missing | Follow the shown manual step, then `webkvm-cli ha deps check` |
| `409 pacemaker_managed` | The workload belongs to Pacemaker | Use Move/Pause/Disable HA or `pcs` |
| `rotation_unverified` | New SSH key not accepted by every node | Authorize it where it fails and "Test all nodes" |

## 12. Reference topology and day-2 operations

Example 3-node cluster architecture. In production, distribute nodes across separate physical hypervisors, failure domains, and power sources.

### 12.1 Topology

| Piece | Where |
|---|---|
| Incus cluster | `node-a` (192.0.2.10), `node-b` (192.0.2.12), `node-c` (192.0.2.11) |
| Private network | `198.51.100.10/.11/.12` on `eth1` |
| LINSTOR controller | VIP `198.51.100.30:3370`, promoted by `drbd-reactor` |
| WebKVM control plane | Private VIP `198.51.100.31:8080`; LAN VIP `192.0.2.13:8080` (`webkvm-vip-lan`) for your PC |
| WebKVM data | DRBD `webkvm_data` (1 GiB, 3 replicas) mounted at `/opt/webkvm` |
| HA VM disk | DRBD `kvm_data` (3 GiB, 3 replicas) mounted at `/var/lib/libvirt/images-ha` |
| Pacemaker groups | `webkvm-cp` (`webkvm-fs`, `webkvm-vip`, `webkvm-app`), `kvm-ha` (`kvm-fs`, `kvm-vm`) |

`node-a` can be excluded from groups with a location constraint if its local
storage or standalone non-HA workloads live under `/opt/webkvm` (e.g. standalone
`example-app`). The control plane is identified by the Pacemaker group named in
`ha.cp_group` (default `webkvm-cp`).

### 12.2 Daily checks

```bash
pcs status                                   # quorum, resources, fail counts
pcs resource failcount show
drbdsetup status                             # disk and peer state
curl -kfsS https://198.51.100.31:8080/api/health
curl -fsS http://198.51.100.30:3370/v1/nodes   # controller + Incus members
systemctl list-timers webkvm-ha-health-check.timer
```

The Nodes page shows the same state: `HA is active`, Pacemaker resources,
Incus fleet (3/3) and any cluster warnings.

### 12.3 Maintenance

Take one node out without fencing it (or use the "Standby"/"Resume" buttons,
which refuse a change that would lose quorum):

```bash
pcs node standby node-b        # resources move to node-c
# work on the node, then
pcs node unstandby node-b
```

Pause only the WebKVM service:

```bash
pcs resource disable webkvm-app
pcs resource enable webkvm-app
```

Reboot a member: put it in standby, reboot, wait for Incus/Corosync, then
unstandby. Never `systemctl enable webkvm`: Pacemaker starts the unit only on
the node that owns `webkvm-cp`.

A VM under Pacemaker (for example `ha-test`) belongs to Pacemaker. Use `pcs` or
the HA controls, not the power buttons; the API returns `409 pacemaker_managed`
on purpose.

### 12.4 Failover test (safe, no node kill)

```bash
t0=$(date +%s)
pcs node standby node-b
until pcs status | grep -q "webkvm-app.*Started node-c"; do sleep 2; done
echo "moved in $(( $(date +%s) - t0 ))s"
pcs node unstandby node-b
```

Validated in a test environment: failover completed in ~8 s for both groups,
including the cold stop/start of `ha-test`; the control plane moved in ~4 s.
Verify afterwards:

```bash
curl -kfsS https://198.51.100.31:8080/api/health
virsh domstate ha-test
```

### 12.5 Incus native healing

Native Incus healing (`cluster.healing_threshold`) must stay **off (0)**: it
evacuates members on its own and fights Pacemaker. The API only accepts 0. If a
member stays in state `Evacuated`:

```bash
incus config get cluster.healing_threshold    # must print 0 (or empty)
incus config set cluster.healing_threshold=0
incus cluster restore node-c
```

### 12.6 The `ocf:webkvm:incus` agent

The OCF agent `ocf:webkvm:incus` (`/usr/lib/ocf/resource.d/webkvm/incus`,
shipped by the package) runs one Incus instance as a Pacemaker resource.
`start` brings the instance up on the local member, cold-moving it first
(`incus move --target`) when it is stopped elsewhere or its member is offline;
`stop` shuts it down gracefully and forces only after `stop_timeout`; `monitor`
reports it running only when it is `RUNNING` on the local member. There is no
live migration: a move is stop + start.

Prerequisites: remote pool (`validate-all` rejects local drivers),
`boot.autostart=false`, `cluster.healing_threshold=0`, working fencing (the
agent relies on Pacemaker having fenced the old node before `start` runs
elsewhere).

Manual install on a node (packages already do this):

```bash
sudo install -m0755 incus /usr/lib/ocf/resource.d/webkvm/incus
```

Test it **only on a throw-away test instance** (ocf-tester starts and stops it
repeatedly):

```bash
ocf-tester -n x -o instance=NAME -o project=default /usr/lib/ocf/resource.d/webkvm/incus
```

Parameters: `instance` (required), `project` (`default`), `stop_timeout` (60 s),
`move_wait` (90 s, how long `start` retries the move while Incus still sees the
source member online).

Failure model: the agent never moves a running instance: if it is `RUNNING` on
another member that is online (or whose state cannot be read), `start` fails
with rc 1 and nothing is touched. It only moves `STOPPED` or `ERROR` instances
(`ERROR` is what a member lost after fencing shows). If the Incus daemon is
unreachable, `monitor` and `stop` fail and Pacemaker fences the node. The
offline harness is `sh packaging/ocf/test/run.sh` (66 scenarios, simulated).

### 12.7 Boot after a fence

All members carry a `systemd-networkd-wait-online` drop-in that waits only for
`eth1` with a 20 s timeout. Without it, a fenced member could hang at boot on a
carrier-less `vmbr0` and never start Pacemaker. Do not remove
`/etc/systemd/system/systemd-networkd-wait-online.service.d/10-webkvm-ha.conf`.

### 12.8 Backups

Off-site, encrypted, daily backup recommendations:

- Script: `/usr/local/sbin/webkvm-ha-state-backup`
- Destination: `/mnt/backup-share/webkvm-backups/`
- Contents: `/opt/webkvm` (users, config, API tokens, TLS keys), Pacemaker CIB,
  DRBD/LINSTOR config, LINSTOR database tar, versions and status.
- Encryption: AES-256-CTR, key in `/etc/webkvm-ha-backup.key` on the host.
  **Losing that key makes every backup unreadable.**
- Retention: 14 days. Timer: `webkvm-ha-state-backup.timer`.
- Integrity: SHA-256 written next to each file; verify and test decrypting
  every archive before finishing.
- Application config backups exclude `cluster-ssh/` (the managed SSH key).

Restore rehearsal (validated in a test environment): copy the archive to a standby
member, decrypt, extract into `/tmp`, point `DATA_DIR` at the copy, start a
second `webkvm` on `127.0.0.1:18080` and query it with `webkvm-cli`. The
restored instance answered health 200, listed `example-app` and returned the
`admin` user. Validate restoration in your environment before trusting backups.

A full LINSTOR database restore requires stopping the controller and the DRBD
resource, so treat it as a documented disaster recovery procedure.

### 12.9 Disaster recovery

1. If `webkvm_data` is corrupted: `pcs resource disable webkvm-cp`, re-create
   the ext4 filesystem only after copying the old volume aside, restore
   `/opt/webkvm` from the archive, then `pcs resource enable webkvm-cp`.
2. If the LINSTOR controller database is lost: restore the tar under
   `/var/lib/linstor` on the DRBD primary, then restart `drbd-reactor`.
3. If all physical hosts or shared storage pools are lost, recover from off-host
   backups: re-provision nodes, restore the encrypted backup from `backup-share`,
   and restart the stack.

### 12.10 Operational considerations

- Ensure physical nodes use independent power, disks, and network interfaces to avoid common failure points.
- Non-HA workloads (e.g. standalone `example-app` instances with local storage) must be backed up independently according to their application requirements.

### 12.11 Updating WebKVM in an HA cluster

The in-app **Update** button (`/#/status`) installs a binary on the node that
serves the panel and restarts its local `webkvm.service`. In an HA cluster that
unit is owned by Pacemaker: the button is **not** a cluster-wide updater and can
race the resource. Update every node with the same release.

Recommended rolling procedure (the control plane is down while the resource is
disabled, so announce a maintenance window):

1. Back up `/opt/webkvm` and the Pacemaker CIB; confirm `pcs status` shows
   quorum and no failed actions.
2. Disable the control-plane resource and wait for it to stop:
   ```bash
   sudo pcs resource disable webkvm-app --wait
   sudo pcs status
   ```
3. On **every** node, install the same release binary. `/usr/local/bin` is
   node-local; `/opt/webkvm` is the shared DRBD volume and must not receive the
   binary. From the release tarball:
   ```bash
   sudo cp -a /usr/local/bin/webkvm /usr/local/bin/webkvm.previous
   sudo install -m 0755 backend/webkvm /usr/local/bin/webkvm.new
   sudo mv /usr/local/bin/webkvm.new /usr/local/bin/webkvm
   sudo install -m 0755 packaging/standalone/update.sh /usr/local/bin/webkvm-update
   ```
   Or use the raw asset `webkvm-0.1.13-linux_amd64` with the same `install`/`mv`.
4. Re-enable the resource and verify:
   ```bash
   sudo pcs resource enable webkvm-app --wait
   sudo pcs resource restart webkvm-app
   curl -kfsS https://<VIP>:8080/api/health
   webkvm version        # 0.1.13
   ```
   Repeat `webkvm version` on each node.
5. AppArmor: existing VMs are migrated on the next WebKVM-managed start, revert
   or snapshot delete; no XML edit is needed. Pacemaker-managed VMs are started
   by Pacemaker, so update their XML only if you use WebKVM snapshot operations
   on them.
6. Rollback: disable the resource, restore `webkvm.previous` on every node,
   then enable it again.

Do not use the UI button as a cluster-wide update. Do not run
`systemctl enable webkvm` on a member: Pacemaker owns the unit.
