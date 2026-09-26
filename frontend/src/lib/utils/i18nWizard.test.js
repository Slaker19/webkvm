import { describe, it, expect } from 'vitest';
import { setLocale, t } from '../i18n.svelte.js';

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
      });
    });
  });
});
