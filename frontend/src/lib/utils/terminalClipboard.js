// Clipboard provider for @xterm/addon-clipboard (OSC 52) used by the serial
// and host terminals.
//
// Whatever runs inside the terminal is untrusted (a guest VM, or any program
// printing attacker-controlled text), so:
//   - the guest can NEVER read the user's clipboard: readText always
//     resolves to an empty string;
//   - guest writes are never applied automatically: the text is handed to
//     onWrite(), which parks it behind a "Copy" button the user must click
//     (a real user gesture) before it reaches navigator.clipboard.

/** Hard cap on the amount of guest-provided text we keep around. */
export const MAX_PENDING_COPY = 1 << 20;

/**
 * @param {(text: string) => void} onWrite called with the text the guest
 *   asked to place on the clipboard (not yet copied).
 */
export function createGuestClipboardProvider(onWrite) {
  return {
    readText() {
      return '';
    },
    writeText(_selection, text) {
      if (typeof text !== 'string' || text === '') return;
      onWrite(text.length > MAX_PENDING_COPY ? text.slice(0, MAX_PENDING_COPY) : text);
    },
  };
}
