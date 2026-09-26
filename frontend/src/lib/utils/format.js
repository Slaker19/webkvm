/** Formats a byte rate (bytes/sec) as a human-readable string, e.g. "3.2 MB/s". */
export function formatRate(b) {
  if (b == null) return '0 B/s';
  if (b < 1024) return b.toFixed(0) + ' B/s';
  if (b < 1024 * 1024) return (b / 1024).toFixed(1) + ' KB/s';
  if (b < 1024 * 1024 * 1024) return (b / 1024 / 1024).toFixed(2) + ' MB/s';
  return (b / 1024 / 1024 / 1024).toFixed(2) + ' GB/s';
}

/** Formats bytes into a human-readable size string, e.g. "1.45 GB", "450 MB". */
export function formatBytes(b) {
  if (b == null || isNaN(b) || b <= 0) return '0 B';
  const units = ['B', 'KB', 'MB', 'GB', 'TB', 'PB'];
  // Integer loop instead of log(): floating-point log can land one unit
  // off at exact boundaries (e.g. 1024 -> KB vs MB).
  let i = 0;
  let v = b;
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024;
    i++;
  }
  // Decimal places per unit: whole bytes, 1 for KB, 2 for MB and above.
  const decimals = i === 0 ? 0 : i === 1 ? 1 : 2;
  return `${v.toFixed(decimals)} ${units[i]}`;
}

/** Formats a RAM size given in MB as "4.0 GB" (or "512 MB"). */
export function formatRAM(mb) {
  if (!mb) return '—';
  if (mb >= 1024) return `${(mb / 1024).toFixed(1)} GB`;
  return `${mb} MB`;
}

/** Formats remaining seconds as ETA string, e.g. "45s", "2m 10s", "1h 15m". */
export function formatETA(sec) {
  if (sec == null || isNaN(sec) || sec <= 0) return '';
  if (sec < 60) return `${Math.round(sec)}s`;
  if (sec < 3600) {
    const m = Math.floor(sec / 60);
    const s = Math.round(sec % 60);
    return `${m}m ${s}s`;
  }
  const h = Math.floor(sec / 3600);
  const m = Math.round((sec % 3600) / 60);
  return `${h}h ${m}m`;
}
