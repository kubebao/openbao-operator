# Changelog

Release notes are generated and maintained via **release-please** based on **Conventional Commits**.

## [0.5.2](https://github.com/kubebao/openbao-operator/compare/0.5.1...0.5.2) (2026-10-13)


### Bug Fixes

* **release:** backport compatible fixes for 0.5.2 ([#809](https://github.com/kubebao/openbao-operator/issues/809)) ([0dd7035](https://github.com/kubebao/openbao-operator/commit/0dd70358de5ced96cf9cae95ba4cf4fd4ad7a1cd))

## [0.5.1](https://github.com/dc-tec/openbao-operator/compare/0.5.0...0.5.1) (2026-09-23)


### Features

* **compat:** qualify OpenBao 2.7 external seal plugins ([#762](https://github.com/dc-tec/openbao-operator/issues/762)) ([94b18f2](https://github.com/dc-tec/openbao-operator/commit/94b18f2e3a2dde49550adad43d095220d4dac945))


### Bug Fixes

* **api:** clarify missing TLS rotation period validation ([#751](https://github.com/dc-tec/openbao-operator/issues/751)) ([017a5bd](https://github.com/dc-tec/openbao-operator/commit/017a5bd7276ac7405a8a1ba4b994a436cb889ce2))
* **backup:** abort partial uploads when the snapshot stream fails ([#759](https://github.com/dc-tec/openbao-operator/issues/759)) ([6d02c45](https://github.com/dc-tec/openbao-operator/commit/6d02c45851b0d26fa027cd7060a9d7a26e684551))
* **backup:** keep the newest snapshot when applying retention ([#757](https://github.com/dc-tec/openbao-operator/issues/757)) ([6668b31](https://github.com/dc-tec/openbao-operator/commit/6668b3194d8224ffbda4749009b1106c8a20aa7d))
* **backup:** limit retention to scheduled backup keys ([#758](https://github.com/dc-tec/openbao-operator/issues/758)) ([7a86183](https://github.com/dc-tec/openbao-operator/commit/7a86183398f584c8504865fbf454051d56aea3ba))
* **backup:** preserve trigger before job creation ([#703](https://github.com/dc-tec/openbao-operator/issues/703)) ([ee44f8e](https://github.com/dc-tec/openbao-operator/commit/ee44f8ec8be8faae088cbfd4a7a4a5dc704d54e4))
* **metrics:** clear finalized reconciliation series ([#721](https://github.com/dc-tec/openbao-operator/issues/721)) ([fc15f5e](https://github.com/dc-tec/openbao-operator/commit/fc15f5e94abdff12c0cbbea3ae7975e65067b806))
* **metrics:** keep cluster phase gauges one-hot ([#716](https://github.com/dc-tec/openbao-operator/issues/716)) ([f4c2e1a](https://github.com/dc-tec/openbao-operator/commit/f4c2e1a132c708cf753f5bb54a5e28b2cc20441c))
* **readiness:** require synchronized caches and fresh admission checks ([#720](https://github.com/dc-tec/openbao-operator/issues/720)) ([4764ab9](https://github.com/dc-tec/openbao-operator/commit/4764ab9c65e5e752d5a43901cb61583c5d34a85c))
* **startup:** validate platform discovery and return errors ([#717](https://github.com/dc-tec/openbao-operator/issues/717)) ([4197f3c](https://github.com/dc-tec/openbao-operator/commit/4197f3c95005161fe4df593f978a11c232ab4a7c))
* **status:** preserve restore state in final adminops patch ([#710](https://github.com/dc-tec/openbao-operator/issues/710)) ([e362877](https://github.com/dc-tec/openbao-operator/commit/e362877532582a179a4c91ab15707f0b32946eeb))
* **upgrade:** correct peer identity, rollback retries, and rollout verification ([2b1f646](https://github.com/dc-tec/openbao-operator/commit/2b1f6467d24d671f09b2d52cd8da686315db1d3f))
* **upgrade:** guard blue/green rollback and preserve active inputs ([#755](https://github.com/dc-tec/openbao-operator/issues/755)) ([ac197df](https://github.com/dc-tec/openbao-operator/commit/ac197dfd71437357bdb933d4f100b07b66b6308c))
* **upgrade:** limit blue/green abort to phases before promotion ([#754](https://github.com/dc-tec/openbao-operator/issues/754)) ([6518d2b](https://github.com/dc-tec/openbao-operator/commit/6518d2b17b8e92e11f2f43024638c84e2152ac7c))
* **upgrade:** retry transient rolling partition conflicts ([#753](https://github.com/dc-tec/openbao-operator/issues/753)) ([b029ff6](https://github.com/dc-tec/openbao-operator/commit/b029ff60bb2354b11a40ee9ed4b4544adcc8e3ff))


### Performance Improvements

* **kube:** reuse apply responses for ownership verification ([#739](https://github.com/dc-tec/openbao-operator/issues/739)) ([a64fd38](https://github.com/dc-tec/openbao-operator/commit/a64fd38602e9ccd1ee505838c095333b56bb2a51))
* **raft:** avoid redundant Autopilot configuration writes ([#737](https://github.com/dc-tec/openbao-operator/issues/737)) ([4b80116](https://github.com/dc-tec/openbao-operator/commit/4b801165c4bfb47f2871989504a32d853e2063cf))

## [0.5.0](https://github.com/dc-tec/openbao-operator/compare/0.4.2...0.5.0) (2026-08-31)

OpenBao Operator 0.5.0 defines a selected compatibility boundary within the
`openbao.org/v1alpha1` API. The API remains alpha. This release rolls up the
`0.5.0-rc.1` through `0.5.0-rc.6` line.

### Features

* **api:** document selected field, omission, update, and condition contracts within `v1alpha1`
* **config:** add plugin-backed KMS seals and OpenBao 2.6 self-init request controls
* **upgrade:** allow idle changes between `RollingUpdate` and `BlueGreen`
* **workload:** configure voter OpenBao container resources independently

### Bug Fixes

* **restore:** persist execution receipts, prevent replay of missing committed Jobs, and recover voter and read-replica state
* **upgrade:** bind validation hooks to an upgrade attempt and preserve leader ordering across revisioned workloads
* **backup:** separate scheduled backups from pre-upgrade snapshots
* **retention:** preserve unrelated Secret owners when pruning retained backups
* **tenancy:** remove tenant governance resources after the last claim and preserve resources shared by active claims
* **networking:** derive Gateway readiness from Route conditions and require external TLS certificates to cover `extraSANs`
* **controller:** reduce steady-state reconciliation and bootstrap log noise

### Security

* **admission:** reject prohibited inline unseal credentials in Hardened clusters
* **lifecycle:** require ownership proof for managed Jobs
* **upgrade:** verify custom BlueGreen validation-hook images through the configured operator image policy
* **deps:** remediate vulnerable Go and website dependencies

### Compatibility

* Validate Kubernetes 1.34, 1.35, and 1.36, with Kubernetes 1.36.1 as the primary target.
* Use OpenBao 2.6.2 as the primary target. Configuration compatibility checks also cover OpenBao 2.4.4 and 2.5.5.

### Upgrade Notes

* Upgrade 0.4.0 or 0.4.1 installations to 0.4.2 before installing 0.5.0.
* Update restore authorization, remove prohibited Hardened inline credentials, and migrate stored manifests before applying the 0.5.0 CRDs.
* Use `RollingUpdate` when upgrading OpenBao from a version before 2.6 to 2.6.x.
* Read the [0.5.0 release notes](https://github.com/dc-tec/openbao-operator/blob/0.5.0/release-notes/0.5.0.md) before upgrading.

## [0.5.0-rc.6](https://github.com/dc-tec/openbao-operator/compare/0.5.0-rc.5...0.5.0-rc.6) (2026-08-28)


### Bug Fixes

* **upgrade:** resolve leaders on revisioned rolling workloads ([#674](https://github.com/dc-tec/openbao-operator/issues/674)) ([3c8e58d](https://github.com/dc-tec/openbao-operator/commit/3c8e58d4849748525b73eab97dbe80f7c791f179))

## [0.5.0-rc.5](https://github.com/dc-tec/openbao-operator/compare/0.5.0-rc.4...0.5.0-rc.5) (2026-08-28)


### Bug Fixes

* **admission:** allow namespace controller cleanup ([#663](https://github.com/dc-tec/openbao-operator/issues/663)) ([2d8d3db](https://github.com/dc-tec/openbao-operator/commit/2d8d3db16721caa4c8fdf656ef5fc10818563283))
* **backup:** ignore pre-upgrade jobs in scheduled observation ([#661](https://github.com/dc-tec/openbao-operator/issues/661)) ([b7ed39f](https://github.com/dc-tec/openbao-operator/commit/b7ed39fa18f974340755288926fdc22fb38e4ee0))
* **backup:** recover after ignoring pre-upgrade jobs ([#662](https://github.com/dc-tec/openbao-operator/issues/662)) ([30e0c9f](https://github.com/dc-tec/openbao-operator/commit/30e0c9f4d05e706da4413ef6888d382e6afd3b81))
* **controller:** reduce steady-state log noise ([#656](https://github.com/dc-tec/openbao-operator/issues/656)) ([fd7462e](https://github.com/dc-tec/openbao-operator/commit/fd7462e107aeee823153f8097271a5b87eb7925c))
* **logging:** reduce steady-state bootstrap noise ([#671](https://github.com/dc-tec/openbao-operator/issues/671)) ([d6656f4](https://github.com/dc-tec/openbao-operator/commit/d6656f4eb27f22a3fcd514a6f30f747f474528dc))
* **restore:** persist execution receipts ([#666](https://github.com/dc-tec/openbao-operator/issues/666)) ([9a84346](https://github.com/dc-tec/openbao-operator/commit/9a843463c0f65b066edde228b6563d81ecaf1201))
* **restore:** preserve target lifecycle JWT identities ([#664](https://github.com/dc-tec/openbao-operator/issues/664)) ([58411f1](https://github.com/dc-tec/openbao-operator/commit/58411f1f81cde9bd03946dfb5baa0de4e24de7f8))
* **restore:** restart voters after snapshot restore ([#658](https://github.com/dc-tec/openbao-operator/issues/658)) ([a1b5bb1](https://github.com/dc-tec/openbao-operator/commit/a1b5bb1e3e9e7ad61dab1bccefb3f549d1cb7c18))
* **restore:** verify snapshot seal by default ([#659](https://github.com/dc-tec/openbao-operator/issues/659)) ([2071a1c](https://github.com/dc-tec/openbao-operator/commit/2071a1c836fdd112e65e1992c43e9a5c42b022b2))
* **retention:** authorize reserved secret names ([#670](https://github.com/dc-tec/openbao-operator/issues/670)) ([1fd26fc](https://github.com/dc-tec/openbao-operator/commit/1fd26fcec8f0cb4791e05dac814d0aa0adeece10))
* **retention:** preserve unrelated secret owners ([#667](https://github.com/dc-tec/openbao-operator/issues/667)) ([de8e32f](https://github.com/dc-tec/openbao-operator/commit/de8e32f1ee102b31ebddda61a329b3932552a7cd))
* **upgrade:** make validation hooks attempt-safe ([#668](https://github.com/dc-tec/openbao-operator/issues/668)) ([7184aa4](https://github.com/dc-tec/openbao-operator/commit/7184aa4179d15e1c5dae1d0e0a686069d0be6963))
* **upgrade:** preserve rolling leader ordering ([#665](https://github.com/dc-tec/openbao-operator/issues/665)) ([cf9c2cc](https://github.com/dc-tec/openbao-operator/commit/cf9c2ccdbebc034cc3d03f48920a3172199f97a0))

## [0.5.0-rc.4](https://github.com/dc-tec/openbao-operator/compare/0.5.0-rc.3...0.5.0-rc.4) (2026-08-25)


### Bug Fixes

* **release:** honor exact versions in manifest mode ([#650](https://github.com/dc-tec/openbao-operator/issues/650)) ([007397f](https://github.com/dc-tec/openbao-operator/commit/007397fb6d5dc2a14018ffe79c6128f1f71765f9))
* **release:** isolate pending label cleanup ([#646](https://github.com/dc-tec/openbao-operator/issues/646)) ([2538473](https://github.com/dc-tec/openbao-operator/commit/25384730cd32ab2c73134a00773f7ba6349b8eb4))

## [0.5.0-rc.3](https://github.com/dc-tec/openbao-operator/compare/0.5.0-rc.2...0.5.0-rc.3) (2026-08-25)


### Bug Fixes

* **release:** unblock RC publication checks ([#642](https://github.com/dc-tec/openbao-operator/issues/642)) ([ff5c506](https://github.com/dc-tec/openbao-operator/commit/ff5c506be38e1dc5acfb924d94608f6b3fb0a58d))
* **release:** compare packaged chart metadata semantically


### Miscellaneous Chores

* **main:** request release 0.5.0-rc.3 ([#643](https://github.com/dc-tec/openbao-operator/issues/643)) ([486d2ea](https://github.com/dc-tec/openbao-operator/commit/486d2eacf038f5efec02dff025fce0814a12175c))

## [0.5.0-rc.2](https://github.com/dc-tec/openbao-operator/compare/0.5.0-rc.1...0.5.0-rc.2) (2026-08-24)


### Bug Fixes

* **release:** checkout before image scans ([#639](https://github.com/dc-tec/openbao-operator/issues/639)) ([fbe9114](https://github.com/dc-tec/openbao-operator/commit/fbe911405ffeebd0eb1bf0119935a02fb2130783))


### Miscellaneous Chores

* **main:** request release 0.5.0-rc.2 ([#640](https://github.com/dc-tec/openbao-operator/issues/640)) ([59576e1](https://github.com/dc-tec/openbao-operator/commit/59576e167213b00ff6394d104a986852ac50bf98))

## [0.5.0-rc.1](https://github.com/dc-tec/openbao-operator/compare/0.4.0...0.5.0-rc.1) (2026-08-24)


### Features

* **api:** stabilize v1alpha1 contracts ([#583](https://github.com/dc-tec/openbao-operator/issues/583)) ([711944e](https://github.com/dc-tec/openbao-operator/commit/711944ed4ec7fa434afbd8f74b5af62b6934b929))
* **config:** support plugin-backed KMS seals ([#535](https://github.com/dc-tec/openbao-operator/issues/535)) ([f911438](https://github.com/dc-tec/openbao-operator/commit/f9114382c9b9e364155da21320761e0b17686eb9))
* **config:** support self-init profile controls ([#536](https://github.com/dc-tec/openbao-operator/issues/536)) ([a79c5a0](https://github.com/dc-tec/openbao-operator/commit/a79c5a0c84f615a88c3f1ac9e619da21abb7ce59))
* **tooling:** make devenv the contributor entry point ([89e5c96](https://github.com/dc-tec/openbao-operator/commit/89e5c9618cbd386eab2aebbc3a40785936f97d0c))
* **upgrade:** allow idle strategy switching ([#556](https://github.com/dc-tec/openbao-operator/issues/556)) ([3509ffb](https://github.com/dc-tec/openbao-operator/commit/3509ffb2abea197cc6609bd56d3b31b2c049fd60))


### Bug Fixes

* **api:** align status and lifecycle contracts ([#604](https://github.com/dc-tec/openbao-operator/issues/604)) ([2a7a3ff](https://github.com/dc-tec/openbao-operator/commit/2a7a3ff81908c3b89ac54b6522efb226b488661f))
* **api:** configure voter resources ([#600](https://github.com/dc-tec/openbao-operator/issues/600)) ([409f482](https://github.com/dc-tec/openbao-operator/commit/409f4827aa446fc5868a6453fadfdc89941b4534))
* **charts:** allow provisioner metrics traffic ([#605](https://github.com/dc-tec/openbao-operator/issues/605)) ([b6cd1f3](https://github.com/dc-tec/openbao-operator/commit/b6cd1f330420005354de3529fe63849194fe9bc7))
* **charts:** set single-tenant controller watch namespace ([#599](https://github.com/dc-tec/openbao-operator/issues/599)) ([026baf2](https://github.com/dc-tec/openbao-operator/commit/026baf21352df2783afc00668bba7cf756acbbf6))
* **compat:** qualify OpenBao 2.6.0 ([#546](https://github.com/dc-tec/openbao-operator/issues/546)) ([fea6897](https://github.com/dc-tec/openbao-operator/commit/fea68976fe1d4ec169d30ed6f03c62b6f7517cfb))
* **config:** omit plugin directory without plugins ([#533](https://github.com/dc-tec/openbao-operator/issues/533)) ([1736c6b](https://github.com/dc-tec/openbao-operator/commit/1736c6b2a268083170e49ea05b6c1ec9d23d6d2a))
* **deps:** remediate website security advisories ([#597](https://github.com/dc-tec/openbao-operator/issues/597)) ([7dd7c37](https://github.com/dc-tec/openbao-operator/commit/7dd7c3737059978baec92c0b215239c84ad220ea))
* **deps:** remediate website transitive vulnerabilities ([#591](https://github.com/dc-tec/openbao-operator/issues/591)) ([eba02bb](https://github.com/dc-tec/openbao-operator/commit/eba02bb4f166162370a071ab8df93891f98ed9ac))
* **deps:** update golang.org/x/mod to v0.40.0 ([#633](https://github.com/dc-tec/openbao-operator/issues/633)) ([4d2796c](https://github.com/dc-tec/openbao-operator/commit/4d2796ce1635ba9ce298cc52609b6749064e6c99))
* **deps:** update vulnerable brace-expansion ([#576](https://github.com/dc-tec/openbao-operator/issues/576)) ([26e7438](https://github.com/dc-tec/openbao-operator/commit/26e74388855697c4eea306ea5bcd0e51cf79e6df))
* **deps:** update vulnerable Go modules ([#569](https://github.com/dc-tec/openbao-operator/issues/569)) ([75d4b9b](https://github.com/dc-tec/openbao-operator/commit/75d4b9b31fba3e0432d7c80c183c64448c2786e0))
* **deps:** update vulnerable website transitive dependencies ([#561](https://github.com/dc-tec/openbao-operator/issues/561)) ([8a857ab](https://github.com/dc-tec/openbao-operator/commit/8a857ab24b065ade486702f815c8701b697e3319))
* **docs:** improve ultrawide layout ([#610](https://github.com/dc-tec/openbao-operator/issues/610)) ([9525648](https://github.com/dc-tec/openbao-operator/commit/952564879bb52254266ab3b1b837361386583203))
* **docs:** restore TypeScript compatibility ([#568](https://github.com/dc-tec/openbao-operator/issues/568)) ([cf514da](https://github.com/dc-tec/openbao-operator/commit/cf514da72dca35cfb58dc88f9f111bd68cc17236))
* **lifecycle:** require ownership proof for managed jobs ([#629](https://github.com/dc-tec/openbao-operator/issues/629)) ([591c579](https://github.com/dc-tec/openbao-operator/commit/591c579c3d614fe067f7075ff9a5913b7631f899))
* **networking:** align Gateway and TLS readiness ([#602](https://github.com/dc-tec/openbao-operator/issues/602)) ([1151b31](https://github.com/dc-tec/openbao-operator/commit/1151b31ef05a9b9f096e83896b03f932556d10f3))
* **provisioner:** clean up tenant governance resources ([#606](https://github.com/dc-tec/openbao-operator/issues/606)) ([bf53821](https://github.com/dc-tec/openbao-operator/commit/bf538212baa79eadb65f74f4db1e204d39870651))
* **release:** harden release candidate gates ([#618](https://github.com/dc-tec/openbao-operator/issues/618)) ([4e999fb](https://github.com/dc-tec/openbao-operator/commit/4e999fba6720f037c8bcd4b9db9232986ae300a4))
* **release:** preserve release-as through squash ([#621](https://github.com/dc-tec/openbao-operator/issues/621)) ([8198a88](https://github.com/dc-tec/openbao-operator/commit/8198a880573c21f2a27f07b5b445c2d71178718d))
* **release:** preserve SPDX schema validity ([#617](https://github.com/dc-tec/openbao-operator/issues/617)) ([305b241](https://github.com/dc-tec/openbao-operator/commit/305b241913063770d5b7efc655d48a3bca8abd8c))
* resolve 0.5.0-rc.1 safety blockers ([#623](https://github.com/dc-tec/openbao-operator/issues/623)) ([ec50a60](https://github.com/dc-tec/openbao-operator/commit/ec50a6057af45b587843d1beab6d1c48afb1b645))
* **upgrade:** verify blue-green validation hook images ([#601](https://github.com/dc-tec/openbao-operator/issues/601)) ([686922a](https://github.com/dc-tec/openbao-operator/commit/686922a286d4b58c485b2118212d9967ab3dd160))


### Performance Improvements

* **tooling:** reduce devenv base closure ([#614](https://github.com/dc-tec/openbao-operator/issues/614)) ([af2826a](https://github.com/dc-tec/openbao-operator/commit/af2826af2262a53707340c4d8659dbc84d851cbb))


### Miscellaneous Chores

* **main:** request release 0.5.0-rc.1 ([#637](https://github.com/dc-tec/openbao-operator/issues/637)) ([dcd2843](https://github.com/dc-tec/openbao-operator/commit/dcd284313208783a0988cdd0620f4c79e5c7433e))

## [0.4.0](https://github.com/dc-tec/openbao-operator/compare/0.3.0...0.4.0) (2026-07-04)


### Features

* **security:** require authority for network publication ([#507](https://github.com/dc-tec/openbao-operator/issues/507)) ([f873094](https://github.com/dc-tec/openbao-operator/commit/f873094f2add4c3bd4d261a14105c6b5853b49c9))
* **self-init:** add initial recovery key bootstrap ([#516](https://github.com/dc-tec/openbao-operator/issues/516)) ([a04cb23](https://github.com/dc-tec/openbao-operator/commit/a04cb232aeaf7d7a6a53b8afbd6af81da1c0e442))


### Bug Fixes

* **security:** delegate dangerous CR controls ([#499](https://github.com/dc-tec/openbao-operator/issues/499)) ([14e1dad](https://github.com/dc-tec/openbao-operator/commit/14e1dadffe0c85e38db60bf76614580629d8b55c))
* **security:** enforce CR reference authorization ([#500](https://github.com/dc-tec/openbao-operator/issues/500)) ([875b999](https://github.com/dc-tec/openbao-operator/commit/875b9997632f16181c233f5f94aba7765840c161))
* **security:** enforce hardened contract for unsafe controls ([#502](https://github.com/dc-tec/openbao-operator/issues/502)) ([6b8f2ad](https://github.com/dc-tec/openbao-operator/commit/6b8f2ad81e797902246948d667f5a11be1a73063))
* **security:** require provenance for managed resources ([#505](https://github.com/dc-tec/openbao-operator/issues/505)) ([fd39a1e](https://github.com/dc-tec/openbao-operator/commit/fd39a1e8c7ccfcb60240776dc1864c6dd810f9e3))
* **self-init:** make bootstrap config one-shot ([#514](https://github.com/dc-tec/openbao-operator/issues/514)) ([cc5876c](https://github.com/dc-tec/openbao-operator/commit/cc5876cb64d00e64158f88fe6a71435376ca3572))

## [0.3.0](https://github.com/dc-tec/openbao-operator/compare/0.2.0...0.3.0) (2026-06-01)


### Features

* **auth:** default JWT flows to inline auth ([#470](https://github.com/dc-tec/openbao-operator/issues/470)) ([445090a](https://github.com/dc-tec/openbao-operator/commit/445090a3ed115574650c11f8de210fca89f5af94))
* **observability:** add workload metrics scraping support ([#476](https://github.com/dc-tec/openbao-operator/issues/476)) ([d799277](https://github.com/dc-tec/openbao-operator/commit/d7992775f6106e5252026d3db6b99d7e39321d26))
* **openbaocluster:** add audit file storage ([#483](https://github.com/dc-tec/openbao-operator/issues/483)) ([f603869](https://github.com/dc-tec/openbao-operator/commit/f60386981813babe97efd08abedf99b4ec55db88))
* **openbaocluster:** add ingress integration readiness ([#409](https://github.com/dc-tec/openbao-operator/issues/409)) ([945b4a4](https://github.com/dc-tec/openbao-operator/commit/945b4a407829e8bb5f5617309873215ae356fc2d))
* **openbao:** improve PKCS[#11](https://github.com/dc-tec/openbao-operator/issues/11) runtime ergonomics ([#400](https://github.com/dc-tec/openbao-operator/issues/400)) ([f32a6ec](https://github.com/dc-tec/openbao-operator/commit/f32a6ec0fdc46ab911bc714daa4ec40d0527ef97))
* **perfcheck:** harden v2 weekly performance harness ([#487](https://github.com/dc-tec/openbao-operator/issues/487)) ([9552584](https://github.com/dc-tec/openbao-operator/commit/95525848447da9e876be853fda5a5096fc1fa412))


### Bug Fixes

* **backup:** record manual triggers and failure time ([#407](https://github.com/dc-tec/openbao-operator/issues/407)) ([ff172c6](https://github.com/dc-tec/openbao-operator/commit/ff172c60d6efabb541f9093dca769fb7b354f0ce))
* **config:** align audit device options with OpenBao ([#423](https://github.com/dc-tec/openbao-operator/issues/423)) ([b1ed4a3](https://github.com/dc-tec/openbao-operator/commit/b1ed4a344e2d9b99fb4ff0efad86107133209bc7))
* **config:** harden generated JWT roles ([#420](https://github.com/dc-tec/openbao-operator/issues/420)) ([546c6db](https://github.com/dc-tec/openbao-operator/commit/546c6dbc605c97c1dac743c5cefb97e4dc595688))
* **config:** use SemVer precedence for OpenBao version checks ([#394](https://github.com/dc-tec/openbao-operator/issues/394)) ([173847d](https://github.com/dc-tec/openbao-operator/commit/173847d22397796e4caa7aa41180f60fcc2a6839))
* **deps:** restore dependency update CI coverage ([#399](https://github.com/dc-tec/openbao-operator/issues/399)) ([032e1b7](https://github.com/dc-tec/openbao-operator/commit/032e1b7a8ae0a008bacc17772bac5d764f410876))
* **deps:** update x/crypto for vulncheck ([#468](https://github.com/dc-tec/openbao-operator/issues/468)) ([b7f48f1](https://github.com/dc-tec/openbao-operator/commit/b7f48f1ff1ab5d6e2bdecaf0498be1639df46fe9))
* **deps:** update x/net for vulncheck ([#471](https://github.com/dc-tec/openbao-operator/issues/471)) ([8f3f554](https://github.com/dc-tec/openbao-operator/commit/8f3f5543b03c35a759cf850beb176c0a6a68bfd8))
* **gateway:** emit TLSRoute as Gateway API v1 ([#429](https://github.com/dc-tec/openbao-operator/issues/429)) ([05177d3](https://github.com/dc-tec/openbao-operator/commit/05177d3aae16aa5bbd80151806b75b5842e6ced9))
* **gateway:** tolerate missing SupportedVersion condition ([#486](https://github.com/dc-tec/openbao-operator/issues/486)) ([a695068](https://github.com/dc-tec/openbao-operator/commit/a6950686fd43dc5384590f7fd22abf410ad082a4))
* **helm:** deduplicate generated RBAC labels ([#414](https://github.com/dc-tec/openbao-operator/issues/414)) ([78f8d73](https://github.com/dc-tec/openbao-operator/commit/78f8d73ed5329c4dfaa7c82926f98ca8933bcb19))
* **openbao:** guard metrics-only listener configuration ([#481](https://github.com/dc-tec/openbao-operator/issues/481)) ([8da708b](https://github.com/dc-tec/openbao-operator/commit/8da708b02bbb6765f3c08036d2eb3a683d647053))
* **openbao:** share JWT token cache ([#419](https://github.com/dc-tec/openbao-operator/issues/419)) ([a4a0887](https://github.com/dc-tec/openbao-operator/commit/a4a088762c584867932d3f48d47ee5399ceadc9e))
* **provisioner:** support external tenant PSS label ownership ([#428](https://github.com/dc-tec/openbao-operator/issues/428)) ([08462c9](https://github.com/dc-tec/openbao-operator/commit/08462c9e108dba154aa9831ce38f9d209b6dbf9e))
* **rbac:** allow verification pull secret reads ([#427](https://github.com/dc-tec/openbao-operator/issues/427)) ([10d40c0](https://github.com/dc-tec/openbao-operator/commit/10d40c0169bda12ea318f9ab1629b0bf4e8bc312))
* **rbac:** permit managed ServiceMonitor reconciliation ([#479](https://github.com/dc-tec/openbao-operator/issues/479)) ([693a43c](https://github.com/dc-tec/openbao-operator/commit/693a43cd6f3d39812cdf4fdf3d9a54a2334fd318))
* **restore:** harden restore job rendering ([#405](https://github.com/dc-tec/openbao-operator/issues/405)) ([3e52f5a](https://github.com/dc-tec/openbao-operator/commit/3e52f5a51731562cb61f8cb8e48d2fdf8bd72e09))
* **restore:** validate static token secret identity ([#466](https://github.com/dc-tec/openbao-operator/issues/466)) ([0508606](https://github.com/dc-tec/openbao-operator/commit/0508606786dcd9b7c88aef4e8d07d4ab0c9e6925))
* **security:** harden backup and restore helper inputs ([#474](https://github.com/dc-tec/openbao-operator/issues/474)) ([01ae81d](https://github.com/dc-tec/openbao-operator/commit/01ae81de8c4d8b8f343bf836e39ca8a3b3545e49))
* **security:** harden backup restore endpoint validation ([#475](https://github.com/dc-tec/openbao-operator/issues/475)) ([98ef673](https://github.com/dc-tec/openbao-operator/commit/98ef67392179a3f2a26725e663f10aba30cd994c))
* **security:** harden transit unseal credential handling ([#473](https://github.com/dc-tec/openbao-operator/issues/473)) ([2e4e6c1](https://github.com/dc-tec/openbao-operator/commit/2e4e6c1d428b0c20774d2161b341e3e476f98190))
* **storage:** enforce request-time endpoint guard ([#477](https://github.com/dc-tec/openbao-operator/issues/477)) ([98f727d](https://github.com/dc-tec/openbao-operator/commit/98f727d62346ea2e570e6537c4838e583a184e4f))
* **storage:** retry transient S3 bucket ensure failures ([#408](https://github.com/dc-tec/openbao-operator/issues/408)) ([9796c2c](https://github.com/dc-tec/openbao-operator/commit/9796c2c174c06f84f8fa645ae29909a774bc6f73))
* **upgrade:** harden rolling upgrade resume ([#406](https://github.com/dc-tec/openbao-operator/issues/406)) ([33fe59d](https://github.com/dc-tec/openbao-operator/commit/33fe59d148751253d6819070630ebad0ce81d80b))
* **workload:** mount OCI plugin directory ([#421](https://github.com/dc-tec/openbao-operator/issues/421)) ([fc95717](https://github.com/dc-tec/openbao-operator/commit/fc95717479d010af90550ae7f74d51e999d36990))

## [0.2.0](https://github.com/dc-tec/openbao-operator/compare/0.1.1...0.2.0) (2026-05-01)


### Features

* **admission:** authorize maintenance through RBAC ([#347](https://github.com/dc-tec/openbao-operator/issues/347)) ([b7c05a7](https://github.com/dc-tec/openbao-operator/commit/b7c05a770bcc97ea1931caf0a3c05919540c38ab))
* **api:** add runtime restart controls ([#348](https://github.com/dc-tec/openbao-operator/issues/348)) ([b1efd34](https://github.com/dc-tec/openbao-operator/commit/b1efd3442c2c5cd0a58c654b749103ab7cf5ac81))
* **readreplicas:** add steady-state read replica topology and status ([#361](https://github.com/dc-tec/openbao-operator/issues/361)) ([9a74c14](https://github.com/dc-tec/openbao-operator/commit/9a74c143e9061f42f5c7557af7a7e9b767252926))
* **readreplicas:** integrate read replicas with upgrade and restore workflows ([#362](https://github.com/dc-tec/openbao-operator/issues/362)) ([e8bf8b8](https://github.com/dc-tec/openbao-operator/commit/e8bf8b820c06ccab1fb81a9df25223dfbf4e0666))


### Bug Fixes

* **admission:** guard hardened security context overrides ([#390](https://github.com/dc-tec/openbao-operator/issues/390)) ([d0a6533](https://github.com/dc-tec/openbao-operator/commit/d0a6533a4c5dbb7b23e4c0c83abf6ee07a5b491e))
* **helm:** allow global values in chart schema ([#378](https://github.com/dc-tec/openbao-operator/issues/378)) ([5dad02e](https://github.com/dc-tec/openbao-operator/commit/5dad02ebc4253ddb366f636e3aea60ffce5f4ffa))
* **helm:** Helm provisioner admission identity ([#387](https://github.com/dc-tec/openbao-operator/issues/387)) ([f781c70](https://github.com/dc-tec/openbao-operator/commit/f781c70b885973b0d682cc102607d3e0b41f36dd))
* **infra:** delete scaled-down raft PVCs ([#341](https://github.com/dc-tec/openbao-operator/issues/341)) ([f406e90](https://github.com/dc-tec/openbao-operator/commit/f406e9029d94c8e7984d77b66cf02b8a97f3c339))
* **multitenancy:** gate cluster reconcile on tenant onboarding ([#359](https://github.com/dc-tec/openbao-operator/issues/359)) ([cfd850f](https://github.com/dc-tec/openbao-operator/commit/cfd850fcf819c4d1562644cc9495143cfee69b27))
* **network:** Require source-scoped managed Ingress access ([#389](https://github.com/dc-tec/openbao-operator/issues/389)) ([a3cec85](https://github.com/dc-tec/openbao-operator/commit/a3cec85a56230560be8196ac02666ad38b7e136d))
* **openbao:** stage safe raft scale-downs ([#339](https://github.com/dc-tec/openbao-operator/issues/339)) ([4da1ec7](https://github.com/dc-tec/openbao-operator/commit/4da1ec74f8e4e45e710a0fae51f86bbf44c257c8))
* **probe:** stabilize openbao workload probes ([#371](https://github.com/dc-tec/openbao-operator/issues/371)) ([260547b](https://github.com/dc-tec/openbao-operator/commit/260547b71d3e12e2ec97ae500f9ed63ab1619804))
* **provisioner:** reduce release reconciliation log noise ([#370](https://github.com/dc-tec/openbao-operator/issues/370)) ([b2f2bca](https://github.com/dc-tec/openbao-operator/commit/b2f2bcaf18dfef15348aa02b9f3de224c02e38ab))
* **security:** fail closed for configured trusted roots ([#393](https://github.com/dc-tec/openbao-operator/issues/393)) ([04cbd64](https://github.com/dc-tec/openbao-operator/commit/04cbd64cf0356f111f0e3c0450b859008e6c5b69))
* **status:** mark unsafe admission mode not production-ready ([#391](https://github.com/dc-tec/openbao-operator/issues/391)) ([98022a3](https://github.com/dc-tec/openbao-operator/commit/98022a3925742e011dbb8ce1fb55c2c79c5a1496))
* **upgrade:** complete SSA ownership migration ([#345](https://github.com/dc-tec/openbao-operator/issues/345)) ([eafa931](https://github.com/dc-tec/openbao-operator/commit/eafa9317acf33155cc7863924b5cb4a8725f97bc))
* **upgrade:** harden bluegreen and rolling recovery flakes ([#374](https://github.com/dc-tec/openbao-operator/issues/374)) ([62cf706](https://github.com/dc-tec/openbao-operator/commit/62cf706df50b8ff462e5893166fc61b83749b298))
* **upgrade:** set executor job resource requirements ([#392](https://github.com/dc-tec/openbao-operator/issues/392)) ([8efb8da](https://github.com/dc-tec/openbao-operator/commit/8efb8da900d378139e35bd32c54489bcc74bec15))
* **upgrade:** treat raft promote already-voter as no-op ([#382](https://github.com/dc-tec/openbao-operator/issues/382)) ([7d25753](https://github.com/dc-tec/openbao-operator/commit/7d25753b9c5c780e174e8adb5487f48c67128267))

## [0.1.1](https://github.com/dc-tec/openbao-operator/compare/0.1.0...0.1.1) (2026-03-31)


### Bug Fixes

* **ci:** allow PR label sync to write labels ([#307](https://github.com/dc-tec/openbao-operator/issues/307)) ([51591d8](https://github.com/dc-tec/openbao-operator/commit/51591d8a212019134cb290d3c876385b08745e01))
* **ci:** replace dangerous PR labeling workflow ([#304](https://github.com/dc-tec/openbao-operator/issues/304)) ([b3740f8](https://github.com/dc-tec/openbao-operator/commit/b3740f89f65379b734ac70e8db5cd5982e479939))
* **upgrade:** verify default helper images for hardened clusters ([#308](https://github.com/dc-tec/openbao-operator/issues/308)) ([8bfeabb](https://github.com/dc-tec/openbao-operator/commit/8bfeabb6b79a8d897617b0aac63d89be9530ef16))

## [0.1.0](https://github.com/dc-tec/openbao-operator/compare/0.1.0-rc.7...0.1.0) (2026-03-30)

The first stable pre-GA release of OpenBao Operator. This release rolls up the
`0.1.0-rc.1` through `0.1.0-rc.7` line into the initial `0.1.0` baseline.

### Highlights

* cluster lifecycle management for OpenBao on Kubernetes, including bootstrap,
  self-init, and day-2 operations
* rolling and blue/green upgrade workflows, with backup and restore support
* tenant onboarding for the default multi-tenant model, plus supported
  single-tenant installation paths
* hardened security posture with image verification, admission guardrails, RBAC
  hardening, and stronger lifecycle validation
* improved observability and operator-facing status, events, and operational
  guidance
* stable Helm chart packaging, signed release tags, signed artifacts, SBOMs,
  and published attestations

### Operational Notes

* The served API remains `openbao.org/v1alpha1`; pre-`1.0` minor releases may
  still carry breaking changes.
* See the `0.1.0-rc.1` through `0.1.0-rc.7` entries below for the detailed
  incremental history behind this release.
* See the project documentation for current compatibility, support policy, and
  known limitations.

## [0.1.0-rc.7](https://github.com/dc-tec/openbao-operator/compare/0.1.0-rc.6...0.1.0-rc.7) (2026-03-30)


### Bug Fixes

* **release:** grant tag workflow comment permissions ([#295](https://github.com/dc-tec/openbao-operator/issues/295)) ([61ec413](https://github.com/dc-tec/openbao-operator/commit/61ec413d7b640e446d135e67e98bbc17c85badec))
* **release:** remove unsupported tag app scope ([#296](https://github.com/dc-tec/openbao-operator/issues/296)) ([e794a76](https://github.com/dc-tec/openbao-operator/commit/e794a7629f3ad31083834a7d5b0f63d64cc4b93e))
* **release:** sign release tags and trim release gates ([#298](https://github.com/dc-tec/openbao-operator/issues/298)) ([33a687b](https://github.com/dc-tec/openbao-operator/commit/33a687b9b93537bffd944791d7f02fc7d48fe855))


### Miscellaneous Chores

* **release:** release 0.1.0-rc.7 ([#299](https://github.com/dc-tec/openbao-operator/issues/299)) ([f1aa990](https://github.com/dc-tec/openbao-operator/commit/f1aa990e7ac08d4cf203d61ede7fd8b3448419bd))

## [0.1.0-rc.6](https://github.com/dc-tec/openbao-operator/compare/0.1.0-rc.5...0.1.0-rc.6) (2026-03-30)


### Features

* **api:** add OpenBaoCluster observedGeneration and printer columns ([#286](https://github.com/dc-tec/openbao-operator/issues/286)) ([1c8f8ae](https://github.com/dc-tec/openbao-operator/commit/1c8f8aeb143fd90ca6452d2f72852c47b14ab5ea))
* **ast-grep:** add policy-driven architecture guardrails with CI enforcement ([#201](https://github.com/dc-tec/openbao-operator/issues/201)) ([1faee9a](https://github.com/dc-tec/openbao-operator/commit/1faee9a6b000d0e68770d7e2894e68d66f13f534))
* **core:** add consistent Kubernetes lifecycle events ([#226](https://github.com/dc-tec/openbao-operator/issues/226)) ([93687af](https://github.com/dc-tec/openbao-operator/commit/93687af087760053b01de76dc6a050e3f5c9e280))
* **core:** harden lifecycle contracts and supporting coverage ([#237](https://github.com/dc-tec/openbao-operator/issues/237)) ([44de947](https://github.com/dc-tec/openbao-operator/commit/44de94790ed765a8eb4036490858139b6a8561bd))
* **infra:** add default node and zone spreading for OpenBao StatefulSets ([#214](https://github.com/dc-tec/openbao-operator/issues/214)) ([1d7afc8](https://github.com/dc-tec/openbao-operator/commit/1d7afc8d55ddede8b24207274101a13d4352e98a))
* **infra:** add pod metadata hooks for workload identity ([#216](https://github.com/dc-tec/openbao-operator/issues/216)) ([9bd2546](https://github.com/dc-tec/openbao-operator/commit/9bd2546ccf5caf0024263073635a2a87ad6713c1))
* **operator:** add supported single-tenant custom identity install paths ([#239](https://github.com/dc-tec/openbao-operator/issues/239)) ([d41ff74](https://github.com/dc-tec/openbao-operator/commit/d41ff74b33133bd05bb2b2a7dadcaf4e4fe3305a))
* **security:** tighten operator security and authentication contracts ([#238](https://github.com/dc-tec/openbao-operator/issues/238)) ([7b14fb1](https://github.com/dc-tec/openbao-operator/commit/7b14fb1cc9046cd469451c3d1d8bb4cb0cbb0302))
* **upgrade:** unify manual upgrade requests on OpenBaoCluster ([#228](https://github.com/dc-tec/openbao-operator/issues/228)) ([b6f6848](https://github.com/dc-tec/openbao-operator/commit/b6f68487add3723932ff454f18d63f0c6688cac5))


### Bug Fixes

* **admission:** allow hardened image verification defaults ([#240](https://github.com/dc-tec/openbao-operator/issues/240)) ([817f144](https://github.com/dc-tec/openbao-operator/commit/817f144a066b21bf05040dd03d35e45ea37b8eb3))
* **auth:** harden operator OIDC bootstrap discovery ([#242](https://github.com/dc-tec/openbao-operator/issues/242)) ([c6fef5d](https://github.com/dc-tec/openbao-operator/commit/c6fef5d05860dab3de42f37cf45c9360c9723986))
* **auth:** retry kubernetes jwks discovery via api service ([#241](https://github.com/dc-tec/openbao-operator/issues/241)) ([37358f6](https://github.com/dc-tec/openbao-operator/commit/37358f65677819cd8d9ac52cd9775ebe718f23ea))
* **ci:** always run perf weekly issue job after failed schedule check ([3d0eb18](https://github.com/dc-tec/openbao-operator/commit/3d0eb189ccda2545def4e3635dd5aabb8a24c599))
* **ci:** harden mainline publish workflows ([#224](https://github.com/dc-tec/openbao-operator/issues/224)) ([3bebc04](https://github.com/dc-tec/openbao-operator/commit/3bebc04970d43c77ba7fc7bcfac5cc7c63a18937))
* **controller:** recheck admission dependencies at runtime ([#262](https://github.com/dc-tec/openbao-operator/issues/262)) ([8203a59](https://github.com/dc-tec/openbao-operator/commit/8203a59048f54c1b89a5862235b602cc9b0fb376))
* **controller:** refresh cluster status on standard cadence ([#257](https://github.com/dc-tec/openbao-operator/issues/257)) ([5fd50f3](https://github.com/dc-tec/openbao-operator/commit/5fd50f371870d3012c485e93b2839a7394cd272a))
* **infra:** fail closed on hostile OIDC bootstrap discovery ([#263](https://github.com/dc-tec/openbao-operator/issues/263)) ([2dbd9be](https://github.com/dc-tec/openbao-operator/commit/2dbd9be4a01395d071af79876ef9cc9989cf606c))
* **rolling:** handle retry status conflicts during upgrade resume ([#192](https://github.com/dc-tec/openbao-operator/issues/192)) ([c6957f2](https://github.com/dc-tec/openbao-operator/commit/c6957f280e1264b7912d0304d5937d6227b8a5f2))
* **security:** harden managed image digests and gateway validation reads ([#243](https://github.com/dc-tec/openbao-operator/issues/243)) ([62a44d0](https://github.com/dc-tec/openbao-operator/commit/62a44d006fc27019e2f5cc1fa58ddb216e088503))
* **security:** remove resolved govulncheck ignores ([#249](https://github.com/dc-tec/openbao-operator/issues/249)) ([58be543](https://github.com/dc-tec/openbao-operator/commit/58be543c57c0b47b977271d1e51eb0baa49853f9))
* **security:** validate UMASK bounds in bao-wrapper ([#195](https://github.com/dc-tec/openbao-operator/issues/195)) ([08b5f8a](https://github.com/dc-tec/openbao-operator/commit/08b5f8a6a92325d176ba40e3c79a4106570ab029))
* **security:** wrap bundle fallback verification error ([#200](https://github.com/dc-tec/openbao-operator/issues/200)) ([827899e](https://github.com/dc-tec/openbao-operator/commit/827899ea077c149e93ccf7aaf3c9d333a45b37c5))
* **status:** make lifecycle status guidance more actionable ([#227](https://github.com/dc-tec/openbao-operator/issues/227)) ([6bf9147](https://github.com/dc-tec/openbao-operator/commit/6bf9147aa42231f0f2494c00f6c9d77924a7e292))
* **storage:** enforce storage class immutability consistently ([#215](https://github.com/dc-tec/openbao-operator/issues/215)) ([c0a551f](https://github.com/dc-tec/openbao-operator/commit/c0a551fd8e5e0c653d151de5b17990573767c333))
* **upgrade:** clear rolling retry failure state with merge status patch ([#205](https://github.com/dc-tec/openbao-operator/issues/205)) ([f4b47f9](https://github.com/dc-tec/openbao-operator/commit/f4b47f9403fdd1ea954dd7af902d194f7889b055))
* **upgrade:** harden OpenBaoCluster upgrade validation, recovery, and documentation ([#225](https://github.com/dc-tec/openbao-operator/issues/225)) ([a170c0a](https://github.com/dc-tec/openbao-operator/commit/a170c0acb3c835016f32483169d3c61e07ab26b3))
* **validation:** block upgrade strategy switches ([#288](https://github.com/dc-tec/openbao-operator/issues/288)) ([b5f0af4](https://github.com/dc-tec/openbao-operator/commit/b5f0af4a7e5c7fbceb733a52e4bc3327171f93c6))


### Miscellaneous Chores

* trigger release-please for 0.1.0-rc.6 ([#293](https://github.com/dc-tec/openbao-operator/issues/293)) ([9f8bfa1](https://github.com/dc-tec/openbao-operator/commit/9f8bfa193a8bb45d3327f99a6e365e49cab9879c))

## [0.1.0-rc.5](https://github.com/dc-tec/openbao-operator/compare/0.1.0-rc.4...0.1.0-rc.5) (2026-03-01)


### Bug Fixes

* **ci:** unblock draft release lookup and run reproducibility post-release ([#185](https://github.com/dc-tec/openbao-operator/issues/185)) ([4fa1089](https://github.com/dc-tec/openbao-operator/commit/4fa10896da12c125cf7873567fd0e49876299517))


### Miscellaneous Chores

* **release:** set release target to 0.1.0-rc.5 ([#187](https://github.com/dc-tec/openbao-operator/issues/187)) ([39649ee](https://github.com/dc-tec/openbao-operator/commit/39649ee68ef28ed3c94cfebf2dc9de04f3ff2466))

## [0.1.0-rc.4](https://github.com/dc-tec/openbao-operator/compare/0.1.0-rc.3...0.1.0-rc.4) (2026-03-01)


### Bug Fixes

* **build:** stabilize byte reproducibility gates for checksums and sbom outputs ([#180](https://github.com/dc-tec/openbao-operator/issues/180)) ([7547ea4](https://github.com/dc-tec/openbao-operator/commit/7547ea48876ddda4788a4d004da31f5f4ea7b985))
* **ci:** stabilize release/build reproducibility and align CI documentation ([#179](https://github.com/dc-tec/openbao-operator/issues/179)) ([4378cfe](https://github.com/dc-tec/openbao-operator/commit/4378cfe9cf33c35b87ea429290608a2d6a3f0c18))


### Miscellaneous Chores

* **release:** set release target to 0.1.0-rc.4 ([#183](https://github.com/dc-tec/openbao-operator/issues/183)) ([b5402ea](https://github.com/dc-tec/openbao-operator/commit/b5402eaed71cf776dfa6b6a42b23c5030b38896c))

## [0.1.0-rc.3](https://github.com/dc-tec/openbao-operator/compare/0.1.0-rc.2...0.1.0-rc.3) (2026-02-28)


### Miscellaneous Chores

* **release:** set release target to 0.1.0-rc.3 ([#176](https://github.com/dc-tec/openbao-operator/issues/176)) ([af6043e](https://github.com/dc-tec/openbao-operator/commit/af6043ee5c02d6440b9de9401ce8bb9c332831ba))

## [0.1.0-rc.2](https://github.com/dc-tec/openbao-operator/compare/0.1.0-rc.1...0.1.0-rc.2) (2026-02-27)


### Bug Fixes

* **ci:** create kind cluster in release e2e gate ([#135](https://github.com/dc-tec/openbao-operator/issues/135)) ([838fe67](https://github.com/dc-tec/openbao-operator/commit/838fe6744cdde4346fe000c092c8059700de0664))


### Miscellaneous Chores

* **release:** set release target to 0.1.0-rc.2 ([#136](https://github.com/dc-tec/openbao-operator/issues/136)) ([624238d](https://github.com/dc-tec/openbao-operator/commit/624238df4f561709ce0390f3332c0737685d7a67))

## 0.1.0-rc.1 (2026-02-26)


### ⚠ BREAKING CHANGES

* **core:** Improve OIDC/JWT bootstrap, update strategy configuration and configuration ergonomics ([#73](https://github.com/dc-tec/openbao-operator/issues/73))
* **core:** remove Sentinel drift detection (VAP hardening) ([#39](https://github.com/dc-tec/openbao-operator/issues/39))
* **upgrade:** simplify blue/green cutover and split rolling strategy ([#37](https://github.com/dc-tec/openbao-operator/issues/37))
* **config:** openbaocluster config renderer
* **upgrade:** upgrade manager; blue/green upgrades
* **controller:** openbaocluster refactor; sentinel improvements

### Features

* **api:** improve sentinel observability ([b9d4168](https://github.com/dc-tec/openbao-operator/commit/b9d41686964165291a974d900d9050d8be8983c0))
* **backup;restore:** azure blob storage and GCS support as backup provider ([#71](https://github.com/dc-tec/openbao-operator/issues/71)) ([e8a2f2d](https://github.com/dc-tec/openbao-operator/commit/e8a2f2dd68b4af96136d0e387e9199e934a74c82))
* **bluegreen:** blue/green traffic switching improvements ([5e5f815](https://github.com/dc-tec/openbao-operator/commit/5e5f8157e52dd7dfcacd07565cd35270c0ec3f20))
* **charts:** operator helm chart ([c00ff58](https://github.com/dc-tec/openbao-operator/commit/c00ff58ab1d39b64919acad5456ae221c8b69fc1))
* **config:** structured config for self-init ([abf2259](https://github.com/dc-tec/openbao-operator/commit/abf22590241d1a559bdba857440f0918760a78a4))
* **controller;chart;rbac:** controller hardening, Helm sync automation, and RBAC race fix ([#40](https://github.com/dc-tec/openbao-operator/issues/40)) ([c9dd0b5](https://github.com/dc-tec/openbao-operator/commit/c9dd0b54857a60d2dfe47bcc10d4a75929412a27))
* **controller:** add extra metrics ([3ed3915](https://github.com/dc-tec/openbao-operator/commit/3ed3915ad5d37349891bbc0abadccca7ce0b0643))
* **controller:** improve event filtering using centralized predicates ([968df6c](https://github.com/dc-tec/openbao-operator/commit/968df6c7c58cd7fb95793208605c4ae2f8fe4e8d))
* **controller:** single tenancy support ([49b7327](https://github.com/dc-tec/openbao-operator/commit/49b7327caed9394e89999023a4cd1f2488faf2a4))
* **core:** add perf baseline harness and gates ([#118](https://github.com/dc-tec/openbao-operator/issues/118)) ([bf91ce2](https://github.com/dc-tec/openbao-operator/commit/bf91ce24ec1de79cb96b1d1a1370938b62195dd7))
* **core:** blue/green upgrades ([1a6783e](https://github.com/dc-tec/openbao-operator/commit/1a6783eeb1cb45933d5cc146c81644fca26ccc11))
* **core:** cluster lifecycle hardening; e2e suite refactor ([#72](https://github.com/dc-tec/openbao-operator/issues/72)) ([3de5142](https://github.com/dc-tec/openbao-operator/commit/3de5142367e0076a169f1ebb14497c150dbf5722))
* **core:** enable Raft Autopilot for automatic dead server cleanup ([#44](https://github.com/dc-tec/openbao-operator/issues/44)) ([61aa711](https://github.com/dc-tec/openbao-operator/commit/61aa7115390c8cd9143f9fd4f985414c2756b909))
* **core:** helm manifest values and templates ([6060fbd](https://github.com/dc-tec/openbao-operator/commit/6060fbd04cfb36caadd97718f604fee4250f43e3))
* **core:** Improve OIDC/JWT bootstrap, update strategy configuration and configuration ergonomics ([#73](https://github.com/dc-tec/openbao-operator/issues/73)) ([446e494](https://github.com/dc-tec/openbao-operator/commit/446e4949febbb3155aa999b2d53a720f971e8db5))
* **core:** introduce restore CRD ([4d19b72](https://github.com/dc-tec/openbao-operator/commit/4d19b72b5c74b337b61776f58f0d8f6ff711e8a9))
* **core:** introduce structured error types ([0b17ae1](https://github.com/dc-tec/openbao-operator/commit/0b17ae13e63ac49ac33d066df146ddb3190c6c40))
* **core:** make JWT audience configurable and plumb JWT bootstrap config across backup/upgrade/restore ([#57](https://github.com/dc-tec/openbao-operator/issues/57)) ([3057c61](https://github.com/dc-tec/openbao-operator/commit/3057c61293920b718d3dd5ece951858b77f5b1c6))
* **core:** OpenShift compatibility support ([#62](https://github.com/dc-tec/openbao-operator/issues/62)) ([47d7770](https://github.com/dc-tec/openbao-operator/commit/47d7770854a52d3113294ecc9cd667d8b54acd77))
* **e2e:** end-to-end testing ([47bed1f](https://github.com/dc-tec/openbao-operator/commit/47bed1fa19b3beadf1cbb339a16456aa0b519359))
* **infra;controller:** implement support for online PVC expansion of running OpenBao Clusters ([#75](https://github.com/dc-tec/openbao-operator/issues/75)) ([42fabd3](https://github.com/dc-tec/openbao-operator/commit/42fabd30c6ef0d5ec4ababe85f74fc8d37cc1810))
* **infra:** Expose listenerName field for Gateway API HTTPRoute targeting ([#30](https://github.com/dc-tec/openbao-operator/issues/30)) ([5babd3f](https://github.com/dc-tec/openbao-operator/commit/5babd3f8a2b44c8135b8c1e2ea75a31062bc42e9))
* **infra:** improve hardened and ACME deployments ([#63](https://github.com/dc-tec/openbao-operator/issues/63)) ([d40600e](https://github.com/dc-tec/openbao-operator/commit/d40600effacb689a89c0f52aee1f74e74129117e))
* **infra:** make DNS namespace configurable in NetworkPolicies ([#58](https://github.com/dc-tec/openbao-operator/issues/58)) ([a675dfa](https://github.com/dc-tec/openbao-operator/commit/a675dfad6c52c030e7c265ebf60836b976957d26))
* **infra:** operator security hardening ([34e703f](https://github.com/dc-tec/openbao-operator/commit/34e703fc57d8915a68c8683f2cd34006a0316505))
* **infra:** standardize sub reconciler pattern ([ae79ef5](https://github.com/dc-tec/openbao-operator/commit/ae79ef5d82cf3c2510b9e23150639d35d73a810a))
* **manifests:** admission validation policies; backup auth ([a76541d](https://github.com/dc-tec/openbao-operator/commit/a76541d5d6770838f69d8b203ff2b4785b23c7a5))
* **manifests:** install manifest ([ffc63c6](https://github.com/dc-tec/openbao-operator/commit/ffc63c669bd13c930c8e8f11ce465298e4ab4c0d))
* **manifests:** optional sentinel deployment for quicker reconcile ([081a17a](https://github.com/dc-tec/openbao-operator/commit/081a17a02060db9fb188477620fbd76d1c55522e))
* **manifests:** security; rbac; backup and upgrade improvements ([89a5ee9](https://github.com/dc-tec/openbao-operator/commit/89a5ee9e1f2f95ab4c374f7831e48167a4b1303b))
* **manifests:** self-service tenant onboarding ([2a8d4d0](https://github.com/dc-tec/openbao-operator/commit/2a8d4d03bfdd53b86af93dbf4b6b4be9c9fcc9a7))
* **manifests:** structured configuration ([503961d](https://github.com/dc-tec/openbao-operator/commit/503961d790f996e1de193b321d005d2d8dcc0d4d))
* **manifests:** wire-in image verification for all components ([d94d1f9](https://github.com/dc-tec/openbao-operator/commit/d94d1f9d14c81bd994124fc964e69787045fb646))
* **observability:** add metrics, dashboards, e2e assertions; upgrade stability ([#101](https://github.com/dc-tec/openbao-operator/issues/101)) ([d4ce07d](https://github.com/dc-tec/openbao-operator/commit/d4ce07dc4d895381066ca86962fc5758f66dfd33))
* **perf:** refresh kind performance baseline ([#120](https://github.com/dc-tec/openbao-operator/issues/120)) ([69e5366](https://github.com/dc-tec/openbao-operator/commit/69e5366651ac500925336358fb013c0e9650e4f2))
* **policy:** enforce Hardened profile requires replicas &gt;= 3 via VAP ([#23](https://github.com/dc-tec/openbao-operator/issues/23)) ([c15ab9f](https://github.com/dc-tec/openbao-operator/commit/c15ab9fd1421b613e138861a962f51cd76b721b3))
* **provisioner:** configurable tenant resource quotas ([#50](https://github.com/dc-tec/openbao-operator/issues/50)) ([4c6fc29](https://github.com/dc-tec/openbao-operator/commit/4c6fc2915cb821547129a6c9b8e1ed73e42fd500))
* **restore:** add RBAC for restore jobs and validate authentication ([#16](https://github.com/dc-tec/openbao-operator/issues/16)) ([e7772a1](https://github.com/dc-tec/openbao-operator/commit/e7772a146482c9626c545bddff185b9a2f687c1b))
* **security:** Add admission-time protections for SSRF, TLS secrets, and tenant self-service ([#51](https://github.com/dc-tec/openbao-operator/issues/51)) ([ae2f86c](https://github.com/dc-tec/openbao-operator/commit/ae2f86c851b1369676cee536b37dd934c8ef0d0a))
* **security:** add operatorimageVerification field to CRD to allow separate verification of both OpenBao and Operator images ([#8](https://github.com/dc-tec/openbao-operator/issues/8)) ([4c1b8cc](https://github.com/dc-tec/openbao-operator/commit/4c1b8cccd1d2c47618c29efa3d08c54535da421c))
* **security:** cosign keyless image verification ([0c60a60](https://github.com/dc-tec/openbao-operator/commit/0c60a60a530904695241707aba96cae7edf8390f))
* **security:** expand control-plane audit coverage for startup, operations, and RBAC mutations ([#109](https://github.com/dc-tec/openbao-operator/issues/109)) ([b32dc97](https://github.com/dc-tec/openbao-operator/commit/b32dc97175999aadb84cecf867395a7cca2a6f85))
* **security:** harden image verification and align edge/nightly signed manifest streams ([#112](https://github.com/dc-tec/openbao-operator/issues/112)) ([b755ca3](https://github.com/dc-tec/openbao-operator/commit/b755ca333c4e598cf5904b9e68817ac540393cc5))
* **security:** harden image verification defaults and sign edge/nightly images ([#111](https://github.com/dc-tec/openbao-operator/issues/111)) ([5ffed83](https://github.com/dc-tec/openbao-operator/commit/5ffed83ea179fe14fedba50320425d8e4ce0b30c))
* **security:** harden operator RBAC with ValidatingAdmissionPolicy guardrails ([#100](https://github.com/dc-tec/openbao-operator/issues/100)) ([643fd94](https://github.com/dc-tec/openbao-operator/commit/643fd94af7f0a128bf4f62fa073ffa70ec92af18))
* **test:** tlsroute; monitoring; backup/upgrade ([bc8497a](https://github.com/dc-tec/openbao-operator/commit/bc8497a112177ecf837ad3c935af4c90945caafb))
* **upgrade:** harden backup and restore flows ([cb542ab](https://github.com/dc-tec/openbao-operator/commit/cb542ab466e29ddbbf61460ebd9368891aa9e359))
* **upgrade:** improve upgrade manager stability by using SSA for status updates and make pre-upgrade backup job names deterministic ([#17](https://github.com/dc-tec/openbao-operator/issues/17)) ([78f6124](https://github.com/dc-tec/openbao-operator/commit/78f6124b7e3545149b86a167165fb081b7c810ac))
* **vap:** harden OpenBaoRestore VAP guardrails + allow default backup executor image ([#76](https://github.com/dc-tec/openbao-operator/issues/76)) ([93524c8](https://github.com/dc-tec/openbao-operator/commit/93524c8b91563bd5bee91caf2ef0d9360d0a2b04))


### Bug Fixes

* **admission:** add admission check ([50d3af0](https://github.com/dc-tec/openbao-operator/commit/50d3af0aa06773e5ea5ee98a1194cba7c9f98b1e))
* **admission:** implement security/rbac improvements ([95cd1b2](https://github.com/dc-tec/openbao-operator/commit/95cd1b246c2eacb18e9fa8da977a44ee7faf1313))
* **api,security:** harden CRD/admission contracts and guardrails ([#106](https://github.com/dc-tec/openbao-operator/issues/106)) ([40f49d8](https://github.com/dc-tec/openbao-operator/commit/40f49d890a757c3623f08142355fb5c1db3ad5e6))
* **api:** switch SecretReference to LocalObjectReference ([c3b8fef](https://github.com/dc-tec/openbao-operator/commit/c3b8fefd41e8f06b1b4456f66861974d06de4428))
* **auth:** harden OIDC discovery and add least-privilege RBAC + admission guardrails ([#86](https://github.com/dc-tec/openbao-operator/issues/86)) ([d128a5d](https://github.com/dc-tec/openbao-operator/commit/d128a5d653aa504bbaaadaf48dbd240fc8c7c8da))
* **backup:** align retention behavior across providers and refactor backup/restore flow ([#105](https://github.com/dc-tec/openbao-operator/issues/105)) ([2e1fa9d](https://github.com/dc-tec/openbao-operator/commit/2e1fa9d941f818512155e34d6e7c8a9c6a620689))
* **backup:** backup ([8bdc5fa](https://github.com/dc-tec/openbao-operator/commit/8bdc5fa0f00affcc6ca8c172f64bdb557a994b54))
* **backup:** make sure backup jobs are idempotent ([#47](https://github.com/dc-tec/openbao-operator/issues/47)) ([8e2ec6f](https://github.com/dc-tec/openbao-operator/commit/8e2ec6f058928a169718908b3e7fa38150ffcf80))
* **backup:** manual / scheduled backups ([f68172e](https://github.com/dc-tec/openbao-operator/commit/f68172e4800ce383d8a5b40e910465f6ad1ce86c))
* **backup:** pod security context hardening for init and backup containers ([cec43e6](https://github.com/dc-tec/openbao-operator/commit/cec43e6c7fa1e080ad4ec4d223bcb61d2106bbf2))
* **backup:** remove unused function ([556161f](https://github.com/dc-tec/openbao-operator/commit/556161f542a71570fb94660a4d986a51df660a84))
* **backup:** upgrade paths ([e2bb9b5](https://github.com/dc-tec/openbao-operator/commit/e2bb9b5ceded236632ce89eee43a001efc0dca70))
* **bluegreen:** harden deterministic upgrade flow, tests, and docs ([#104](https://github.com/dc-tec/openbao-operator/issues/104)) ([bb64c2e](https://github.com/dc-tec/openbao-operator/commit/bb64c2ed593962f94c004971ec0986270a5270e0))
* **chart:** sync helm chart ([9c22829](https://github.com/dc-tec/openbao-operator/commit/9c228297ace116396f351290620eb44991739d57))
* **chart:** sync helm chart ([#7](https://github.com/dc-tec/openbao-operator/issues/7)) ([507c364](https://github.com/dc-tec/openbao-operator/commit/507c36400b8f83b75e614df3fd34fcddd0e12283))
* **ci:** handle kind load failures for multi-arch OpenBao images ([#125](https://github.com/dc-tec/openbao-operator/issues/125)) ([05038ba](https://github.com/dc-tec/openbao-operator/commit/05038baaf0a706ee4c4c1c1d944f93a84c4768f0))
* **ci:** restore security and bot PR pipeline stability ([#129](https://github.com/dc-tec/openbao-operator/issues/129)) ([ae8d297](https://github.com/dc-tec/openbao-operator/commit/ae8d297eae7ed5673d919673167ac4bdea002e1c))
* **ci:** stabilize nightly e2e image refs and matrix check naming ([#121](https://github.com/dc-tec/openbao-operator/issues/121)) ([c69993d](https://github.com/dc-tec/openbao-operator/commit/c69993d4eace0c5104aaf1659f390a25fadb4b69))
* **controller:** infer BlueImage from running pods to prevent premature upgrades ([#95](https://github.com/dc-tec/openbao-operator/issues/95)) ([dfdc11e](https://github.com/dc-tec/openbao-operator/commit/dfdc11efe964fa427b69cfebf0b22bac0fa98d3e))
* **controller:** persist initialized status ([c2ebbd1](https://github.com/dc-tec/openbao-operator/commit/c2ebbd1b6701982fbf5881d71c5a073d35f9854d))
* **controller:** Prevent data loss by orphaning secrets when DeletionPolicy is Retain ([#11](https://github.com/dc-tec/openbao-operator/issues/11)) ([0899cfa](https://github.com/dc-tec/openbao-operator/commit/0899cfa44d53deea6aaf65343d44b61c6a488168))
* **controller:** prevent OpenBaoCluster resourceVersion churn ([#49](https://github.com/dc-tec/openbao-operator/issues/49)) ([c0e4fe8](https://github.com/dc-tec/openbao-operator/commit/c0e4fe88c628cec4cab6ed6cd1bc053378f27d1e))
* **controller:** remove force ownership of status ([#70](https://github.com/dc-tec/openbao-operator/issues/70)) ([e59e5da](https://github.com/dc-tec/openbao-operator/commit/e59e5da6d22ea82dde7c8c272447e4744991b51e))
* **controller:** strengthen status updates with patching ([6c54a5e](https://github.com/dc-tec/openbao-operator/commit/6c54a5e4505c8ff17756d8cc477b75641eaebedc))
* **controller:** timeout for image verification ([cbcd9cf](https://github.com/dc-tec/openbao-operator/commit/cbcd9cf753ee6b33d0167d8195cfff69a13e966c))
* **core:** add temporary transient error ([e0aeb21](https://github.com/dc-tec/openbao-operator/commit/e0aeb2146e9e713226c8aeecab06508d69295b2d))
* **core:** centralize constants into internal/constants ([058b0a3](https://github.com/dc-tec/openbao-operator/commit/058b0a3b5abfc1733d49b6dbcbf66e5a4fbb3be4))
* **core:** check token existence ([f4669f5](https://github.com/dc-tec/openbao-operator/commit/f4669f5b2cc2fe844e02e1282e8e3e8e12d5763a))
* **core:** decouple openbao client logic ([d3a0acc](https://github.com/dc-tec/openbao-operator/commit/d3a0acc6323aa7cda1e20b47189576f3a094bb0b))
* **core:** harden controller determinism and idempotency  ([#107](https://github.com/dc-tec/openbao-operator/issues/107)) ([e573bf9](https://github.com/dc-tec/openbao-operator/commit/e573bf96702c4fca761c34456b9898f5d7d63e90))
* **core:** improve container status checking ([e357dcc](https://github.com/dc-tec/openbao-operator/commit/e357dcc0fd385adb4b6a400eaca3cd84ef52bcc4))
* **core:** rbac and admission hardening ([477be64](https://github.com/dc-tec/openbao-operator/commit/477be6472cd6d45324b2ec879a70d50bd10fcf2f))
* **deps:** resolve security vulnerabilities in go-tuf/v2 and rekor dependencies ([#74](https://github.com/dc-tec/openbao-operator/issues/74)) ([ecbfba8](https://github.com/dc-tec/openbao-operator/commit/ecbfba80715689bf0eb1689ec370befbfad6cd83))
* **e2e:** sentinel drift detection robustness ([648f3df](https://github.com/dc-tec/openbao-operator/commit/648f3df3b08e71633f172993a22ecaf0559acfdb))
* **e2e:** unused param ([b7a9c02](https://github.com/dc-tec/openbao-operator/commit/b7a9c0294172e5d52c048e117a874239ffb6d10a))
* **images:** fail-fast on missing OPERATOR_VERSION environment variable ([#25](https://github.com/dc-tec/openbao-operator/issues/25)) ([1a42097](https://github.com/dc-tec/openbao-operator/commit/1a42097c8fd80bfe773682865c1119b29ca77d02))
* Implement versioned default images for backup, upgrade, and init container ([#14](https://github.com/dc-tec/openbao-operator/issues/14)) ([1b34f78](https://github.com/dc-tec/openbao-operator/commit/1b34f785009750a2667293d31334260fee04716d))
* **infra:** add IPv6/dual-stack support for listener binding and development egress rules ([#56](https://github.com/dc-tec/openbao-operator/issues/56)) ([7bfdb41](https://github.com/dc-tec/openbao-operator/commit/7bfdb41840bed338cbfcede82be3aea6642a7a53))
* **infra:** exclude job pods from pdb ([#9](https://github.com/dc-tec/openbao-operator/issues/9)) ([825a191](https://github.com/dc-tec/openbao-operator/commit/825a1916d68a6a0bb09c4f46c1251cf2af9cd159))
* **infra:** improve initialization robustness by treating transient Secret/RBAC errors as retriable and hardening root-token creation ([#55](https://github.com/dc-tec/openbao-operator/issues/55)) ([f760ac5](https://github.com/dc-tec/openbao-operator/commit/f760ac5c17bd99f747e8c3dc637bdcee1b4cb511))
* **infra:** resolve BackendTLSPolicy mismatch and cleanup stale services after Blue/Green upgrade ([#10](https://github.com/dc-tec/openbao-operator/issues/10)) ([7052a54](https://github.com/dc-tec/openbao-operator/commit/7052a54145a4d9ac1a1d9ed3b7fdb1cc8de994a2))
* **infra:** stop apiserver endpoint autodetection; use service VIP allow-list with optional endpoint IPs ([#54](https://github.com/dc-tec/openbao-operator/issues/54)) ([d73179a](https://github.com/dc-tec/openbao-operator/commit/d73179a434428bb787684791d1de88dc778f138f))
* **init:** retrty writing root token to secret to handle transient cr… ([#84](https://github.com/dc-tec/openbao-operator/issues/84)) ([e100176](https://github.com/dc-tec/openbao-operator/commit/e1001769b05fbccae2c861b586dd3eac3eaefd8c))
* **kube:** add job check ([a7439a9](https://github.com/dc-tec/openbao-operator/commit/a7439a9fe060a4710deda76bea6b7bfafde18020))
* **manifests:** improve operator rbac ([8a17db3](https://github.com/dc-tec/openbao-operator/commit/8a17db3dc103b294e7b602c0a69ff1088e5393e7))
* **manifests:** make JWT auth bootstrap a opt-in feature ([ded02a3](https://github.com/dc-tec/openbao-operator/commit/ded02a3173c672e7cbc03f5e993635e1cb345663))
* **manifests:** operator namespace detection ([139450a](https://github.com/dc-tec/openbao-operator/commit/139450a401beb7b292aad7962e84e6dcfb109098))
* **manifests:** rbac; upgrade deps ([8b7d4e8](https://github.com/dc-tec/openbao-operator/commit/8b7d4e85c080473fcc01d53e103ffc3891d0949a))
* **manifests:** secure defaults and profiles ([6617383](https://github.com/dc-tec/openbao-operator/commit/66173839968834008119e07cf29cc99188ef8121))
* **nightly:** harden init token persistence and e2e autopilot reliability ([#117](https://github.com/dc-tec/openbao-operator/issues/117)) ([f85886f](https://github.com/dc-tec/openbao-operator/commit/f85886fc92b5df3eff30b5075659b41279e8717d))
* **openbao:** handle 403 forbidden gracefully ([#94](https://github.com/dc-tec/openbao-operator/issues/94)) ([4243f67](https://github.com/dc-tec/openbao-operator/commit/4243f67d68e69d8406b5e0702c806a4f876dd774))
* **security;e2e:** verify signed hardened/acme flows in CI/nightly and support digest-safe keyless defaults ([#116](https://github.com/dc-tec/openbao-operator/issues/116)) ([3b966fe](https://github.com/dc-tec/openbao-operator/commit/3b966fe25097fbb4e490682f93bc8671463741f2))
* **security:** implement image verification LRU cache; docker auth handeling ([#18](https://github.com/dc-tec/openbao-operator/issues/18)) ([a4b7203](https://github.com/dc-tec/openbao-operator/commit/a4b720313ec7fa40a7b0123de4bbbbe090441c0e))
* **security:** performance issue image verification by reording cache lookups ([#12](https://github.com/dc-tec/openbao-operator/issues/12)) ([a5ca5eb](https://github.com/dc-tec/openbao-operator/commit/a5ca5eb1268d9afe98d8bcc0ce6c3dda0efde20c))
* **sentinel:** prevent noisy neighbors and thundering herd behavior ([57eb7bd](https://github.com/dc-tec/openbao-operator/commit/57eb7bdfd9b714e2d64c0954d5a36c260dde7efa))
* **sentinel:** rely on uuids instead of timestamps as sentinel triggerid ([#6](https://github.com/dc-tec/openbao-operator/issues/6)) ([f88b697](https://github.com/dc-tec/openbao-operator/commit/f88b697f6dc13f19cf9a00d2764a4ed0be58868d))
* **upgrade:** add metrics for upgrade ([936d71e](https://github.com/dc-tec/openbao-operator/commit/936d71edca1f111a40c8a04bd32910459c24fc93))
* **upgrade:** improve upgrade manager stability ([#13](https://github.com/dc-tec/openbao-operator/issues/13)) ([c6a1b34](https://github.com/dc-tec/openbao-operator/commit/c6a1b34a515e7ed4201d61cd2b564ba2b0a9b5bf))
* **upgrade:** make rolling upgrades deterministic and harden rolling upgrade coverage ([#103](https://github.com/dc-tec/openbao-operator/issues/103)) ([5f3edfd](https://github.com/dc-tec/openbao-operator/commit/5f3edfd3d1b111b3b07a8818aa743f523ab8d810))
* **upgrade:** revert partition update to MergeFrom to fix StatefulSet validation ([#52](https://github.com/dc-tec/openbao-operator/issues/52)) ([504c319](https://github.com/dc-tec/openbao-operator/commit/504c31970030519ed602f16ebc3d7be5b339d32c))
* **upgrade:** use SSA for upgrade manager ([d0c289c](https://github.com/dc-tec/openbao-operator/commit/d0c289ce76686f7329e79cbbbfdc29b172446c74))
* **vap:** require self init requests when self initialization is enabled ([#82](https://github.com/dc-tec/openbao-operator/issues/82)) ([c572aaa](https://github.com/dc-tec/openbao-operator/commit/c572aaa392ecc8c8f6dccdee5203a964055a6106))
* **vap:** stuck Job deletions by allowing GC Job-finalizer updates in lock-managed-resource-mutations VAP ([#53](https://github.com/dc-tec/openbao-operator/issues/53)) ([0c56a87](https://github.com/dc-tec/openbao-operator/commit/0c56a8726c3a972566fc4a93b8a8d3d9bbd99ae7))


### Miscellaneous Chores

* **release:** set release target to 0.1.0-rc.1 ([#133](https://github.com/dc-tec/openbao-operator/issues/133)) ([ad509ed](https://github.com/dc-tec/openbao-operator/commit/ad509edfa50936cc8b263fcae1d1233fa6b9f47b))


### Code Refactoring

* **config:** openbaocluster config renderer ([a230262](https://github.com/dc-tec/openbao-operator/commit/a230262c4795566c21ad58a65b74364e7cdd36b6))
* **controller:** openbaocluster refactor; sentinel improvements ([9d0de98](https://github.com/dc-tec/openbao-operator/commit/9d0de984d9681d53f4c5569ff84443ae46e2bad5))
* **core:** remove Sentinel drift detection (VAP hardening) ([#39](https://github.com/dc-tec/openbao-operator/issues/39)) ([d289cf2](https://github.com/dc-tec/openbao-operator/commit/d289cf262213ab13ca3c9e3631df1d4845ee6fc7))
* **upgrade:** simplify blue/green cutover and split rolling strategy ([#37](https://github.com/dc-tec/openbao-operator/issues/37)) ([7453e23](https://github.com/dc-tec/openbao-operator/commit/7453e23880b1edbfa0c825d6982c29893d4ac08d))
* **upgrade:** upgrade manager; blue/green upgrades ([2ba56a4](https://github.com/dc-tec/openbao-operator/commit/2ba56a426caa12a79a069700b0b2a4ede44156e1))

## 0.1.0

Initial release.

### Highlights

- Core OpenBao operator (controller + provisioner).
- Helm chart and install manifests (including CRDs).
- Backup/restore and upgrade workflows (including rolling and blue/green).
- Admission and supply-chain guardrails for hardened environments.
- E2E suite and CI pipelines for multi-Kubernetes validation.
