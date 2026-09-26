import { onDestroy } from 'svelte';

/**
 * useCopyFeedback — "copied!" state machine shared by every modal that
 * copies text to the clipboard and needs to flash a confirmation for a
 * couple of seconds (PasswordModal, CredentialsModal, and any future
 * one). Extracted because both modals had byte-for-byte identical
 * `copied` state + `setTimeout` + `onDestroy` cleanup blocks — the kind
 * of duplication that silently drifts (one component fixes a timer leak,
 * the other doesn't) instead of being an intentional design choice.
 *
 * Usage:
 *   const copy = useCopyFeedback();
 *   ...
 *   <Button onclick={() => copy.copy(text)}>
 *     {copy.copied ? 'Copied!' : 'Copy'}
 *   </Button>
 */
export function useCopyFeedback(resetMs = 2000) {
  let copied = $state(false);
  let timer = null;

  function copy(text) {
    navigator.clipboard.writeText(text);
    copied = true;
    clearTimeout(timer);
    timer = setTimeout(() => (copied = false), resetMs);
  }

  onDestroy(() => clearTimeout(timer));

  return {
    get copied() {
      return copied;
    },
    copy,
  };
}
