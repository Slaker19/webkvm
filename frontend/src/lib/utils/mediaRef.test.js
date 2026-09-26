import { describe, it, expect } from 'vitest';
import { isMediaRef } from './mediaRef.js';

// Avatars, logos and favicons are rendered as <img src>. The value can
// reach the UI from localStorage, which anything running in the page can
// edit, so the client re-checks the shape instead of trusting that the
// backend was the only writer.
describe('isMediaRef', () => {
  it('rejects values that would be an injection or a leak', () => {
    const bad = [
      ['javascript:alert(1)', 'script URL'],
      ['data:text/html;base64,PHNjcmlwdD4=', 'data URL'],
      ['https://evil.example/pixel.png', 'external tracker'],
      ['//evil.example/x.png', 'protocol-relative URL'],
      ['/api/media/custom:../../../etc/passwd/raw', 'traversal'],
      ['/api/media/../../etc/passwd/raw', 'traversal above the prefix'],
      ['/api/media/custom:a/b/raw', 'nested path'],
      ['/api/media//raw', 'empty id'],
      ['/api/media/unknown:x.png/raw', 'unknown category'],
      ['/api/media/x.png/raw', 'missing category'],
      ['/api/media/custom:x.png/raw?next=//evil', 'query smuggling'],
      ['/api/media/custom:x.png/raw#frag', 'fragment smuggling'],
      ['/api/media/custom:x.png', 'missing /raw suffix'],
      ['api/media/custom:x.png/raw', 'missing leading slash'],
      ['', 'empty string'],
      ['   ', 'whitespace'],
    ];
    for (const [value, why] of bad) {
      expect(isMediaRef(value), `${value} (${why}) must be rejected`).toBe(false);
    }
  });

  it('rejects non-string values instead of throwing', () => {
    for (const v of [null, undefined, 0, 42, {}, [], true]) {
      expect(isMediaRef(v)).toBe(false);
    }
  });

  it('accepts canonical media references', () => {
    const good = [
      '/api/media/system:avatar-default.svg/raw',
      '/api/media/custom:1790124038-1c0e9fb8-wallhaven-3q5lmy.jpg/raw',
      '/api/media/custom:upload.png/raw',
    ];
    for (const v of good) {
      expect(isMediaRef(v), `${v} must be accepted`).toBe(true);
    }
  });
});
