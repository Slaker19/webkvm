#!/bin/sh
set -eu

fail() {
    printf '%s\n' "$1" >&2
    exit 1
}

# The whole install lives in main() and runs with stdin detached: this script
# is piped over `sh -s`, and any command that reads stdin (apt, dnf, ...)
# would otherwise swallow the rest of the script. The function is parsed in
# full before it runs, and only the terminal prompt below reads from the tty.
main() {
    [ "$(id -u)" -eq 0 ] || fail 'Run this script as root or through sudo.'

    libvirt_user=${WEBKVM_SSH_USER:-${SUDO_USER:-}}
    if [ -z "$libvirt_user" ] && [ -r /dev/tty ]; then
        printf 'SSH username WebKVM will use: ' >/dev/tty
        IFS= read -r libvirt_user </dev/tty
    fi
    [ -n "$libvirt_user" ] || fail 'Set WEBKVM_SSH_USER or run through sudo as the SSH user.'

    case "$libvirt_user" in
        ''|-*|*[!A-Za-z0-9_.-]*) fail 'Invalid SSH username.' ;;
    esac

    if command -v apt-get >/dev/null 2>&1; then
        export DEBIAN_FRONTEND=noninteractive
        apt-get update
        apt-get install -y libvirt-daemon-system libvirt-clients libvirt-daemon-driver-qemu qemu-system-x86 qemu-utils ovmf swtpm swtpm-tools dnsmasq-base
    elif command -v dnf >/dev/null 2>&1; then
        dnf install -y libvirt-daemon libvirt-daemon-driver-qemu libvirt-client qemu-kvm-core qemu-img edk2-ovmf swtpm swtpm-tools dnsmasq
    elif command -v pacman >/dev/null 2>&1; then
        [ "${WEBKVM_ALLOW_SYSTEM_UPGRADE:-0}" = 1 ] || fail 'Arch requires a full system upgrade. Confirm it in WebKVM or set WEBKVM_ALLOW_SYSTEM_UPGRADE=1.'
        pacman -Syu --needed --noconfirm libvirt qemu-full qemu-img edk2-ovmf swtpm dnsmasq
    else
        fail 'Supported package managers are apt, dnf, and pacman.'
    fi

    if [ "$libvirt_user" != root ]; then
        getent group libvirt >/dev/null 2>&1 || fail 'The libvirt group was not created by the package manager.'
        usermod -aG libvirt "$libvirt_user"
    fi

    unit=
    for candidate in libvirtd.service libvirtd.socket virtqemud.socket; do
        if systemctl --no-pager cat "$candidate" >/dev/null 2>&1; then
            unit=$candidate
            break
        fi
    done
    [ -n "$unit" ] || fail 'No libvirt system daemon unit was installed.'
    systemctl enable --now "$unit"

    for candidate in virtlogd.socket virtlockd.socket; do
        if systemctl --no-pager cat "$candidate" >/dev/null 2>&1; then
            systemctl enable --now "$candidate"
        fi
    done

    command -v virsh >/dev/null 2>&1 || fail 'virsh was not installed.'
    virsh -c qemu:///system list --all >/dev/null || fail 'The qemu:///system libvirt connection is not ready.'
    printf 'WebKVM node dependencies are installed for %s.\n' "$libvirt_user"
}

main </dev/null
