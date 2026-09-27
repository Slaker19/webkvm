#!/usr/bin/env node
// Finds components used in a Svelte template that are never imported
// or declared locally. Svelte 5 compiles an unresolved capitalised tag
// into a direct call to a bare identifier, so the page builds, ships
// and type-checks clean — and then throws ReferenceError in the browser
// the first time that branch renders. It is invisible until the exact
// code path runs, which is how a "Mover a otro pool" button ends up
// doing nothing at all.
//
// Usage: node scripts/check-undefined-components.mjs
import { readdirSync, readFileSync, statSync } from 'node:fs';
import { join, relative } from 'node:path';

const ROOT = new URL('..', import.meta.url).pathname;
const SRC = join(ROOT, 'frontend/src');

// Every component that exists in the project, so a missing import can
// be told apart from a name that is simply not a component.
const known = new Map(); // lowercased basename -> real path(s)
function index(dir) {
  for (const e of readdirSync(dir)) {
    const p = join(dir, e);
    const st = statSync(p);
    if (st.isDirectory()) {
      if (e === 'node_modules') continue;
      index(p);
    } else if (e.endsWith('.svelte')) {
      const base = e.slice(0, -'.svelte'.length).toLowerCase();
      if (!known.has(base)) known.set(base, []);
      known.get(base).push(p);
    }
  }
}
index(SRC);

function walk(dir, out = []) {
  for (const e of readdirSync(dir)) {
    const p = join(dir, e);
    if (statSync(p).isDirectory()) walk(p, out);
    else if (e.endsWith('.svelte')) out.push(p);
  }
  return out;
}

const findings = [];

for (const file of walk(SRC)) {
  const src = readFileSync(file, 'utf8');
  // A component may carry more than one script block (a module block
  // plus the instance block), and identifiers can be bound in any of
  // them. Reading only the first one makes every import in the second
  // look missing.
  const scripts = [...src.matchAll(/<script\b[^>]*>([\s\S]*?)<\/script[^>]*>/gi)];
  if (!scripts.length) continue;
  const code = scripts.map((m) => m[1]).join('\n');
  const firstIdx = scripts[0].index;
  const lastIdx = scripts[scripts.length - 1].index + scripts[scripts.length - 1][0].length;
  // Strip the trailing <style> block so CSS selectors never count.
  const afterScript = src.slice(lastIdx);
  const styleIdx = afterScript.search(/<style\b[^>]*>/i);
  const tpl = styleIdx === -1 ? afterScript : afterScript.slice(0, styleIdx);
  void firstIdx;

  // Identifiers bound in this file: imports, declarations, props.
  const bound = new Set();
  for (const m of code.matchAll(
    /import\s+(?:([\w$]+)\s*,\s*)?(?:\{([^}]*)\}|\*\s+as\s+([\w$]+)|([\w$]+))?/g
  )) {
    for (const g of [m[1], m[4]]) if (g) bound.add(g);
    if (m[3]) bound.add(m[3]);
    if (m[2])
      for (const part of m[2].split(',')) {
        const name = part.trim().split(/\s+as\s+/).pop();
        if (name) bound.add(name);
      }
  }
  for (const m of code.matchAll(/\b(?:let|const|var|function|class)\s+([\w$]+)/g))
    bound.add(m[1]);
  for (const m of code.matchAll(/\b([\w$]+)\s*[:=]/g)) bound.add(m[1]);

  // Components referenced as <Name ...> or <Name> in the template.
  const used = new Map();
  for (const m of tpl.matchAll(/<([A-Z][\w$]*)[\s/>]/g)) {
    const name = m[1];
    if (bound.has(name)) continue;
    if (!used.has(name)) used.set(name, tpl.slice(0, m.index).split('\n').length);
  }

  for (const [name, line] of used) {
    // Only report names that are a real component somewhere in the
    // project: an unknown capitalised tag is a different problem.
    if (!known.has(name.toLowerCase())) continue;
    findings.push({
      file: relative(ROOT, file),
      line,
      name,
      candidates: known.get(name.toLowerCase()).map((p) => relative(ROOT, p)),
    });
  }
}

if (!findings.length) {
  console.log('check-undefined-components: OK');
  process.exit(0);
}
for (const f of findings) {
  console.log(
    `${f.file}:${f.line}  <${f.name}> is used in the template but never imported.\n` +
      `  it would throw ReferenceError in the browser when rendered. Candidate: ${f.candidates.join(', ')}`
  );
}
console.log(`\ncheck-undefined-components: ${findings.length} problem(s)`);
process.exit(1);
