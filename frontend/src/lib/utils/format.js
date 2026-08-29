/** Formats a byte rate (bytes/sec) as a human-readable string, e.g. "3.2 MB/s". */
export function formatRate(b) {
  if (b == null) return '0 B/s';
  if (b < 1024) return b.toFixed(0) + ' B/s';
  if (b < 1024 * 1024) return (b / 1024).toFixed(1) + ' KB/s';
  if (b < 1024 * 1024 * 1024) return (b / 1024 / 1024).toFixed(2) + ' MB/s';
  return (b / 1024 / 1024 / 1024).toFixed(2) + ' GB/s';
}
