// Fills every Leonardo-driven colour in tokens/themes/*.tokens.json from its
// scale and target ratio, checks each declared contrast floor, picks the
// ANSI-256 value per terminal role, and rewrites the contrast table in DESIGN.md.
import fs from 'node:fs';
import { Theme, Color, BackgroundColor } from '@adobe/leonardo-contrast-colors';
import ColorJS from 'colorjs.io';
import { MODES, read, write, walk, index, resolveHex } from './tokens.mjs';

const SURFACES = ['color.surface.base', 'color.surface.raised', 'color.surface.card', 'color.surface.overlay'];
// The TUI never paints a background, so terminal roles are checked against
// common terminal defaults: VS Code dark, plain white and Solarized Light.
const TERMINAL_BG = { dark: ['#1e1e1e'], light: ['#ffffff', '#fdf6e3'] };

const primitives = read('primitives.tokens.json');
const base = read('base.tokens.json');
const themes = Object.fromEntries(MODES.map((m) => [m, read(`themes/${m}.tokens.json`)]));

const colour = (h) => ({ colorSpace: 'srgb', components: [1, 3, 5].map((i) => +(parseInt(h.slice(i, i + 2), 16) / 255).toFixed(4)), hex: h });
const wcag = (a, b) => new ColorJS(a).contrast(new ColorJS(b), 'WCAG21');
const apca = (fg, bg) => Math.abs(new ColorJS(bg).contrast(new ColorJS(fg), 'APCA'));

const prim = index(primitives);
const scaleKeys = (scale) => [...prim.keys()].filter((p) => p.startsWith(`palette.${scale}.`)).map((p) => resolveHex(prim, p));

for (const mode of MODES) {
  const doc = themes[mode];
  const { background, lightness } = doc.$extensions['dev.hydra.leonardo'];
  const roles = [...walk(doc)].filter(([, t]) => t.$extensions?.['dev.hydra.leonardo']);
  const byScale = {};
  let bgPath;
  for (const [path, t] of roles) {
    const { scale, ratio, background: isBg } = t.$extensions['dev.hydra.leonardo'];
    if (isBg) { bgPath = path; continue; }
    (byScale[scale] ??= {})[path] = ratio;
  }
  const mk = (Ctor, scale) => new Ctor({ name: scale, colorKeys: scaleKeys(scale), colorSpace: 'OKLCH', ratios: byScale[scale] ?? {} });
  const bg = mk(BackgroundColor, background);
  const colors = Object.keys(byScale).filter((s) => s !== background).map((s) => mk(Color, s));
  const out = new Theme({ colors: [bg, ...colors], backgroundColor: bg, lightness, output: 'HEX' }).contrastColors;
  const values = { [bgPath]: out[0].background };
  for (const c of out.slice(1)) for (const v of c.values) values[v.name] = v.value;
  for (const [path, t] of roles) t.$value = colour(values[path].toLowerCase());
}

// ANSI-256: the cube and grey ramp only (0-15 follow the user's theme).
const lv = [0, 95, 135, 175, 215, 255];
const ansi = [];
for (let i = 0; i < 216; i++) ansi.push([16 + i, [lv[Math.floor(i / 36)], lv[Math.floor(i / 6) % 6], lv[i % 6]]]);
for (let i = 0; i < 24; i++) ansi.push([232 + i, Array(3).fill(8 + i * 10)]);
const ansiHex = (rgb) => '#' + rgb.map((x) => x.toString(16).padStart(2, '0')).join('');
// hueGap is the OKLCH hue distance in degrees from role a to candidate b. A
// role on the neutral scale may take any grey; a hue role may never turn grey.
function hueGap(a, b, neutral) {
  const [, , ha] = a.to('oklch').coords, [, cb, hb] = b.to('oklch').coords;
  if (cb < 0.08 && neutral) return 0;
  // colorjs reports a grey's hue as NaN, which would compare false and pass.
  if (Number.isNaN(ha)) return cb < 0.04 ? 0 : 180;
  if (cb < 0.04) return 180;
  const d = Math.abs(ha - hb) % 360;
  return Math.min(d, 360 - d);
}

