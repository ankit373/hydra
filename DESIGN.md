# Hydra design system

One token source styles all three surfaces: the website (`docs/`), the desktop app (`desktop/frontend`) and the TUI (`internal/tui`). This file is the reference for what the tokens are, which role to reach for, and how they reach a terminal. The contrast tables below are generated; everything else is maintained by hand.

## Principles: a calm instrument

Hydra is a tool people trust with real work, so it should read like a well-made instrument: quiet, legible and exact.

1. **Say what it does before showing how clever it is.** Every first screen states what Hydra is and the next action in plain words.
2. **One system, three renderers.** A single DTCG source generates the CSS for the web and the desktop app and the Go code for the TUI. No surface defines a colour, size or duration outside the generated tokens.
3. **Legible first, branded second.** WCAG 2 AA is the floor for every text role on every surface, in both themes and at every terminal colour depth. Brand lives in the logo, one accent and illustrations, never in body-text colour.
4. **Meaning never rides on colour alone.** A status is a colour plus a glyph or a word (`✓ checked`, `! warning`, `✗ failed`). That serves NO_COLOR, colour-blind and screen-reader users alike.
5. **Light and dark are equals.** Each role is targeted against its own theme's background; neither theme is derived from the other.
6. **Respect the environment.** The TUI uses the terminal's own foreground for body text and never paints a background. The website and the desktop app follow the system theme unless the viewer picks one, and honour reduced motion.

## How it fits together

```
design/tokens/
  primitives.tokens.json      palette.*: Leonardo key colours per hue scale, plus the logo stops
  base.tokens.json            font, space, radius, motion, focus (theme-independent)
  themes/light.tokens.json    semantic color.* roles for light
  themes/dark.tokens.json     semantic color.* roles for dark
  hydra.resolver.json         DTCG resolver: base set + theme modifier {light, dark}
design/scripts/palette.mjs    Leonardo: fills each role from its scale and target ratio, checks floors, picks ANSI-256
design/terrazzo.config.mjs    Terrazzo 2: one CSS file of custom properties
design/scripts/emit.mjs       copies the CSS to its consumers and writes the lipgloss roles
```

| Output | Consumer |
|---|---|
| `docs/assets/tokens.css` | every website page links it, then `docs/assets/site.css`, the one shared stylesheet; canvas and SVG drawing read the same roles through `Hydra.color()` in `docs/assets/site.js` |
| `desktop/frontend/src/tokens.generated.css` | imported by `desktop/frontend/src/tokens.css` |
| `internal/tui/tokens_gen.go` | `internal/tui/styles.go` and the terminal mark in `splash.go`; padding in cells and the spinner's frame time |
| `desktop/window_gen.go` | the desktop app's native window background for each theme, painted before the first frame |

Light values sit on `:root`. Dark values apply under `@media (prefers-color-scheme: dark)` scoped to `:root:not([data-theme="light"])`, and always under `:root[data-theme="dark"]`.

### Choosing a theme

Both surfaces follow the OS until the viewer picks a theme. A header button cycles system, light and dark; the choice is saved per browser (or per app install) under the `hydra-theme` key in `localStorage`, and every read and write is wrapped so a refused storage leaves the page following the choice for the session.

- **No flash.** A one-line inline script in each page's `<head>`, before any stylesheet, sets `data-theme` from the saved choice. `cmd/hydra/docs_theme_test.go` fails on a page without it, with it after a stylesheet, or with a pinned `data-theme`; `ThemeToggle.test.tsx` checks the desktop entry pages the same way.
- **Canvas and SVG.** `Hydra.color()` drops its cache and `Hydra.loop()` redraws on any theme change, from the OS or the button. A page that keeps colours of its own refreshes them in `Hydra.onTheme(fn)`. SVG built by script should take `var(--hy-color-…)` in its style so it follows without script.
- **Effects that depend on direction.** Additive blending, white highlights, coloured glows and drop shadows read one way on dark and another on light. The token `theme.dark` (`--hy-theme-dark`, 0 in light and 1 in dark) is the switch: each such value is one rule through `calc()` or `color-mix()`, never a block copied into both dark scopes. The desktop app's `--hy-scrim`, `--hy-shadow`, `--hy-vignette` and `--hy-glow` (every glow is scaled by it) live in `tokens.css`; the site's `--site-shadow` in `site.css`; canvases ask `Hydra.dark()`.
- **The native window.** `desktop/window_gen.go` carries both themes' `surface.base`; the app paints the one matching the OS at launch, and the page repaints it whenever the theme in force changes.

