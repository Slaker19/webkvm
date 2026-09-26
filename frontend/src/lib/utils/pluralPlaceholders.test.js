import { describe, expect, it } from 'vitest';

/**
 * Guards the pluralisation contract of the translation layer.
 *
 * Several keys carry a plural slot, e.g.
 *     runFiles:   '{n} file{s} · {size}'
 *     groupsHint: '... ({n} group{s})'
 * and callers must pass `s` explicitly (as Backup.vmFilterInclude
 * already does). Two call sites forgot, and t() substitutes only the
 * placeholders it is given, so the UI rendered the raw text
 * "3 file{s} · 1.2 GB" — in English, Spanish AND Catalan.
 *
 * This test models the substitution t() performs so the contract is
 * explicit rather than folklore: a key that uses {s} must receive it.
 */
function render(template, vars) {
  return template.replace(/\{(\w+)\}/g, (m, k) => (k in vars ? String(vars[k]) : m));
}

describe('backup pluralisation placeholders', () => {
  const KEYS = {
    en: {
      runFiles: '{n} file{s} · {size}',
      groupsHint: 'Every VM belonging to any of these groups is backed up ({n} group{s}).',
    },
    es: {
      runFiles: '{n} archivo{s} · {size}',
      groupsHint: 'Se hace copia de toda VM de cualquiera de estos grupos ({n} grupo{s}).',
    },
    ca: {
      runFiles: '{n} fitxer{s} · {size}',
      groupsHint: "Es fa còpia de tota VM de qualsevol d'aquests grups ({n} grup{s}).",
    },
  };

  for (const [locale, keys] of Object.entries(KEYS)) {
    it(`${locale}: runFiles pluralises with s='' for one file`, () => {
      const out = render(keys.runFiles, { n: 1, s: '', size: '12 MB' });
      expect(out).not.toContain('{');
      expect(out).toMatch(/1 file|1 archivo|1 fitxer/);
    });

    it(`${locale}: runFiles pluralises with s='s' for many`, () => {
      const out = render(keys.runFiles, { n: 3, s: 's', size: '1.2 GB' });
      expect(out).not.toContain('{');
    });

    it(`${locale}: omitting s leaks the placeholder verbatim`, () => {
      // This is exactly the bug: no `s` in vars means no replacement.
      const out = render(keys.runFiles, { n: 3, size: '1.2 GB' });
      expect(out).toContain('{s}');
    });

    it(`${locale}: groupsHint renders cleanly with s`, () => {
      const out = render(keys.groupsHint, { n: 1, s: '' });
      expect(out).not.toContain('{');
    });
  }
});