// nearest256 is the closest in-hue index by ΔE2000 that meets the floor on
// every terminal background (plain nearest-match broke TUI-03) and that no
// sibling role took, so a family keeps its hierarchy at 256 colours.
function nearest256(h, refs, min, taken, neutral) {
  const c = new ColorJS(h);
  let best;
  for (const [i, rgb] of ansi) {
    const x = ansiHex(rgb);
    if (taken.has(i) || hueGap(c, new ColorJS(x), neutral) > 40 || (min && refs.some((b) => wcag(x, b) < min))) continue;
    const d = c.deltaE(new ColorJS(x), '2000');
    if (!best || d < best.d) best = { i, d, x };
  }
  return best;
}

const failures = [];
const report = {};
for (const mode of MODES) {
  const tokens = index(primitives, base, themes[mode]);
  const hexOf = (p) => resolveHex(tokens, p);
  const taken = {};
  for (const [path, t] of walk(themes[mode])) {
    const floor = t.$extensions?.['dev.hydra.contrast'];
    const term = t.$extensions?.['dev.hydra.terminal'];
    if (!floor && !term && !t.$extensions?.['dev.hydra.leonardo']) continue;
    const h = hexOf(path);
    const row = (report[path] ??= {});
    const onSurfaces = SURFACES.map((s) => wcag(h, hexOf(s)));
    row[mode] = { hex: h, base: onSurfaces[0], worst: Math.min(...onSurfaces), apca: apca(h, hexOf(SURFACES[0])) };
    if (floor?.min && row[mode].worst < floor.min) failures.push(`${mode} ${path} ${h} is ${row[mode].worst.toFixed(2)}:1 on a surface, floor ${floor.min}`);
    if (floor?.apca && row[mode].apca < floor.apca) failures.push(`${mode} ${path} ${h} is APCA Lc ${row[mode].apca.toFixed(0)}, floor ${floor.apca}`);
    if (!term || term.ansi16 === 'default') continue;
    const refs = TERMINAL_BG[mode];
    for (const b of refs) if (floor?.min && wcag(h, b) < floor.min) failures.push(`${mode} ${path} ${h} is ${wcag(h, b).toFixed(2)}:1 on terminal ${b}`);
    const family = (taken[path.split('.').slice(0, 2).join('.')] ??= new Set());
    const pick = nearest256(h, refs, floor?.min, family, t.$extensions['dev.hydra.leonardo']?.scale === 'neutral');
    if (!pick) { failures.push(`${mode} ${path} ${h} has no in-hue ANSI-256 index a sibling has not taken that reaches ${floor?.min ?? 0}:1`); continue; }
    family.add(pick.i);
    term.ansi256 = String(pick.i);
    row[mode].ansi256 = pick;
    row[mode].term = Math.min(...refs.map((b) => wcag(h, b)));
  }
}
for (const [path, r] of Object.entries(report)) if (!r.light || !r.dark) failures.push(`${path} is checked in one theme only; declare it in both`);

