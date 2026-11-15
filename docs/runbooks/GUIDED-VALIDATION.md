# Guided validation checklist (UI)

For an operator validating WebKVM's Cluster/HA features from the web UI on a
live cluster. Each step gives the action, the expected result, a **read-only**
check on the node, and what to send back if it fails. Background and error
codes: [HA-CLUSTER.md](HA-CLUSTER.md). Spanish version:
[GUIDED-VALIDATION.es.md](GUIDED-VALIDATION.es.md).

Status of what you are testing: the UI flows below (managed SSH, remote
console, remote editing, cluster wizard, HA per workload, "Test fencing") were
implemented and unit-tested but **not tested live** before this checklist.
Anything that fails is useful information; do not work around it silently.

## Before you start

- Do it in a maintenance window, on a test cluster or with disposable
  workloads. Steps 8 and 9 change the cluster.
- Use a test VM (KVM) and a disposable container (Incus). Never start with
  production workloads.
- Everything under "Check on the node" only reads state. None of it changes the
  cluster.
- When something fails, copy the **exact error code** shown in the UI (for
  example `ssh_auth_denied`) and the output of the command listed.

Read-only commands used below:

```bash
pcs status
pcs status xml | grep fence_event
pcs stonith history show NODE
incus list
incus cluster list
virsh -c qemu:///system list --all
sudo webkvm-cli ha deps check
```

## Checklist

### 1. Open the panel through the LAN VIP

1. In your browser open `https://LAN-VIP:8080` (the address on YOUR network,
   not the private cluster network).
2. Expected: the login page loads (not blank, no certificate loop beyond the
   usual self-signed warning).
3. Check on the node: `pcs status` shows `webkvm-vip-lan` (or your LAN VIP
   resource) and `webkvm-app` Started on one node.
4. If it fails, send me: the exact URL you used, what the browser shows, the
   output of `pcs status`.

### 2. Log in as an administrator

1. Log in with an admin account. Remote nodes and HA actions are admin only.
2. Expected: the Nodes page opens and shows the cluster section.
3. Check on the node: `pcs status` shows quorum (`partition with quorum`).
4. If it fails, send me: the message shown, the output of `pcs status`.

### 3. Set each node's address on your network (`ui_url`)

1. Nodes page, edit each node, fill "Address on your network (URL)", for
   example `https://192.0.2.10:8080`. Only `scheme://host:port`.
2. Expected: saved; the "Open WebKVM" button of that node opens its own UI from
   your PC.
