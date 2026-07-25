#!/usr/bin/env node
// Downloads the prebuilt `hyctl` binary matching this package's version from
// the GitHub release, verifies its checksum, and extracts it into bin/.
'use strict';

const fs = require('fs');
const os = require('os');
const path = require('path');
const https = require('https');
const crypto = require('crypto');
const { execFileSync } = require('child_process');

const REPO = 'ankit373/hydra';
const PROJECT = 'hydra'; // goreleaser archive filename prefix
const version = require('../package.json').version;
const TAG = `v${version}`;

const PLATFORM = { darwin: 'darwin', linux: 'linux', win32: 'windows' }[process.platform];
const ARCH = { x64: 'amd64', arm64: 'arm64' }[process.arch];

function fail(msg) {
  console.error(`\n  hyctl: ${msg}`);
  console.error(`  Install manually from https://github.com/${REPO}/releases/tag/${TAG}\n`);
  process.exit(1);
}

if (!PLATFORM || !ARCH) {
  fail(`unsupported platform ${process.platform}/${process.arch}`);
}

const ext = PLATFORM === 'windows' ? 'zip' : 'tar.gz';
const archive = `${PROJECT}_${version}_${PLATFORM}_${ARCH}.${ext}`;
const base = `https://github.com/${REPO}/releases/download/${TAG}`;
const binName = PLATFORM === 'windows' ? 'hyctl.exe' : 'hyctl';
const binDir = path.join(__dirname, '..', 'bin');

// Follow redirects (GitHub release assets 302 to a CDN) and buffer the body.
function get(url) {
  return new Promise((resolve, reject) => {
    https
      .get(url, { headers: { 'User-Agent': 'hyctl-npm-installer' } }, (res) => {
        if (res.statusCode >= 300 && res.statusCode < 400 && res.headers.location) {
          res.resume();
          resolve(get(res.headers.location));
          return;
        }
        if (res.statusCode !== 200) {
          res.resume();
          reject(new Error(`HTTP ${res.statusCode} for ${url}`));
          return;
        }
        const chunks = [];
        res.on('data', (c) => chunks.push(c));
        res.on('end', () => resolve(Buffer.concat(chunks)));
      })
      .on('error', reject);
  });
}

async function main() {
  console.log(`hyctl: downloading ${archive} (${TAG})…`);

  let archiveBuf;
  try {
    archiveBuf = await get(`${base}/${archive}`);
  } catch (e) {
    fail(`download failed — ${e.message}`);
  }

  // Verify against checksums.txt (skip only if the file is absent).
  try {
    const sums = (await get(`${base}/checksums.txt`)).toString('utf8');
    const line = sums.split('\n').find((l) => l.trim().endsWith(archive));
    if (line) {
      const expected = line.trim().split(/\s+/)[0];
      const actual = crypto.createHash('sha256').update(archiveBuf).digest('hex');
      if (expected !== actual) fail(`checksum mismatch (expected ${expected}, got ${actual})`);
      console.log('hyctl: checksum verified');
    }
  } catch {
    console.warn('hyctl: checksums.txt unavailable — skipping verification');
  }

  // Extract via the system `tar` (handles .tar.gz on unix and .zip on Win10+).
  fs.mkdirSync(binDir, { recursive: true });
  const tmp = fs.mkdtempSync(path.join(os.tmpdir(), 'hyctl-'));
  const archivePath = path.join(tmp, archive);
  fs.writeFileSync(archivePath, archiveBuf);

  try {
    execFileSync('tar', ['-xf', archivePath, '-C', tmp, binName], { stdio: 'ignore' });
  } catch (e) {
    fail(`extraction failed — ${e.message}`);
  }

  const dest = path.join(binDir, binName);
  fs.copyFileSync(path.join(tmp, binName), dest);
  if (PLATFORM !== 'windows') fs.chmodSync(dest, 0o755);
  fs.rmSync(tmp, { recursive: true, force: true });

  console.log(`hyctl: installed ${binName} ${TAG}`);
}

main().catch((e) => fail(e.message));
