#!/usr/bin/env node

'use strict';

const fs = require('node:fs');
const path = require('node:path');

const PINNED_RELEASE_PLEASE_VERSION = '17.11.2';
const releasePleaseRoot = path.join(
  __dirname,
  'release-please',
  'node_modules',
  'release-please'
);

function requireEnvironment(name) {
  const value = process.env[name];
  if (!value) {
    throw new Error(`${name} is required`);
  }
  return value;
}

async function permitExactEmptyChangelog(manifest, releaseAs) {
  if (typeof manifest.getStrategiesByPath !== 'function') {
    throw new Error('release-please strategy access is unavailable');
  }

  const strategiesByPath = await manifest.getStrategiesByPath();
  const paths = Object.keys(strategiesByPath);
  if (paths.length !== 1 || paths[0] !== '.') {
    throw new Error(
      `exact empty-changelog promotion requires one root package, found: ${paths.join(', ')}`
    );
  }

  const strategy = strategiesByPath['.'];
  if (typeof strategy.changelogEmpty !== 'function') {
    throw new Error('release-please changelog emptiness check is unavailable');
  }

  const changelogEmpty = strategy.changelogEmpty.bind(strategy);
  strategy.changelogEmpty = changelogEntry => {
    if (!changelogEmpty(changelogEntry)) {
      return false;
    }

    const nonEmptyLines = changelogEntry
      .split('\n')
      .map(line => line.trim())
      .filter(Boolean);
    if (
      nonEmptyLines.length !== 1 ||
      !nonEmptyLines[0].startsWith('## ') ||
      !nonEmptyLines[0].includes(releaseAs)
    ) {
      throw new Error(
        `refusing unexpected empty changelog output for ${releaseAs}: ${JSON.stringify(changelogEntry)}`
      );
    }

    console.log(
      `Allowing exact ${releaseAs} promotion with a one-line changelog section; ` +
        'the stable release rollup must be reviewed before merge.'
    );
    return false;
  };
}

async function selfTest() {
  const strategy = {
    changelogEmpty: entry => entry.split('\n').length <= 1,
  };
  const manifest = {
    getStrategiesByPath: async () => ({'.': strategy}),
  };

  await permitExactEmptyChangelog(manifest, '0.5.0');
  if (strategy.changelogEmpty('## [0.5.0](https://example.test/0.5.0)') !== false) {
    throw new Error('exact empty changelog was not permitted');
  }
  if (strategy.changelogEmpty('## 0.5.0\n\n### Bug Fixes\n\n* fixed') !== false) {
    throw new Error('non-empty changelog behavior changed');
  }

  let rejected = false;
  try {
    strategy.changelogEmpty('');
  } catch (_error) {
    rejected = true;
  }
  if (!rejected) {
    throw new Error('unexpected empty changelog output was accepted');
  }

  console.log('exact release-please promotion self-test passed');
}

async function main() {
  if (process.argv.includes('--self-test')) {
    await selfTest();
    return;
  }

  const releasePleasePackage = require(path.join(releasePleaseRoot, 'package.json'));
  if (releasePleasePackage.version !== PINNED_RELEASE_PLEASE_VERSION) {
    throw new Error(
      `release-please version ${releasePleasePackage.version} does not match ` +
        `pinned compatibility version ${PINNED_RELEASE_PLEASE_VERSION}`
    );
  }

  const releaseAs = requireEnvironment('RELEASE_AS');
  const repository = requireEnvironment('REPOSITORY');
  const targetBranch = requireEnvironment('TARGET_BRANCH');
  const tokenFile = requireEnvironment('RELEASE_PLEASE_TOKEN_FILE');
  const repositoryParts = repository.split('/');
  if (repositoryParts.length !== 2 || repositoryParts.some(part => !part)) {
    throw new Error(`REPOSITORY must be owner/name, got ${repository}`);
  }

  const token = fs.readFileSync(tokenFile, 'utf8').trim();
  if (!token) {
    throw new Error('release-please token file is empty');
  }

  const {GitHub, Manifest} = require(releasePleaseRoot);
  const github = await GitHub.create({
    owner: repositoryParts[0],
    repo: repositoryParts[1],
    defaultBranch: targetBranch,
    token,
  });
  const manifest = await Manifest.fromManifest(
    github,
    targetBranch,
    'release-please-config.json',
    '.release-please-manifest.json',
    {},
    undefined,
    releaseAs
  );

  await permitExactEmptyChangelog(manifest, releaseAs);
  const pullRequests = await manifest.createPullRequests();
  if (pullRequests.length !== 1 || !pullRequests[0]) {
    throw new Error(
      `expected one exact-version release PR for ${releaseAs}, created ${pullRequests.length}`
    );
  }

  console.log(
    `Created or updated exact-version release PR #${pullRequests[0].number} for ${releaseAs}.`
  );
}

main().catch(error => {
  console.error(error instanceof Error ? error.stack : error);
  process.exit(1);
});