3. Check on the node: `ip -br addr` (read-only) shows that address on the node.
4. If it fails, send me: the error text under the field (for example "must not
   contain a path") and the address you typed.

### 4. Cluster access: key, authorize, pin, test

Card "Cluster access (managed SSH)". Repeat 4.2 to 4.4 for every remote KVM
node.

1. **Generate cluster key.** Expected: key created, algorithm ed25519 and a
   fingerprint shown.
   If it fails, send me: the error code (`ssh_store_error`, `ssh_unavailable`).
2. **Authorize.** Copy the command and run it ON the node as the node's SSH
   user. Expected: no output, no error.
   Check on the node: `grep restrict ~/.ssh/authorized_keys` shows the line
   `restrict,port-forwarding,permitopen="127.0.0.1:*" ssh-ed25519 ...`.
3. **Check host key, then pin.** Compare the fingerprint with the one the node
   reports, then pin it. Expected: state "Pinned".
   Check on the node: `ssh-keygen -lf /etc/ssh/ssh_host_ed25519_key.pub` (the
   SHA256 value equals the UI).
   If it differs, STOP and send me both fingerprints.
4. **Test connection.** Expected: "Connection to NODE works", SSH login OK,
   libvirt OK with its version, "KVM available" and a VM count.
   Check on the node: `virsh -c qemu:///system list --all` shows the same
   number of VMs.
5. If it fails, send me: the code and hint shown. Typical:
   `ssh_auth_denied` (key not authorized; test again after authorizing),
   `host_key_changed`, `node_unreachable`, `libvirt_unavailable`,
   `transport_unsupported` (see HA-CLUSTER.md, section 11). Also the output of
   `sudo journalctl -u sshd -n 30 --no-pager` on that node.

### 5. Open a remote VM and its console

1. In the VM list pick a VM that lives on another node (the list is federated;
   it shows the node). Open it.
2. Expected: details load; the console (noVNC) shows the screen of the VM.
   In-browser SPICE is not offered for remote VMs: use noVNC or the `.vv` file.
3. Check on the node that owns the VM: `virsh -c qemu:///system list` shows it
   running; `ss -ltn | grep 590` shows a local VNC port on `127.0.0.1`.
4. If it fails, send me: the code (`vm_not_running`, `console_forward_denied`,
   `console_busy`, `ticket_scope`, `remote_unsupported`), and on the node the
   output of `virsh -c qemu:///system dumpxml VM | grep graphics`.

### 6. Edit hardware on a remote VM

1. On a test VM of a remote node change something harmless (vCPU or RAM) and
   save. Power-cycle if the UI asks for it.
2. Expected: saved without error; the new value shows after reload.
3. Check on the node: `virsh -c qemu:///system dumpxml VM | grep -E "<vcpu|<memory"`
   shows the new values.
4. If it fails, send me: the error code and text, and the same `dumpxml`
   excerpt.

### 7. Incus: open an instance on another member

1. Open an Incus instance that runs on another member of the cluster.
2. Expected: details load from any node.
3. Check on a node: `incus list` and `incus cluster list` show the instance on
   that member and all members `ONLINE`.
4. If it fails, send me: the error code and the output of `incus cluster list`.

### 8. Enable and disable HA on a test workload

Pre-requisite: a passed fencing test (step 9). Without it, enabling is refused
with `fencing_test_required`; that refusal is itself a valid result of this
step.

1. Pick a disposable workload. Incus: its profile must use a network that
   exists on every member and `boot.autostart=false` (see HA-CLUSTER.md, 8.3).
   KVM: the VM must be shut down and live on the replicated DRBD volume.
2. Turn on "High availability" and press "Preview pcs commands" first.
   Expected: a command list, nothing changed.
   If blocked, "Why?" lists reasons: send me every reason code shown.
3. Confirm "Turn on HA". Expected: "HA enabled for NAME"; the panel "HA
   workloads" lists it as Running.
   Check on a node: `pcs status` shows `ha-incus-PROJECT-NAME` (Incus) or
   `ha-kvm-NAME-grp` (KVM) Started; `incus list` / `virsh -c qemu:///system list`
   shows the instance running on the same node.
4. Use "Move" to another node. Expected: it restarts there in about 10 to 15 s
   (Incus resource measured).
   Check: `pcs status` shows the new node; `incus list` agrees.
5. Turn HA off with "Disable HA", mode "Stop it". Expected: "HA disabled for
   NAME"; the resources disappear.
   Check: `pcs status` no longer lists the resources; `incus list` shows the
   instance stopped.
6. If it fails, send me: the error code, the `blockers` or `reasons` shown, and
   the output of `pcs status`, `incus list`, `incus config show NAME` (without
   secrets).

### 9. Fencing: simulate a failure

Use a node that does NOT run the control plane. Do it in a window.

1. "Test fencing" panel. Expected: it says how the cluster fences. With
   watchdog-only SBD it offers "Simulate a failure".
   Do NOT use a direct fence on a healthy node: it is refused
   (`watchdog_only_unreliable`) because it proves nothing.
2. Pick the node and copy the command shown. Run it yourself ON that node:

   ```bash
   sudo sh -c 'echo 1 > /proc/sys/kernel/sysrq; echo c > /proc/sysrq-trigger'
   ```

3. Press "I ran it: wait for evidence". Expected: the node disappears for about
   20 to 30 s and comes back by itself; the page ends with "Fencing proven:
   Pacemaker fenced NODE".
4. Check on another node: `pcs status` shows 2 of 3 online while it is down and
   quorum kept; `pcs stonith history show NODE` lists the event;
   `pcs status xml | grep fence_event` shows `status="success"`.
5. When the node is back: `pcs status` shows all nodes Online, no failed
   actions; `incus cluster list` shows all members `ONLINE`.
6. If it fails, send me: the output of `pcs status`, `pcs stonith history show
   NODE`, `pcs property config | grep -i stonith`, `ls /dev/watchdog*`, and
   whether the node actually rebooted.

### 10. Final state

1. Expected: `pcs status` has no failed actions, quorum is kept, and
   `sudo webkvm-cli ha deps check` reports OK on each node.
2. Check: `incus cluster list` shows all members `ONLINE` (none `Evacuated`).
3. If something is `Evacuated`, send me `incus cluster list` and
   `incus config get cluster.healing_threshold` (it must be 0).

## What to send back

For every failed step, in one message:

1. The step number and what you did.
2. The exact error code and text from the UI.
3. The output of the read-only commands listed in that step.
4. The time (so the logs can be matched) and which node you were on.

Do not paste passwords, tokens, private keys or the contents of
`DATA_DIR/cluster-ssh/`.
