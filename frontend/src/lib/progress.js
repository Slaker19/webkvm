// progress.js — translates backend job stage codes into the active
// locale. The backend reports stable phase codes (preparing,
// compress_vm, config, uploading, restore_copying, ...) with their
// interpolation vars; the UI renders them in the user's language
// instead of showing backend text.

export function progressLabel(stage, vars, fallback, t) {
  switch (stage) {
    case 'preparing':
      return t('backup.progress.preparing');
    case 'compress_vm':
      return t('backup.progress.compressVm', vars || {});
    case 'config':
      return t('backup.progress.config');
    case 'config_copy':
      return t('backup.progress.configCopy');
    case 'uploading':
      return t('backup.progress.uploading', vars || {});
    case 'restore_preparing':
      return t('backup.progress.restorePreparing');
    case 'restore_copying':
      return t('backup.progress.restoreCopying');
    case 'restore_define':
      return t('backup.progress.restoreDefine');
    case 'restore_extract':
      return t('backup.progress.restoreExtract', vars || {});
    case 'done':
      return t('backup.progress.done');
    case 'failed':
      return t('backup.progress.failed');
    default:
      return fallback || '';
  }
}
