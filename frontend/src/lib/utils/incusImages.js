/**
 * Incus/LXD container image presets (v2.2.0, formerly lxdImages).
 * Instead of forcing the operator to type "ubuntu:24.04", the container
 * form offers a dropdown of friendly, descriptive distribution labels
 * backed by valid Incus/LXD image references — every ref here passes
 * parseImageRef() on the backend (known remotes: ubuntu, ubuntu-daily,
 * images, almalinux, rockylinux). "Custom / Other…" reveals the manual
 * text input for anything else.
 */

export const INCUS_IMAGE_PRESETS = [
  { label: 'Ubuntu 24.04 LTS', ref: 'ubuntu:24.04' },
  { label: 'Ubuntu 22.04 LTS', ref: 'ubuntu:22.04' },
  { label: 'Debian 12 (bookworm)', ref: 'images:debian/12' },
  { label: 'Debian 11 (bullseye)', ref: 'images:debian/11' },
  { label: 'Alpine Linux 3.20', ref: 'images:alpine/3.20' },
  { label: 'Fedora 40', ref: 'images:fedora/40' },
  { label: 'CentOS Stream 9', ref: 'images:centos-stream-9' },
  { label: 'AlmaLinux 9', ref: 'almalinux:9' },
  { label: 'Rocky Linux 9', ref: 'rockylinux:9' },
];

export const CUSTOM_IMAGE = { label: 'Custom / Other…', ref: '' };

/**
 * Friendly label for an image reference. Known presets map to their
 * distribution name; unknown/custom references fall back to the raw ref
 * (we cannot invent a label for something the operator typed).
 */
export function labelForImage(ref) {
  const hit = INCUS_IMAGE_PRESETS.find((p) => p.ref === ref);
  return hit ? hit.label : ref || '';
}
