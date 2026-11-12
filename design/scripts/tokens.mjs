// Reading the DTCG source, shared by palette.mjs and emit.mjs so both resolve
// a reference the same way: primitives, then base, then one theme.
import fs from 'node:fs';

export const MODES = ['light', 'dark'];
const root = new URL('../tokens/', import.meta.url);
export const read = (p) => JSON.parse(fs.readFileSync(new URL(p, root)));
export const write = (p, o) => fs.writeFileSync(new URL(p, root), JSON.stringify(o, null, 2) + '\n');

// walk yields [path, token] for every node carrying a $value.
export function* walk(node, path = []) {
  if (node && typeof node === 'object' && '$value' in node) { yield [path.join('.'), node]; return; }
  for (const [k, v] of Object.entries(node ?? {})) if (!k.startsWith('$')) yield* walk(v, [...path, k]);
}

// index maps path to token across docs; later docs win, as in the resolver.
export const index = (...docs) => new Map(docs.flatMap((d) => [...walk(d)]));

export function resolveHex(tokens, path, seen = new Set()) {
  const t = tokens.get(path);
  if (!t || seen.has(path)) throw new Error(`unresolvable colour reference ${path}`);
  if (typeof t.$value === 'string') return resolveHex(tokens, t.$value.slice(1, -1), seen.add(path));
  return t.$value.hex;
}
