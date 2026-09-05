#!/usr/bin/env node
// Thin launcher: exec the platform `hyctl` binary that postinstall placed
// alongside this file, forwarding args, stdio, and exit code.
'use strict';

const path = require('path');
const { spawnSync } = require('child_process');

const binName = process.platform === 'win32' ? 'hyctl.exe' : 'hyctl';
const bin = path.join(__dirname, binName);

const result = spawnSync(bin, process.argv.slice(2), { stdio: 'inherit' });

if (result.error) {
  if (result.error.code === 'ENOENT') {
    console.error(
      'hyctl: binary not found, reinstall with `npm rebuild hyctl` or install from ' +
        'https://github.com/ankit373/hydra/releases'
    );
  } else {
    console.error(`hyctl: ${result.error.message}`);
  }
  process.exit(1);
}

process.exit(result.status === null ? 1 : result.status);
