import { defineConfig } from '@terrazzo/cli';
import css from '@terrazzo/plugin-css';

// Light is the :root default; dark applies under the OS preference unless a page
// pins light, and always under data-theme="dark" (web and desktop pin it today).
const dark = (c) =>
  `@media (prefers-color-scheme: dark) {\n  :root:not([data-theme="light"]) {\n    ${c}\n  }\n}\n\n` +
  `:root[data-theme="dark"] {\n  ${c.replace(/\n {4}/g, '\n  ')}\n}\n`;

export default defineConfig({
  tokens: ['./tokens/hydra.resolver.json'],
  outDir: './dist/',
  plugins: [
    css({
      filename: 'tokens.css',
      legacyHex: true,
      // Leonardo key colours are inputs to the generator, not styles.
      exclude: ['neutral', 'aqua', 'violet', 'green', 'amber', 'red', 'blue'].map((s) => `palette.${s}.**`),
      variableName: (token) => `--hy-${token.id.replaceAll('.', '-')}`,
      permutations: [
        { input: { theme: 'light' }, prepare: (c) => `:root {\n  ${c}\n}\n` },
        { input: { theme: 'dark' }, prepare: dark, include: ['color.**'] },
      ],
    }),
  ],
});
