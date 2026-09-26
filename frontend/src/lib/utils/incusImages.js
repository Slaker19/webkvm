/**
 * Incus/LXD container image presets & visual catalog.
 * Instead of forcing the operator to type "ubuntu:24.04", the container
 * form offers a rich visual catalog of distributions and friendly labels
 * backed by valid Incus/LXD image references and local cached images.
 */

export const INCUS_IMAGE_CATEGORIES = [
  { id: 'all', label: 'Todas las Distribuciones' },
  { id: 'local', label: 'Locales (En Caché)' },
  { id: 'images', label: 'Remotas (images:)' },
  { id: 'popular', label: 'Populares / Servidor' },
  { id: 'minimal', label: 'Ligeras / Microservicios' },
  { id: 'enterprise', label: 'Enterprise / RHEL' },
  { id: 'dev', label: 'Desarrollo / Rolling' },
];

export const INCUS_IMAGE_PRESETS = [
  // Populares / Servidor
  {
    label: 'Ubuntu 26.04 (Resolute)',
    ref: 'images:ubuntu/resolute',
    category: 'popular',
    distro: 'ubuntu',
    desc: 'Ubuntu 26.04 Resolute LTS (desarrollo / compilación más reciente).',
    arch: 'x86_64 / arm64',
    badge: '26.04',
    recommended_vcpu: 2,
    recommended_ram_mb: 2048,
    recommended_disk_gb: 20,
    color: 'from-orange-500/20 to-orange-600/10 border-orange-500/30 text-orange-400',
  },
  {
    // NOTE: intentionally "images:ubuntu/24.04", NOT the legacy
    // "ubuntu:24.04" (cloud-images.ubuntu.com) remote. The backend's
    // Incus client doesn't recognize that stream's image metadata
    // format, so pulling it always fails with "Alias ... doesn't
    // exist" — images.linuxcontainers.org mirrors the same release and
    // actually works.
    label: 'Ubuntu 24.04 LTS (Noble)',
    ref: 'images:ubuntu/24.04',
    category: 'popular',
    distro: 'ubuntu',
    desc: 'LTS más reciente de Ubuntu con soporte oficial hasta 2029.',
    arch: 'x86_64 / arm64',
    badge: 'LTS',
    recommended_vcpu: 2,
    recommended_ram_mb: 2048,
    recommended_disk_gb: 20,
    color: 'from-orange-500/20 to-orange-600/10 border-orange-500/30 text-orange-400',
  },
  {
    label: 'Ubuntu 22.04 LTS (Jammy)',
    ref: 'images:ubuntu/22.04',
    category: 'popular',
    distro: 'ubuntu',
    desc: 'Versión LTS clásica y ampliamente compatible.',
    arch: 'x86_64 / arm64',
    badge: 'LTS',
    recommended_vcpu: 2,
    recommended_ram_mb: 2048,
    recommended_disk_gb: 20,
    color: 'from-orange-500/20 to-orange-600/10 border-orange-500/30 text-orange-400',
  },
  {
    label: 'Debian 14 (Forky)',
    ref: 'images:debian/forky',
    category: 'dev',
    distro: 'debian',
    desc: 'Siguiente generación de Debian 14 con paquetería de última hornada.',
    arch: 'x86_64 / arm64',
    badge: 'Debian 14',
    recommended_vcpu: 2,
    recommended_ram_mb: 2048,
    recommended_disk_gb: 15,
    color: 'from-rose-500/20 to-rose-600/10 border-rose-500/30 text-rose-400',
  },
  {
    label: 'Debian 13 (Trixie)',
    ref: 'images:debian/13',
    category: 'popular',
    distro: 'debian',
    desc: 'Siguiente generación de Debian con paquetería actualizada.',
    arch: 'x86_64 / arm64',
    badge: 'Testing',
    recommended_vcpu: 2,
    recommended_ram_mb: 2048,
    recommended_disk_gb: 20,
    color: 'from-rose-500/20 to-rose-600/10 border-rose-500/30 text-rose-400',
  },
  {
    label: 'Debian 12 (Bookworm)',
    ref: 'images:debian/12',
    category: 'popular',
    distro: 'debian',
    desc: 'Debian estable, ideal para servidores de producción y bases de datos.',
    arch: 'x86_64 / arm64',
    badge: 'Estable',
    recommended_vcpu: 2,
    recommended_ram_mb: 2048,
    recommended_disk_gb: 20,
    color: 'from-rose-500/20 to-rose-600/10 border-rose-500/30 text-rose-400',
  },
  {
    label: 'Debian 11 (Bullseye)',
    ref: 'images:debian/11',
    category: 'popular',
    distro: 'debian',
    desc: 'Debian oldstable optimizada para servicios heredados.',
    arch: 'x86_64 / arm64',
    badge: 'Oldstable',
    recommended_vcpu: 1,
    recommended_ram_mb: 1024,
    recommended_disk_gb: 10,
    color: 'from-rose-500/20 to-rose-600/10 border-rose-500/30 text-rose-400',
  },
  {
    label: 'Debian Sid (Unstable)',
    ref: 'images:debian/sid',
    category: 'dev',
    distro: 'debian',
    desc: 'Rama inestable de Debian para desarrolladores y bleeding-edge.',
    arch: 'x86_64 / arm64',
    badge: 'Unstable',
    recommended_vcpu: 2,
    recommended_ram_mb: 2048,
    recommended_disk_gb: 15,
    color: 'from-rose-500/20 to-rose-600/10 border-rose-500/30 text-rose-400',
  },

  // Ligeras / Minimalistas
  {
    label: 'Alpine Linux 3.24',
    ref: 'images:alpine/3.24',
    category: 'minimal',
    distro: 'alpine',
    desc: 'Alpine Linux versión más reciente.',
    arch: 'x86_64 / arm64',
    badge: '3.24',
    recommended_vcpu: 1,
    recommended_ram_mb: 512,
    recommended_disk_gb: 5,
    color: 'from-sky-500/20 to-sky-600/10 border-sky-500/30 text-sky-400',
  },
  {
    label: 'Alpine Linux 3.23',
    ref: 'images:alpine/3.23',
    category: 'minimal',
    distro: 'alpine',
    desc: 'Alpine Linux versión reciente estable.',
    arch: 'x86_64 / arm64',
    badge: '3.23',
    recommended_vcpu: 1,
    recommended_ram_mb: 512,
    recommended_disk_gb: 5,
    color: 'from-sky-500/20 to-sky-600/10 border-sky-500/30 text-sky-400',
  },
  {
    label: 'Alpine Linux 3.22',
    ref: 'images:alpine/3.22',
    category: 'minimal',
    distro: 'alpine',
    desc: 'Alpine Linux versión estable.',
    arch: 'x86_64 / arm64',
    badge: '3.22',
    recommended_vcpu: 1,
    recommended_ram_mb: 512,
    recommended_disk_gb: 5,
    color: 'from-sky-500/20 to-sky-600/10 border-sky-500/30 text-sky-400',
  },
  {
    label: 'Alpine Linux 3.21',
    ref: 'images:alpine/3.21',
    category: 'minimal',
    distro: 'alpine',
    desc: 'Ultraligera basada en musl y BusyBox (~5MB). Máximo rendimiento y bajo consumo.',
    arch: 'x86_64 / arm64',
    badge: '5 MB',
    recommended_vcpu: 1,
    recommended_ram_mb: 512,
    recommended_disk_gb: 5,
    color: 'from-sky-500/20 to-sky-600/10 border-sky-500/30 text-sky-400',
  },
  {
    label: 'Alpine Linux 3.20',
    ref: 'images:alpine/3.20',
    category: 'minimal',
    distro: 'alpine',
    desc: 'Versión estable consolidada de Alpine Linux.',
    arch: 'x86_64 / arm64',
    badge: '5 MB',
    recommended_vcpu: 1,
    recommended_ram_mb: 512,
    recommended_disk_gb: 5,
    color: 'from-sky-500/20 to-sky-600/10 border-sky-500/30 text-sky-400',
  },
  {
    label: 'Alpine Linux Edge',
    ref: 'images:alpine/edge',
    category: 'minimal',
    distro: 'alpine',
    desc: 'Rama rolling de vanguardia para pruebas de paquetes y bleeding-edge.',
    arch: 'x86_64 / arm64',
    badge: 'Edge',
    recommended_vcpu: 1,
    recommended_ram_mb: 512,
    recommended_disk_gb: 5,
    color: 'from-sky-500/20 to-sky-600/10 border-sky-500/30 text-sky-400',
  },
  {
    label: 'Void Linux (glibc)',
    ref: 'images:voidlinux',
    category: 'minimal',
    distro: 'void',
    desc: 'Distribución independiente ligera con gestor XBPS y runit.',
    arch: 'x86_64',
    badge: 'Lightweight',
    recommended_vcpu: 1,
    recommended_ram_mb: 512,
    recommended_disk_gb: 10,
    color: 'from-emerald-500/20 to-emerald-600/10 border-emerald-500/30 text-emerald-400',
  },
  {
    label: 'Void Linux (musl)',
    ref: 'images:voidlinux/musl',
    category: 'minimal',
    distro: 'void',
    desc: 'Variante ultra compacta de Void Linux compilada contra musl libc.',
    arch: 'x86_64',
    badge: 'musl',
    recommended_vcpu: 1,
    recommended_ram_mb: 512,
    recommended_disk_gb: 10,
    color: 'from-emerald-500/20 to-emerald-600/10 border-emerald-500/30 text-emerald-400',
  },
  {
    label: 'OpenWrt 23.05',
    ref: 'images:openwrt/23.05',
    category: 'minimal',
    distro: 'openwrt',
    desc: 'Sistema operativo embebido ultraligero ideal para enrutamiento y micro-servicios.',
    arch: 'x86_64 / arm64',
    badge: 'Router',
    recommended_vcpu: 1,
    recommended_ram_mb: 256,
    recommended_disk_gb: 2,
    color: 'from-teal-500/20 to-cyan-600/10 border-teal-500/30 text-teal-400',
  },
  {
    label: 'Devuan 5 (Daedalus)',
    ref: 'images:devuan/daedalus',
    category: 'minimal',
    distro: 'devuan',
    desc: 'Bifurcación de Debian sin systemd (usa SysVinit / OpenRC / runit).',
    arch: 'x86_64 / arm64',
    badge: 'No-systemd',
    recommended_vcpu: 1,
    recommended_ram_mb: 1024,
    recommended_disk_gb: 10,
    color: 'from-purple-500/20 to-indigo-600/10 border-purple-500/30 text-purple-400',
  },

  // Enterprise / RHEL
  {
    label: 'Rocky Linux 10',
    ref: 'images:rockylinux/10',
    category: 'enterprise',
    distro: 'rocky',
    desc: 'Siguiente generación compatible con Red Hat Enterprise Linux 10.',
    arch: 'x86_64 / arm64',
    badge: 'RHEL 10',
    recommended_vcpu: 2,
    recommended_ram_mb: 2048,
    recommended_disk_gb: 20,
    color: 'from-emerald-500/20 to-teal-600/10 border-emerald-500/30 text-emerald-400',
  },
  {
    label: 'Rocky Linux 9 (images:)',
    ref: 'images:rockylinux/9',
    category: 'enterprise',
    distro: 'rocky',
    desc: '100% compatible binario con Red Hat Enterprise Linux 9 desde remote images:.',
    arch: 'x86_64 / arm64',
    badge: 'Enterprise',
    recommended_vcpu: 2,
    recommended_ram_mb: 2048,
    recommended_disk_gb: 20,
    color: 'from-emerald-500/20 to-teal-600/10 border-emerald-500/30 text-emerald-400',
  },
  {
    label: 'Rocky Linux 8',
    ref: 'images:rockylinux/8',
    category: 'enterprise',
    distro: 'rocky',
    desc: 'Versión empresarial con compatibilidad RHEL 8.',
    arch: 'x86_64 / arm64',
    badge: 'Enterprise',
    recommended_vcpu: 2,
    recommended_ram_mb: 2048,
    recommended_disk_gb: 20,
    color: 'from-emerald-500/20 to-teal-600/10 border-emerald-500/30 text-emerald-400',
  },
  {
    label: 'AlmaLinux 9 (images:)',
    ref: 'images:almalinux/9',
    category: 'enterprise',
    distro: 'almalinux',
    desc: 'Enterprise Linux comunitaria compatible 1:1 con RHEL 9.',
    arch: 'x86_64 / arm64',
    badge: 'Enterprise',
    recommended_vcpu: 2,
    recommended_ram_mb: 2048,
    recommended_disk_gb: 20,
    color: 'from-blue-500/20 to-indigo-600/10 border-blue-500/30 text-blue-400',
  },
  {
    label: 'AlmaLinux 8',
    ref: 'images:almalinux/8',
    category: 'enterprise',
    distro: 'almalinux',
    desc: 'Enterprise Linux comunitaria compatible 1:1 con RHEL 8.',
    arch: 'x86_64 / arm64',
    badge: 'Enterprise',
    recommended_vcpu: 2,
    recommended_ram_mb: 2048,
    recommended_disk_gb: 20,
    color: 'from-blue-500/20 to-indigo-600/10 border-blue-500/30 text-blue-400',
  },
  {
    label: 'CentOS Stream 10',
    ref: 'images:centos/10-Stream',
    category: 'enterprise',
    distro: 'centos',
    desc: 'Rama upstream más reciente de desarrollo para RHEL 10.',
    arch: 'x86_64 / arm64',
    badge: 'Stream 10',
    recommended_vcpu: 2,
    recommended_ram_mb: 2048,
    recommended_disk_gb: 20,
    color: 'from-purple-500/20 to-purple-600/10 border-purple-500/30 text-purple-400',
  },
  {
    label: 'CentOS Stream 9',
    ref: 'images:centos/9-Stream',
    category: 'enterprise',
    distro: 'centos',
    desc: 'Rama de desarrollo upstream de Red Hat Enterprise Linux 9.',
    arch: 'x86_64 / arm64',
    badge: 'Upstream',
    recommended_vcpu: 2,
    recommended_ram_mb: 2048,
    recommended_disk_gb: 20,
    color: 'from-purple-500/20 to-purple-600/10 border-purple-500/30 text-purple-400',
  },
  {
    label: 'Oracle Linux 10',
    ref: 'images:oracle/10',
    category: 'enterprise',
    distro: 'oracle',
    desc: 'Siguiente generación Enterprise optimizada para bases de datos.',
    arch: 'x86_64 / arm64',
    badge: 'Oracle 10',
    recommended_vcpu: 2,
    recommended_ram_mb: 2048,
    recommended_disk_gb: 20,
    color: 'from-red-500/20 to-red-600/10 border-red-500/30 text-red-400',
  },
  {
    label: 'Oracle Linux 9',
    ref: 'images:oracle/9',
    category: 'enterprise',
    distro: 'oracle',
    desc: 'Distribución empresarial optimizada para bases de datos y cargas pesadas.',
    arch: 'x86_64',
    badge: 'UEK',
    recommended_vcpu: 2,
    recommended_ram_mb: 2048,
    recommended_disk_gb: 20,
    color: 'from-red-500/20 to-red-600/10 border-red-500/30 text-red-400',
  },
  {
    label: 'Amazon Linux 2023',
    ref: 'images:amazonlinux/2023',
    category: 'enterprise',
    distro: 'amazon',
    desc: 'Distribución orientada a la nube de AWS optimizada para contenedores.',
    arch: 'x86_64 / arm64',
    badge: 'Cloud',
    recommended_vcpu: 2,
    recommended_ram_mb: 2048,
    recommended_disk_gb: 15,
    color: 'from-amber-500/20 to-orange-600/10 border-amber-500/30 text-amber-400',
  },
  {
    label: 'openSUSE 16.0',
    ref: 'images:opensuse/16.0',
    category: 'enterprise',
    distro: 'opensuse',
    desc: 'Siguiente generación de openSUSE Leap.',
    arch: 'x86_64 / arm64',
    badge: 'Leap 16',
    recommended_vcpu: 2,
    recommended_ram_mb: 2048,
    recommended_disk_gb: 20,
    color: 'from-lime-500/20 to-green-600/10 border-lime-500/30 text-lime-400',
  },
  {
    label: 'openSUSE Leap 15.6',
    ref: 'images:opensuse/15.6',
    category: 'enterprise',
    distro: 'opensuse',
    desc: 'Distribución estable orientada a servidores empresariales con YaST.',
    arch: 'x86_64 / arm64',
    badge: 'Leap',
    recommended_vcpu: 2,
    recommended_ram_mb: 2048,
    recommended_disk_gb: 20,
    color: 'from-lime-500/20 to-green-600/10 border-lime-500/30 text-lime-400',
  },

  // Desarrollo / Rolling
  {
    label: 'Arch Linux',
    ref: 'images:archlinux',
    category: 'dev',
    distro: 'arch',
    desc: 'Rolling release con los paquetes más recientes del kernel y librerías.',
    arch: 'x86_64',
    badge: 'Rolling',
    recommended_vcpu: 2,
    recommended_ram_mb: 2048,
    recommended_disk_gb: 20,
    color: 'from-cyan-500/20 to-blue-600/10 border-cyan-500/30 text-cyan-400',
  },
  {
    label: 'Fedora 44 (Rawhide/Next)',
    ref: 'images:fedora/44',
    category: 'dev',
    distro: 'fedora',
    desc: 'Siguiente generación de Fedora con las tecnologías más punteras.',
    arch: 'x86_64 / arm64',
    badge: 'Fedora 44',
    recommended_vcpu: 2,
    recommended_ram_mb: 2048,
    recommended_disk_gb: 20,
    color: 'from-blue-500/20 to-indigo-600/10 border-blue-500/30 text-blue-400',
  },
  {
    label: 'Fedora 43',
    ref: 'images:fedora/43',
    category: 'dev',
    distro: 'fedora',
    desc: 'Versión de vanguardia de Fedora con paquetería actualizada.',
    arch: 'x86_64 / arm64',
    badge: 'Fedora 43',
    recommended_vcpu: 2,
    recommended_ram_mb: 2048,
    recommended_disk_gb: 20,
    color: 'from-blue-500/20 to-indigo-600/10 border-blue-500/30 text-blue-400',
  },
  {
    label: 'Fedora 41',
    ref: 'images:fedora/41',
    category: 'dev',
    distro: 'fedora',
    desc: 'Innovación rápida con las últimas tecnologías del ecosistema Linux.',
    arch: 'x86_64 / arm64',
    badge: 'Cutting Edge',
    recommended_vcpu: 2,
    recommended_ram_mb: 2048,
    recommended_disk_gb: 20,
    color: 'from-blue-500/20 to-indigo-600/10 border-blue-500/30 text-blue-400',
  },
  {
    label: 'Fedora 40',
    ref: 'images:fedora/40',
    category: 'dev',
    distro: 'fedora',
    desc: 'Versión estable de Fedora para desarrollo y servidores.',
    arch: 'x86_64 / arm64',
    badge: 'Estable',
    recommended_vcpu: 2,
    recommended_ram_mb: 2048,
    recommended_disk_gb: 20,
    color: 'from-blue-500/20 to-indigo-600/10 border-blue-500/30 text-blue-400',
  },
  {
    label: 'NixOS 26.05',
    ref: 'images:nixos/26.05',
    category: 'dev',
    distro: 'nixos',
    desc: 'Distribución declarativa y reproducible basada en el gestor de paquetes Nix.',
    arch: 'x86_64 / arm64',
    badge: 'Declarative',
    recommended_vcpu: 2,
    recommended_ram_mb: 2048,
    recommended_disk_gb: 20,
    color: 'from-sky-500/20 to-blue-600/10 border-sky-500/30 text-sky-400',
  },
  {
    label: 'openSUSE Tumbleweed',
    ref: 'images:opensuse/tumbleweed',
    category: 'dev',
    distro: 'opensuse',
    desc: 'Rolling release estable con Zypper y soporte de snapshots Btrfs.',
    arch: 'x86_64',
    badge: 'Rolling',
    recommended_vcpu: 2,
    recommended_ram_mb: 2048,
    recommended_disk_gb: 20,
    color: 'from-lime-500/20 to-green-600/10 border-lime-500/30 text-lime-400',
  },
  {
    label: 'Kali Linux',
    ref: 'images:kali',
    category: 'dev',
    distro: 'kali',
    desc: 'Especializada en auditorías de seguridad y pruebas de penetración.',
    arch: 'x86_64',
    badge: 'Security',
    recommended_vcpu: 2,
    recommended_ram_mb: 4096,
    recommended_disk_gb: 25,
    color: 'from-blue-600/20 to-cyan-600/10 border-blue-500/30 text-blue-400',
  },
  {
    label: 'Gentoo Linux',
    ref: 'images:gentoo',
    category: 'dev',
    distro: 'gentoo',
    desc: 'Distribución source-based meta altamente optimizable con Portage.',
    arch: 'x86_64 / arm64',
    badge: 'Source',
    recommended_vcpu: 4,
    recommended_ram_mb: 4096,
    recommended_disk_gb: 30,
    color: 'from-purple-500/20 to-pink-600/10 border-purple-500/30 text-purple-400',
  },
];

