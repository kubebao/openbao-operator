'use strict';

const assert = require('node:assert/strict');
const {createRequire} = require('node:module');
const {RepositoryFileCache} = require('@google-automations/git-file-utils');

async function main() {
  const paths = [
    'package.json',
    'charts/openbao-operator/Chart.yaml',
    'charts/openbao-operator/values.yaml',
    '.github/workflows/release.yml',
    'services/worker/package.json',
    'services/worker/src/main.js',
    'services/api/package.json',
    'README.md',
  ];
  const tree = paths.map(path => ({path, type: 'blob', mode: '100644'}));
  tree.push({path: 'services/submodule/package.json', type: 'commit', mode: '160000'});
  let requests = 0;
  const octokit = {
    git: {
      getTree: async request => {
        assert.equal(request.owner, 'fixture');
        assert.equal(request.repo, 'fixture');
        assert.equal(request.tree_sha, 'main');
        requests++;
        return {data: {tree, truncated: false}};
      },
    },
  };
  const cache = new RepositoryFileCache(octokit, {owner: 'fixture', repo: 'fixture'});
  const cases = [
    ['*.json', undefined, ['package.json']],
    ['**/package.json', undefined, [
      'package.json', 'services/worker/package.json', 'services/api/package.json',
    ]],
    ['charts/**/*.{yaml,yml}', undefined, [
      'charts/openbao-operator/Chart.yaml', 'charts/openbao-operator/values.yaml',
    ]],
    ['.github/workflows/*.yml', undefined, ['.github/workflows/release.yml']],
    ['services/+(worker|api)/package.json', undefined, [
      'services/worker/package.json', 'services/api/package.json',
    ]],
    ['services/worker/**', 'services/worker', ['package.json', 'src/main.js']],
    ['missing/**/*.json', undefined, []],
  ];
  for (const [glob, prefix, expected] of cases) {
    assert.deepEqual(await cache.findFilesByGlob(glob, 'main', prefix), expected, glob);
  }
  assert.equal(requests, 1, 'the fixture tree must be cached');
  const consumerRequire = createRequire(require.resolve('@google-automations/git-file-utils'));
  const version = consumerRequire('minimatch/package.json').version;
  console.log(`release-please file discovery passed ${cases.length} cases with minimatch ${version}`);
}

main().catch(error => {
  console.error(error);
  process.exitCode = 1;
});