// Series identity rides on hue alone, so neighbours must stay apart for full colour
// vision (ΔE ≥ 15) and under protan/deutan (ΔE ≥ 8): the dataviz method's floors,
// OKLab ×100, simulated with Machado et al. 2009 at severity 1, which they assume.
const MACHADO = {
  protan: [[0.152286, 1.052583, -0.204868], [0.114503, 0.786281, 0.099216], [-0.003882, -0.048116, 1.051998]],
  deutan: [[0.367322, 0.860646, -0.227968], [0.280085, 0.672501, 0.047413], [-0.01182, 0.04294, 0.968881]],
};
function lab(h, m) {
  let c = new ColorJS(h).to('srgb-linear').coords;
  if (m) c = m.map((r) => Math.min(1, Math.max(0, r[0] * c[0] + r[1] * c[1] + r[2] * c[2])));
  return new ColorJS('srgb-linear', c).to('oklab').coords;
}
const dE = (a, b, m) => 100 * Math.hypot(...lab(a, m).map((v, i) => v - lab(b, m)[i]));
for (const mode of MODES) {
  const tokens = index(primitives, base, themes[mode]);
  const series = [...walk(themes[mode])].filter(([p]) => p.startsWith('color.chart.series-')).map(([p]) => [p, resolveHex(tokens, p)]);
  for (let i = 1; i < series.length; i++) {
    const [[pa, a], [pb, b]] = [series[i - 1], series[i]];
    if (dE(a, b) < 15) failures.push(`${mode} ${pa} ${a} and ${pb} ${b} are ΔE ${dE(a, b).toFixed(1)} apart, floor 15`);
    const cvd = Math.min(dE(a, b, MACHADO.protan), dE(a, b, MACHADO.deutan));
    if (cvd < 8) failures.push(`${mode} ${pa} ${a} and ${pb} ${b} are ΔE ${cvd.toFixed(1)} apart under protan/deutan, floor 8`);
  }
  // cost.* is a sequential scale: each step further from the page than the last,
  // in lightness steps a reader can see (OKLCH ΔL ≥ 0.06), whichever way the theme runs.
  const L = (h) => new ColorJS(h).to('oklch').coords[0];
  const page = L(resolveHex(tokens, SURFACES[0]));
  const cost = ['low', 'mid', 'high'].map((k) => [`color.cost.${k}`, resolveHex(tokens, `color.cost.${k}`)]);
  for (let i = 1; i < cost.length; i++) {
    const [[pa, a], [pb, b]] = [cost[i - 1], cost[i]];
    const step = Math.abs(page - L(b)) - Math.abs(page - L(a));
    if (step < 0.06) failures.push(`${mode} ${pb} ${b} is only ${step.toFixed(3)} further from the page in OKLCH L than ${pa} ${a}; a sequential step needs 0.06`);
  }
}
if (failures.length) { console.error(failures.join('\n')); process.exit(1); }
for (const mode of MODES) write(`themes/${mode}.tokens.json`, themes[mode]);

const f = (n) => n.toFixed(2);
let md = '| Role | Light | on base / worst surface | APCA Lc | Dark | on base / worst surface | APCA Lc |\n|---|---|---|---|---|---|---|\n';
for (const [path, r] of Object.entries(report)) {
  md += `| \`${path.replace('color.', '')}\` | \`${r.light.hex}\` | ${f(r.light.base)} / ${f(r.light.worst)} | ${r.light.apca.toFixed(0)} | \`${r.dark.hex}\` | ${f(r.dark.base)} / ${f(r.dark.worst)} | ${r.dark.apca.toFixed(0)} |\n`;
}
md += '\nTerminal roles, worst case against the terminal backgrounds above:\n\n| Role | ANSI-16 (light / dark) | ANSI-256 light (ΔE, contrast) | ANSI-256 dark (ΔE, contrast) | Truecolor light / dark |\n|---|---|---|---|---|\n';
for (const [path, r] of Object.entries(report)) {
  const t = (m) => themes[m] && [...walk(themes[m])].find(([p]) => p === path)[1].$extensions?.['dev.hydra.terminal'];
  if (!t('light')) continue;
  if (t('light').ansi16 === 'default') { md += `| \`${path.replace('color.', '')}\` | default fg | default fg | default fg | default fg |\n`; continue; }
  const cell = (m) => `${r[m].ansi256.i} (${r[m].ansi256.d.toFixed(1)}, ${f(Math.min(...TERMINAL_BG[m].map((b) => wcag(r[m].ansi256.x, b))))}:1)`;
  md += `| \`${path.replace('color.', '')}\` | ${t('light').ansi16} / ${t('dark').ansi16} | ${cell('light')} | ${cell('dark')} | ${f(r.light.term)} / ${f(r.dark.term)} |\n`;
}
const designMd = new URL('../../DESIGN.md', import.meta.url);
const START = '<!-- generated:contrast -->', END = '<!-- /generated:contrast -->';
const doc = fs.readFileSync(designMd, 'utf8');
const parts = doc.split(START), tail = parts[1]?.split(END);
if (parts.length !== 2 || tail.length !== 2) throw new Error('DESIGN.md must hold each contrast marker exactly once');
fs.writeFileSync(designMd, `${parts[0]}${START}\n${md}${END}${tail[1]}`);