**To change a token:** edit the JSON under `design/tokens/`, then run `cd design && npm ci && npm run build` and commit the source and every generated file together. The build fails if any role misses its declared contrast floor. The `Design tokens up to date` CI job rebuilds from a clean install and fails if any generated file differs from what is committed.

### How a colour is made

Each semantic colour role carries its recipe in `$extensions`:

```json
"muted": {
  "$value": { "colorSpace": "srgb", "components": [0.6745, 0.698, 0.8157], "hex": "#acb2d0" },
  "$extensions": {
    "dev.hydra.leonardo": { "scale": "neutral", "ratio": 9.6 },
    "dev.hydra.terminal": { "ansi16": 7, "ansi256": "146" },
    "dev.hydra.contrast": { "min": 4.5, "apca": 60 }
  }
}
```

- `dev.hydra.leonardo`: the role is generated by [Leonardo](https://github.com/adobe/leonardo) from the `palette.<scale>` key colours, at a WCAG 2 `ratio` against the theme's `surface.base`. The theme file's root extension sets the background scale and lightness. `$value` is written by the build; never edit it by hand.
- `dev.hydra.contrast`: the floor this role must meet against **every** surface in its theme (WCAG 2 `min`), and optionally an APCA Lc floor against the base surface. Terminal roles must also meet `min` on the terminal backgrounds below.
- `dev.hydra.terminal`: the role reaches the TUI. `ansi16` is chosen by hand (it inherits the user's terminal palette); `ansi256` is chosen by the build.

Dark targets reproduce the colours that shipped before Phase 2, so the dark theme looks the same; only `text.muted`, `text.subtle` and `accent.secondary` were raised, because they failed AA. Light targets are new.

## Colour roles and usage

Names are purpose, never hue. CSS variables are `--hy-` plus the token path with dots as hyphens (`color.text.muted` is `--hy-color-text-muted`); Go names are `tok` plus the path in camel case (`tokTextMuted`).

| Role | Use for | Never for |
|---|---|---|
| `surface.base` | the page or window background | |
| `surface.raised` | sidebars, headers, table heads | |
| `surface.card` | cards and panels | stacking a card on a card |
| `surface.overlay` | popovers, input wells, selected rows: the highest surface | |
| `border.subtle` | hairlines and dividers | the only boundary of a control |
| `border.default` | card and control outlines | |
| `border.strong` | input and control borders that must read at 3:1 (WCAG 1.4.11) | |
| `text.default` | body text (the terminal's own foreground in the TUI) | |
| `text.muted` | secondary text and labels | |
| `text.subtle` | metadata and hints | anything the user must read to act |
| `text.inverse` | text on a saturated fill (an accent or status background) | text on a surface |
| `accent.default` | links, primary actions, live values, selection, the focus ring | decoration |
| `accent.strong` | emphasis in a second, bluer teal: headings in data views, inline code | actions (use `accent.default`) |
| `accent.secondary` | tags and series that need a second hue | actions |
| `status.success` / `warning` / `danger` / `info` | the state of something, always with a glyph or word | cost, or a chart series |
| `cost.low` / `mid` / `high` | price and spend bands, which are data | states |
| `chart.series-1` … `series-8` | categorical series, in this fixed order, never cycled | text |
| `focus.ring` | the 2px focus ring at a 2px offset | |
| `brand.start` / `mid` / `end` | the logo mark only | anything else, the wordmark included: it is plain text |

`cost.*` is a sequential scale on one hue (blue), cheap to expensive: each step sits further from the page than the last, by at least 0.06 in OKLCH lightness, so it runs light to dark on the light theme and dark to light on the dark one. Every step is text-safe (4.5:1 on every surface), because prices are printed in it. It is for price and spend only; a pass, a fail or a diff line is a state and takes `status.*`.

The chart series follow the dataviz reference order (blue, orange, aqua, yellow, magenta, green, violet, red). Every series reaches 3:1 on every surface in both themes, and neighbours stay apart for full colour vision (OKLab ΔE ≥ 15) and under protanopia and deuteranopia (ΔE ≥ 8, Machado 2009 simulation). The build checks all three and fails on a miss. The light steps of series 2 to 5 were darkened and nudged in hue to get there (the reference values sat at 1.8 to 2.7:1 on the light surfaces); dark is the reference set unchanged.

The values and their contrast are in the generated table under **Contrast results**.

## Type, space, radius, motion

These live in `base.tokens.json` and are the same in both themes. The website and the desktop app read every type size, space, radius and duration from them; `TestDocs_TypeSizesComeFromTheScale` and `TestAppCSS_SizeSpaceAndMotionComeFromTokens` in `cmd/hydra` fail on a literal. Widths and heights stay literal, since they are the size of a thing and not a step on a scale.

| Group | Tokens |
|---|---|
| `font.family` | `text` system-ui stack; `mono` JetBrains Mono, then ui-monospace (code, commands, IDs and numeric columns only); `display` is an open decision between Space Grotesk and system-ui |
| `font.size` | `xs` 12, `sm` 13, `md` 14, `base` 16, `lg` 18, `xl` 22, `2xl` 28, `3xl` 36, `4xl` 48 px. 12px is the floor for any text and 13px for interactive labels |
| `font.weight` | `regular` 400, `medium` 500, `semibold` 600, `bold` 700 |
| `space` | `0-5` 2, `1` 4, `1-5` 6, `2` 8, `2-5` 10, `3` 12, `4` 16, `5` 20, `6` 24, `8` 32, `12` 48, `16` 64, `24` 96, `32` 128 px. The half steps are for dense rows. A 1px hairline is a line, not a step, and stays literal. The TUI pads 0, 1 cell (`tokSpace2`) or 2 (`tokSpace4`) |
| `radius` | `mark` 2 (bars and swatches), `control` 6, `card` 10, `pill` 999 px (pills, dots, switches) |
| `motion.duration` | `fast` 120, `base` 200, `slow` 320 ms, and `loop` 1600 ms for one cycle of a spinner or shimmer; non-essential motion drops to 0 under reduced motion. The TUI spinner steps every `base` |
| `motion.easing` | `standard` cubic-bezier(.2, 0, 0, 1), `emphasized` cubic-bezier(.3, 0, 0, 1). `linear` and `steps()` are not curves and stay keywords |
| `focus` | `width` 2px, `offset` 2px |

## Terminal mapping

The TUI never paints a background, so it cannot know the exact colour behind its text. Terminal roles are therefore checked against three common terminal backgrounds: `#1e1e1e` for dark, and `#ffffff` and Solarized Light `#fdf6e3` for light. lipgloss picks the light or dark value from background detection, overridable with `HYDRA_THEME=light|dark`.

- **Truecolor:** the token's own value.
- **ANSI-256:** the closest colour by ΔE2000 *among those that meet the role's floor on every terminal background* and stay within 40° of the role's OKLCH hue (neutral roles may take a grey). Plain nearest-match is what turned body text unreadable at 256 colours (TUI-03). Roles in one family (`text.*`, `accent.*`) take different indices, so the hierarchy survives at 256 colours; if no in-hue index is left, the build fails rather than share one or change hue.
- **ANSI-16:** a fixed slot per role, so the user's own terminal palette applies.
- **Body text** has no colour at all and uses the terminal's default foreground.
- **NO_COLOR and selection:** selection and focus use reverse video plus a glyph (`[name]` for the active tab, a heavy border for the focused pane), so nothing is told apart by colour alone.

| Role | ANSI-16 | Meaning without colour |
|---|---|---|
| `text.default` | terminal default foreground | n/a |
| `text.muted` | 8 light / 7 dark | n/a |
| `text.subtle` | 8 | decoration only |
| `accent.default`, `accent.strong` | 6 (cyan) | bold for the active item |
| `accent.secondary` | 5 (magenta) | n/a |
| `status.success` / `warning` / `danger` | 2 / 3 / 1 | `✓` / `!` / `✗` |
| `border.default` | 8 | box-drawing characters |
| `brand.*` | 6 / 5 / 5 | the mark's shape |

## Contrast results

WCAG 2 ratios are against `surface.base`, and against the lowest-contrast surface in the same theme ("worst surface"). APCA Lc is against `surface.base`; body text (`text.default`, `text.muted`) must reach Lc 60. Every role with a `min` floor meets it on every surface, and the terminal roles meet it on every terminal background at truecolor and ANSI-256; `TestPalette_TextRolesMeetAAOnLightAndDark` in `internal/tui` checks the Go side again.

<!-- generated:contrast -->
| Role | Light | on base / worst surface | APCA Lc | Dark | on base / worst surface | APCA Lc |
|---|---|---|---|---|---|---|
| `surface.base` | `#f7f8fc` | 1.00 / 1.00 | 0 | `#06070f` | 1.00 / 1.00 | 0 |
| `surface.raised` | `#f4f5fb` | 1.03 / 1.00 | 0 | `#0a0c18` | 1.03 / 1.00 | 0 |
| `surface.card` | `#eef0f8` | 1.07 / 1.00 | 0 | `#0c0f1e` | 1.06 / 1.00 | 0 |
| `surface.overlay` | `#e9eaf4` | 1.13 / 1.00 | 0 | `#0f1223` | 1.08 / 1.00 | 0 |
| `border.subtle` | `#d4d7e8` | 1.35 / 1.19 | 17 | `#1a1e32` | 1.22 / 1.13 | 0 |
| `border.default` | `#b6bad5` | 1.80 / 1.60 | 32 | `#2b2f49` | 1.54 / 1.42 | 0 |
| `border.strong` | `#7e83a9` | 3.47 / 3.07 | 60 | `#60668b` | 3.61 / 3.33 | 24 |
| `text.default` | `#1d2036` | 15.08 / 13.37 | 99 | `#e7e9f4` | 16.61 / 15.35 | 94 |
| `text.muted` | `#505578` | 6.80 / 6.02 | 81 | `#acb2d0` | 9.60 / 8.87 | 61 |
| `text.subtle` | `#62678d` | 5.15 / 4.56 | 73 | `#8288ad` | 5.81 / 5.37 | 39 |
| `accent.default` | `#007867` | 5.10 / 4.52 | 72 | `#00e6c3` | 12.54 / 11.59 | 77 |
| `accent.strong` | `#006e78` | 5.65 / 5.01 | 75 | `#2af0e0` | 14.01 / 12.94 | 83 |
| `accent.secondary` | `#764ddd` | 5.11 / 4.53 | 72 | `#a68bfb` | 7.38 / 6.81 | 49 |
| `status.success` | `#187947` | 5.12 / 4.54 | 73 | `#3fd98a` | 11.01 / 10.17 | 69 |
| `status.warning` | `#8e6112` | 5.11 / 4.53 | 73 | `#e0a93b` | 9.48 / 8.76 | 61 |
| `status.danger` | `#c13646` | 5.09 / 4.52 | 72 | `#ff5a6e` | 6.64 / 6.14 | 46 |
| `status.info` | `#2d6ab6` | 5.14 / 4.56 | 73 | `#59a7fd` | 8.01 / 7.40 | 53 |
| `cost.low` | `#2d69b5` | 5.21 / 4.62 | 73 | `#3f83d3` | 5.17 / 4.77 | 36 |
| `cost.mid` | `#174c91` | 7.98 / 7.08 | 85 | `#59a7fd` | 8.01 / 7.40 | 53 |
| `cost.high` | `#002f71` | 12.03 / 10.66 | 94 | `#a5d0ff` | 12.50 / 11.55 | 76 |
| `chart.series-1` | `#2a78d6` | 4.16 / 3.69 | 66 | `#3987e5` | 5.52 / 5.10 | 38 |
| `chart.series-2` | `#e35b2d` | 3.41 / 3.02 | 59 | `#d95926` | 5.17 / 4.78 | 36 |
| `chart.series-3` | `#029876` | 3.44 / 3.05 | 59 | `#199e70` | 5.90 / 5.45 | 41 |
| `chart.series-4` | `#ba7916` | 3.39 / 3.01 | 59 | `#c98500` | 6.54 / 6.05 | 44 |
| `chart.series-5` | `#d85a98` | 3.39 / 3.01 | 59 | `#d55181` | 5.09 / 4.71 | 36 |
| `chart.series-6` | `#008300` | 4.66 / 4.13 | 69 | `#008300` | 4.06 / 3.75 | 28 |
| `chart.series-7` | `#6250d6` | 5.44 / 4.82 | 74 | `#9085e9` | 6.43 / 5.94 | 43 |
| `chart.series-8` | `#e34948` | 3.72 / 3.30 | 62 | `#e66767` | 6.22 / 5.75 | 43 |
| `brand.start` | `#00707a` | 5.50 / 4.87 | 75 | `#2af0e0` | 14.01 / 12.94 | 83 |
| `brand.mid` | `#6d28d9` | 6.69 / 5.93 | 79 | `#8b5cf6` | 4.75 / 4.38 | 33 |
| `brand.end` | `#a21caf` | 5.96 / 5.28 | 76 | `#e852c8` | 6.19 / 5.72 | 43 |

Terminal roles, worst case against the terminal backgrounds above:

| Role | ANSI-16 (light / dark) | ANSI-256 light (ΔE, contrast) | ANSI-256 dark (ΔE, contrast) | Truecolor light / dark |
|---|---|---|---|---|
| `border.default` | 8 / 8 | 146 (4.9, 1.96:1) | 236 (12.8, 1.26:1) | 1.77 / 1.27 |
| `text.default` | default fg | default fg | default fg | default fg |
| `text.muted` | 8 / 7 | 60 (4.6, 5.61:1) | 146 (3.7, 7.89:1) | 6.69 / 7.96 |
| `text.subtle` | 8 / 8 | 61 (7.5, 5.20:1) | 103 (2.4, 4.85:1) | 5.06 / 4.82 |
| `accent.default` | 6 / 6 | 23 (10.6, 6.95:1) | 43 (4.0, 9.00:1) | 5.01 / 10.40 |
| `accent.strong` | 6 / 6 | 24 (12.5, 6.52:1) | 51 (5.9, 13.30:1) | 5.56 / 11.62 |
| `accent.secondary` | 5 / 5 | 62 (7.5, 4.74:1) | 141 (2.5, 6.14:1) | 5.02 / 6.12 |
| `status.success` | 2 / 2 | 22 (12.4, 7.38:1) | 42 (1.8, 8.79:1) | 5.04 / 9.13 |
| `status.warning` | 3 / 3 | 94 (2.7, 5.31:1) | 179 (4.9, 8.09:1) | 5.03 / 7.87 |
| `status.danger` | 1 / 1 | 161 (9.1, 4.80:1) | 203 (5.0, 5.60:1) | 5.01 / 5.51 |
| `brand.start` | 6 / 6 | 23 (7.1, 6.95:1) | 51 (5.9, 13.30:1) | 5.41 / 11.62 |
| `brand.mid` | 5 / 5 | 56 (4.6, 7.89:1) | 99 (1.9, 4.06:1) | 6.59 / 3.94 |
| `brand.end` | 5 / 5 | 127 (2.5, 5.66:1) | 170 (4.9, 5.17:1) | 5.86 / 5.14 |
<!-- /generated:contrast -->

## Not yet on the tokens

Each of these is scheduled in the design revamp and listed so nobody mistakes it for the pattern to copy:

- **Website radii, widths and display sizes (Phase 7).** Colour, type size, spacing and motion on the site come from tokens and a check fails on a colour literal; corner radii, container widths, 1–2px hairline nudges and the hero display sizes above the 48px top of the scale (fluid, inside `clamp()`) are still literal.
- **The desktop logo SVG.** `brand.tsx` keeps its gradient stops, whose pale highlights were drawn for a dark ground.
- **TUI cost colours (Phase 8).** The TUI's cost bands still use the status hues: `cost.*` has no terminal mapping, and a sequential scale needs more than the ANSI-16 slots give.
- **TUI wizards and CLI colours (Phase 8).** `init.go`, `install.go` and `cmd/hydra` still use their own ANSI-256 values.
