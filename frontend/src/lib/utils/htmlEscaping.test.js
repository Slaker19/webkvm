import { describe, expect, it } from 'vitest';

// This suite documents the escaping contract that {@html} depends on.
// It exists because two call sites wrapped USER-SUPPLIED values in
// htmlVar() — the wrapper that deliberately disables escaping — and the
// result was stored XSS in the operator's own session:
//
//   DeleteVmDialog:  <strong>${vmName}</strong>
//   Storage:         <code ...>/mnt/${poolName}</code>
//
// A VM or pool named `<img src=x onerror=alert(1)>` ran arbitrary
// JavaScript as soon as an admin opened the delete dialog or the pool
// hint. Both now pass a plain string, which t() escapes.

/** Mirror of the interpolation in src/lib/i18n.svelte.js. */
function escapeHtml(s) {
  return s
    .replace(/&/g, '&amp;')
    .replace(/</g, '&lt;')
    .replace(/>/g, '&gt;')
    .replace(/"/g, '&quot;')
    .replace(/'/g, '&#39;');
}

function interpolate(template, vars) {
  let out = template;
  for (const [k, v] of Object.entries(vars)) {
    out = out.replace(new RegExp(`\\{${k}\\}`, 'g'), escapeHtml(String(v)));
  }
  return out;
}

const XSS = '<img src=x onerror=alert(1)>';

describe('html interpolation escaping', () => {
  it('escapes a plain interpolated value', () => {
    // The surrounding <strong> is the developer's literal markup and
    // stays intact; only the VALUE is escaped. That is the whole point
    // of the split: literals render, data never can.
    const out = interpolate('<strong>{name}</strong>', { name: XSS });
    expect(out).toBe('<strong>&lt;img src=x onerror=alert(1)&gt;</strong>');
    expect(out).not.toContain('<img');
  });

  it('does not let a value inject its own markup', () => {
    const out = interpolate('<strong>{name}</strong>', {
      name: '</strong><script>alert(1)</script><strong>',
    });
    expect(out).not.toContain('<script>');
    expect(out).not.toContain('</strong><script');
  });

  it('escapes attribute-breaking characters', () => {
    const out = interpolate('<code title="{name}">x</code>', { name: '" onmouseover="alert(1)' });
    expect(out).not.toContain('" onmouseover="');
  });

  it('leaves a normal VM name readable', () => {
    // The fix must not mangle the everyday case — a VM called
    // "prod-web-01" still has to render as itself.
    const out = interpolate('<strong>{name}</strong>', { name: 'prod-web-01' });
    expect(out).toBe('<strong>prod-web-01</strong>');
  });

  it('escapes an ampersand so entities cannot be forged', () => {
    const out = interpolate('{name}', { name: '&lt;img&gt;' });
    expect(out).toBe('&amp;lt;img&amp;gt;');
  });
});
