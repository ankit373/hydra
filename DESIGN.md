# Hydra design system

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
| `accent.default` | `#007867` | 5.10 / 4.52 | 72 | `#01e6c4` | 12.55 / 11.60 | 77 |
| `accent.strong` | `#006053` | 7.07 / 6.27 | 81 | `#2af0e0` | 14.01 / 12.94 | 83 |
| `accent.secondary` | `#764ddd` | 5.11 / 4.53 | 72 | `#a68bfb` | 7.38 / 6.81 | 49 |
| `status.success` | `#187947` | 5.12 / 4.54 | 73 | `#3fd98a` | 11.01 / 10.17 | 69 |
| `status.warning` | `#8e6112` | 5.11 / 4.53 | 73 | `#e0a93b` | 9.48 / 8.76 | 61 |
| `status.danger` | `#c13646` | 5.09 / 4.52 | 72 | `#ff5a6e` | 6.64 / 6.14 | 46 |
| `status.info` | `#2d6ab6` | 5.14 / 4.56 | 73 | `#59a7fd` | 8.01 / 7.40 | 53 |
| `brand.start` | `#00707a` | 5.50 / 4.87 | 75 | `#2af0e0` | 14.01 / 12.94 | 83 |
| `brand.mid` | `#6d28d9` | 6.69 / 5.93 | 79 | `#8b5cf6` | 4.75 / 4.38 | 33 |
| `brand.end` | `#a21caf` | 5.96 / 5.28 | 76 | `#e852c8` | 6.19 / 5.72 | 43 |

Terminal roles, worst case against the terminal backgrounds above:

| Role | ANSI-16 (light / dark) | ANSI-256 light (ΔE, contrast) | ANSI-256 dark (ΔE, contrast) | Truecolor light / dark |
|---|---|---|---|---|
| `border.default` | 8 / 8 | 146 (4.9, 1.96:1) | 236 (12.8, 1.26:1) | 1.77 / 1.27 |
| `text.default` | default fg | default fg | default fg | default fg |
| `text.muted` | 8 / 7 | 60 (4.6, 5.61:1) | 146 (3.7, 7.89:1) | 6.69 / 7.96 |
| `text.subtle` | 8 / 8 | 60 (3.2, 5.61:1) | 103 (2.4, 4.85:1) | 5.06 / 4.82 |
| `accent.default` | 6 / 6 | 23 (10.6, 6.95:1) | 43 (4.1, 9.00:1) | 5.01 / 10.41 |
| `accent.strong` | 6 / 6 | 23 (5.7, 6.95:1) | 51 (5.9, 13.30:1) | 6.96 / 11.62 |
| `accent.secondary` | 5 / 5 | 62 (7.5, 4.74:1) | 141 (2.5, 6.14:1) | 5.02 / 6.12 |
| `status.success` | 2 / 2 | 22 (12.4, 7.38:1) | 42 (1.8, 8.79:1) | 5.04 / 9.13 |
| `status.warning` | 3 / 3 | 94 (2.7, 5.31:1) | 179 (4.9, 8.09:1) | 5.03 / 7.87 |
| `status.danger` | 1 / 1 | 161 (9.1, 4.80:1) | 203 (5.0, 5.60:1) | 5.01 / 5.51 |
| `brand.start` | 6 / 6 | 23 (7.1, 6.95:1) | 51 (5.9, 13.30:1) | 5.41 / 11.62 |
| `brand.mid` | 5 / 5 | 56 (4.6, 7.89:1) | 99 (1.9, 4.06:1) | 6.59 / 3.94 |
| `brand.end` | 5 / 5 | 127 (2.5, 5.66:1) | 170 (4.9, 5.17:1) | 5.86 / 5.14 |
<!-- /generated:contrast -->
