// @ts-check
import js from '@eslint/js';
import ts from 'typescript-eslint';
import svelte from 'eslint-plugin-svelte';
import prettier from 'eslint-config-prettier';
import globals from 'globals';

/** @type {import('eslint').Linter.Config[]} */
export default [
  js.configs.recommended,
  ...ts.configs.recommended,
  ...svelte.configs['flat/recommended'],
  prettier,
  ...svelte.configs['flat/prettier'],
  {
    languageOptions: {
      globals: {
        ...globals.browser,
        ...globals.node,
      },
    },
  },
  {
    files: ['**/*.svelte'],
    languageOptions: {
      parserOptions: {
        parser: ts.parser,
      },
    },
  },
  {
    rules: {
      // Allow underscore-prefixed unused args in callbacks.
      '@typescript-eslint/no-unused-vars': [
        'error',
        {
          argsIgnorePattern: '^_',
          varsIgnorePattern: '^_',
          caughtErrorsIgnorePattern: '^_',
        },
      ],
      // Allow `any` in JS for now; we'll tighten later.
      '@typescript-eslint/no-explicit-any': 'off',
      // Svelte 5 runes are still new; some false positives.
      'svelte/no-at-html-tags': 'off',
      'svelte/no-useless-mustaches': 'off',
      // ----------------------------------------------------------------
      // TODO(phase-1.5, svelte-5-migration): re-enable these once the
      // refactor is done. They are real perf/correctness wins but
      // require touching ~50 sites and need per-site review.
      // ----------------------------------------------------------------
      'svelte/require-each-key': 'off',
      'svelte/prefer-svelte-reactivity': 'off',
      // No console in prod, but during dev we use it.
      'no-console': ['warn', { allow: ['warn', 'error'] }],
    },
  },
  {
    ignores: [
      'build/',
      '.svelte-kit/',
      'dist/',
      'node_modules/',
      'src/lib/novnc/**', // third-party noVNC assets
    ],
  },
];