export const CUSTOM_IMAGE = { label: 'Personalizada / Otra…', ref: '' };

/**
 * Friendly label for an image reference. Known presets map to their
 * distribution name; unknown/custom references fall back to the raw ref.
 */
export function labelForImage(ref, dynamicList = null) {
  if (!ref) return '';
  const list = dynamicList && dynamicList.length ? dynamicList : INCUS_IMAGE_PRESETS;
  const hit = list.find((p) => p.ref === ref);
  return hit ? hit.label : ref;
}

/**
 * Returns recommended resources for a given image reference.
 */
export function getRecommendedResources(ref, dynamicList = null) {
  if (!ref) return { vcpus: 2, ramMB: 2048, diskGB: 20 };
  const list = dynamicList && dynamicList.length ? dynamicList : INCUS_IMAGE_PRESETS;
  const hit = list.find((p) => p.ref === ref);
  if (hit) {
    return {
      vcpus: hit.rec_vcpus || hit.recommended_vcpu || 2,
      ramMB: hit.rec_ram_mb || hit.recommended_ram_mb || 2048,
      diskGB: hit.rec_disk_gb || hit.recommended_disk_gb || 20,
    };
  }
  return { vcpus: 2, ramMB: 2048, diskGB: 20 };
}

/**
 * True when ref is a bare Incus image fingerprint: the hex digest an
 * image is stored under, in full (64 chars) or in the 12-char short
 * form the catalog and `incus image list` show.
 *
 * A locally cached image need not carry any alias — an image pulled by
 * an instance create is stored by fingerprint alone — so the API hands
 * those back with the fingerprint AS their ref. Treating that as
 * malformed rejected a value that came straight out of our own catalog.
 */
export function isImageFingerprint(ref) {
  return /^[0-9a-f]{12}$|^[0-9a-f]{64}$/.test((ref || '').trim().toLowerCase());
}

/**
 * True when ref is something Incus can resolve to an image: either
 * <remote>:<alias>, or a local image fingerprint.
 */
export function isValidImageRef(ref) {
  const value = (ref || '').trim();
  if (!value) return false;
  return value.includes(':') || isImageFingerprint(value);
}

/**
 * Filter images list by category and search query.
 */
export function filterIncusImages(images, { category = 'all', search = '' } = {}) {
  const query = (search || '').trim().toLowerCase();
  let list = images || [];

  if (category === 'local') {
    list = list.filter((img) => img.is_local);
  } else if (category === 'images') {
    list = list.filter((img) => (img.ref || '').startsWith('images:'));
  } else if (category && category !== 'all') {
    list = list.filter((img) => img.category === category);
  }

  if (!query) return list;

  return list.filter((img) => {
    const haystack =
      `${img.label || ''} ${img.ref || ''} ${img.description || img.desc || ''} ${img.distro || ''} ${img.badge || ''}`.toLowerCase();
    return haystack.includes(query);
  });
}
