#!/usr/bin/env node
// Regression guard for hardcoded Spanish UI strings.
//
// The i18n dictionaries are not enough if a component renders a literal:
// English and Catalan users see Spanish. This scan is deliberately
// conservative (accents + unambiguous Spanish words) so it stays useful
// without flagging ordinary English text.

import fs from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');
const src = path.join(root, 'frontend', 'src');

const ACCENT = /[áéíóúÁÉÍÓÚñÑ¿¡üÜ]/;
const SPANISH = new RegExp(
  String.raw`\b(sin|para|por|los|las|del|que|como|más|menos|crear|guardar|borrar|eliminar|descargar|subir|seleccionar|activar|desactivar|recomendado|heredado|servidor|servidores|gráficos|consola|desplegando|preparando|plantilla|sonido|memoria|almacenamiento|nodo|clúster|tarea|advertencia|contraseña|incorrecta|cuota|excedida|recursos|protegidos|actualización|completada|éxito|pausada|apagada|encendida|reiniciar|arrancar|detener|conectado|desconectado|desconocido|moviendo|creando|formateando|montando|registrando|inicializando|receta|personalizada|contenedores|copias|despliegue|aprovisionando|construyendo|instalar|bloque|conexión|gestionado|pantalla|ajustes|añadir|quitar|activado|desactivado|habilitado|deshabilitado|pausado|suspendido|apagado|encendido|iniciado|detenido|eliminado|creado|guardado|actualizado|fallido|completado|seleccionado|configurado|instalado|descargado|subido|abierto|cerrado|automático|automática|personalizado|predeterminado|predeterminada|secundario|secundaria|interno|interna|externo|externa|rápido|rápida|lento|lenta|grande|pequeño|pequeña|ocupado|ocupada|interrumpir|suspender|limpiar|salir)\b`,
  'i'
);

const STRING_RE = /'([^'\\]*(?:\\.[^'\\]*)*)'|"([^"\\]*(?:\\.[^"\\]*)*)"|`([^`\\]*(?:\\.[^`\\]*)*)`/g;
const RAW_TEXT_RE = />([^<>{}]+)</g;

// English text that briefly matches a Spanish word.
const ALLOW = [
  /^Manual ban by administrator$/i,
  /^Send Ctrl\+Alt\+Del$/i,
  /^Ctrl\+Alt\+Del$/i,
  /^No items$/i,
  /^No-systemd$/i,
  /^(en|es|ca)-(US|ES)$/i,
  /^Manual$/i,
];

function allowed(value) {
  return ALLOW.some((re) => re.test(value));
}

function isTranslationKey(line, index) {
  const before = line.slice(0, index);
  return /(?:^|[^A-Za-z0-9_])t\(\s*$/.test(before) || /(?:labelKey|descriptionKey)\s*:\s*$/.test(before);
}

function isTechnical(value) {
  if (/^(https?:\/\/|\/api\/|\/|#|\.)/.test(value)) return true;
  if (/^[a-z0-9_./:#%{},-]+$/i.test(value)) return true;
  if (/(^|\s)(flex|grid|block|inline|hidden|text-|bg-|border-|hover:|focus:|from-|to-|w-|h-|p-|m-|rounded|items-|justify-|gap-|space-)/.test(value)) return true;
  return false;
}

function suspicious(value) {
  if (!value || value.length < 2) return false;
  if (allowed(value) || isTechnical(value)) return false;
  return ACCENT.test(value) || SPANISH.test(value);
}

function stripComments(text) {
  const out = [];
  let inHtmlComment = false;
  let inBlockComment = false;
  for (const original of text.split('\n')) {
    let line = original;
    if (inHtmlComment) {
      const end = line.indexOf('-->');
      if (end === -1) {
        out.push('');
        continue;
      }
      line = line.slice(end + 3);
      inHtmlComment = false;
    }
    if (inBlockComment) {
      const end = line.indexOf('*/');
      if (end === -1) {
        out.push('');
        continue;
      }
      line = line.slice(end + 2);
      inBlockComment = false;
    }
    for (;;) {
      const start = line.indexOf('<!--');
      if (start === -1) break;
      const end = line.indexOf('-->', start + 4);
      if (end === -1) {
        line = line.slice(0, start);
        inHtmlComment = true;
        break;
      }
      line = line.slice(0, start) + line.slice(end + 3);
    }
    for (;;) {
      const start = line.indexOf('/*');
      if (start === -1) break;
      const end = line.indexOf('*/', start + 2);
      if (end === -1) {
        line = line.slice(0, start);
        inBlockComment = true;
        break;
      }
      line = line.slice(0, start) + line.slice(end + 2);
    }
    if (line.trim().startsWith('//')) {
      out.push('');
      continue;
    }
    out.push(line);
  }
  return out.join('\n');
}

function scanFile(file) {
  const text = stripComments(fs.readFileSync(file, 'utf8'));
  const lines = text.split('\n');
  const hits = [];
  for (let i = 0; i < lines.length; i++) {
    const line = lines[i];
    if (!line.trim() || line.trim().startsWith('*')) continue;
    let m;
    STRING_RE.lastIndex = 0;
    while ((m = STRING_RE.exec(line)) !== null) {
      const value = m[1] ?? m[2] ?? m[3] ?? '';
      if (isTranslationKey(line, m.index)) continue;
      if (suspicious(value)) hits.push({ line: i + 1, value });
    }
  }
  if (file.endsWith('.svelte')) {
    let inScript = false;
    let inStyle = false;
    for (let i = 0; i < lines.length; i++) {
      const lower = lines[i].toLowerCase();
      if (inScript) {
        if (lower.includes('</script>')) inScript = false;
        continue;
      }
      if (inStyle) {
        if (lower.includes('</style>')) inStyle = false;
        continue;
      }
      if (lower.includes('<script')) {
        if (!lower.includes('</script>')) inScript = true;
        continue;
      }
      if (lower.includes('<style')) {
        if (!lower.includes('</style>')) inStyle = true;
        continue;
      }
      RAW_TEXT_RE.lastIndex = 0;
      let m;
      while ((m = RAW_TEXT_RE.exec(lines[i])) !== null) {
        const value = m[1].trim();
        if (suspicious(value)) hits.push({ line: i + 1, value });
      }
    }
  }
  return hits;
}

function walk(dir, out = []) {
  for (const entry of fs.readdirSync(dir, { withFileTypes: true })) {
    const full = path.join(dir, entry.name);
    if (entry.isDirectory()) walk(full, out);
    else if (/\.(svelte|js)$/.test(entry.name)) out.push(full);
  }
  return out;
}

const files = walk(src).filter(
  (f) => !f.endsWith(path.join('lib', 'i18n.svelte.js')) && !/\.(dom\.)?test\.js$/.test(f)
);

let count = 0;
for (const file of files) {
  for (const hit of scanFile(file)) {
    count++;
    console.log(`${path.relative(root, file)}:${hit.line}: ${hit.value.slice(0, 160)}`);
  }
}

if (count > 0) {
  console.error(`check-hardcoded-es: ${count} hardcoded Spanish UI string(s) found`);
  process.exit(1);
}
console.log('check-hardcoded-es: OK');
