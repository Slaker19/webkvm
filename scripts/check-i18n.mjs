#!/usr/bin/env node
// check-i18n — compares the en/es/ca message dictionaries in
// src/lib/i18n.svelte.js and fails if their key sets are not identical.
//
// i18n.svelte.js uses Svelte 5 $state, so it can't be imported directly
// in a plain Node process. Instead we extract each top-level language
// object with a brace-matching scanner and evaluate it as a plain object
// literal (all keys are quoted single/double strings, no functions).
//
// Exit codes: 0 = symmetric, 1 = asymmetric (missing keys printed).
import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';

const target = process.argv[2] || fileURLToPath(new URL('../frontend/src/lib/i18n.svelte.js', import.meta.url));

let src;
try {
  src = readFileSync(target, 'utf8');
} catch {
  console.error(`check-i18n: cannot read ${target}`);
  process.exit(1);
}

// Extract the body of `messages.<key> = { ... }` (top-level object) with
// brace matching so nested braces inside string values are handled by the
// same counter (strings never contain unbalanced braces in this file).
function extractObject(src, key) {
  const re = new RegExp(`\\b${key}\\s*:\\s*\\{`);
  const m = re.exec(src);
  if (!m) return null;
  let i = src.indexOf('{', m.index);
  let depth = 0;
  for (; i < src.length; i++) {
    if (src[i] === '{') depth++;
    else if (src[i] === '}') {
      depth--;
      if (depth === 0) return src.slice(m.index + m[0].length - 1, i + 1);
    }
  }
  return null;
}

function flatKeys(obj, prefix = '', out = new Set()) {
  for (const [k, v] of Object.entries(obj)) {
    const kk = prefix ? `${prefix}.${k}` : k;
    if (v && typeof v === 'object' && !Array.isArray(v)) flatKeys(v, kk, out);
    else out.add(kk);
  }
  return out;
}

const dicts = {};
for (const lang of ['en', 'es', 'ca']) {
  const text = extractObject(src, lang);
  if (!text) {
    console.error(`check-i18n: could not find messages.${lang} block`);
    process.exit(1);
  }
  try {
    dicts[lang] = Function(`"use strict"; return (${text});`)();
  } catch (err) {
    console.error(`check-i18n: failed to evaluate messages.${lang}: ${err.message}`);
    process.exit(1);
  }
}

const keys = Object.fromEntries(Object.entries(dicts).map(([l, d]) => [l, flatKeys(d)]));

let failed = false;
for (const [a, b] of [
  ['en', 'es'],
  ['en', 'ca'],
  ['es', 'ca'],
]) {
  const missingInB = [...keys[a]].filter((k) => !keys[b].has(k));
  const missingInA = [...keys[b]].filter((k) => !keys[a].has(k));
  if (missingInB.length) {
    failed = true;
    console.error(`check-i18n: ${missingInB.length} key(s) present in "${a}" but missing in "${b}":`);
    for (const k of missingInB) console.error(`  - ${k}`);
  }
  if (missingInA.length) {
    failed = true;
    console.error(`check-i18n: ${missingInA.length} key(s) present in "${b}" but missing in "${a}":`);
    for (const k of missingInA) console.error(`  - ${k}`);
  }
}

if (failed) {
  console.error(`check-i18n: FAIL (en=${keys.en.size}, es=${keys.es.size}, ca=${keys.ca.size})`);
  process.exit(1);
}
console.log(`check-i18n: OK (en=${keys.en.size}, es=${keys.es.size}, ca=${keys.ca.size})`);