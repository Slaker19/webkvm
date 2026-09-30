import { describe, it, expect } from 'vitest';
import { setLocale, t, messages } from '../i18n.svelte.js';

describe('VmCreate wizard mode translations', () => {
  const requiredKeys = [
    'modeEasy',
    'modeAdvanced',
    'modeEasyDesc',
    'modeAdvancedDesc',
    'switchToAdvancedNotice',
    'switchToAdvancedAction',
    'cpuUnitsDetailedHelper',
    'kvmHiddenDetailedHelper',
    'cpuFlagsDetailedHelper',
  ];

  const langs = ['en', 'es', 'ca'];

  langs.forEach((lang) => {
    describe(`Language: ${lang}`, () => {
      it(`has all wizard mode keys defined in vmCreate`, () => {
        setLocale(lang);

        for (const key of requiredKeys) {
          const val = t(`vmCreate.${key}`);
          expect(
            val,
            `Key "vmCreate.${key}" should be defined and not equal key name in ${lang}`
          ).not.toBe(`vmCreate.${key}`);
          expect(typeof val).toBe('string');
          expect(val.length).toBeGreaterThan(0);
        }

        const detailKeys = [
          'cpuPriorityLabel',
          'cpuPriorityHelper',
          'cpuPriorityNormal',
          'cpuPriorityNormalDesc',
          'cpuPriorityLow',
          'cpuPriorityLowDesc',
          'cpuPriorityHigh',
          'cpuPriorityHighDesc',
          'cpuPriorityCustom',
          'cpuFlagsAutoTitle',
          'cpuFlagsAutoDesc',
          'cpuFlagsCustomize',
          'cpuFlagsResetAuto',
          'fstrimBtn',
          'fstrimDesc',
          'fstrimSuccess',
          'fstrimError',
          'guestUsers',
          'guestTimezone',
        ];

        for (const key of detailKeys) {
          const val = t(`vmDetail.${key}`);
          expect(
            val,
            `Key "vmDetail.${key}" should be defined and not equal key name in ${lang}`
          ).not.toBe(`vmDetail.${key}`);
          expect(typeof val).toBe('string');
          expect(val.length).toBeGreaterThan(0);
        }

        const networkKeys = [
          'directStaticConfigTitle',
          'directStaticConfigDesc',
          'ipSubnet',
          'directCidrHelp',
          'gatewayHelp',
          'dns1',
          'dns2',
        ];

        for (const key of networkKeys) {
          const val = t(`networks.${key}`);
          expect(
            val,
            `Key "networks.${key}" should be defined and not equal key name in ${lang}`
          ).not.toBe(`networks.${key}`);
          expect(typeof val).toBe('string');
          expect(val.length).toBeGreaterThan(0);
        }
      });
    });
  });

  describe('Full translation key parity', () => {
    function getAllKeys(obj, prefix = '') {
      let keys = [];
      for (const k in obj) {
        if (typeof obj[k] === 'object' && obj[k] !== null) {
          keys = keys.concat(getAllKeys(obj[k], prefix + k + '.'));
        } else {
          keys.push(prefix + k);
        }
      }
      return keys;
    }

    const enKeys = new Set(getAllKeys(messages.en));
    const esKeys = new Set(getAllKeys(messages.es));
    const caKeys = new Set(getAllKeys(messages.ca));

    it('en and es have matching keys', () => {
      const missingInEs = [...enKeys].filter((k) => !esKeys.has(k));
      const extraInEs = [...esKeys].filter((k) => !enKeys.has(k));
      expect(missingInEs, `Keys in en but missing in es: ${missingInEs.join(', ')}`).toEqual([]);
      expect(extraInEs, `Keys in es but missing in en: ${extraInEs.join(', ')}`).toEqual([]);
    });

    it('en and ca have matching keys', () => {
      const missingInCa = [...enKeys].filter((k) => !caKeys.has(k));
      const extraInCa = [...caKeys].filter((k) => !enKeys.has(k));
      expect(missingInCa, `Keys in en but missing in ca: ${missingInCa.join(', ')}`).toEqual([]);
      expect(extraInCa, `Keys in ca but missing in en: ${extraInCa.join(', ')}`).toEqual([]);
    });
  });
});
