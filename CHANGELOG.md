# Changelog

## [1.0.0](https://github.com/Charg/technitium-kube-operator/compare/v0.1.0...v1.0.0) (2026-09-18)


### ⚠ BREAKING CHANGES

* target Zones at a managed instance via serverRef ([#48](https://github.com/Charg/technitium-kube-operator/issues/48))

### Features

* **api:** add TechnitiumCluster CRD scaffolding ([#41](https://github.com/Charg/technitium-kube-operator/issues/41)) ([#45](https://github.com/Charg/technitium-kube-operator/issues/45)) ([2b2de35](https://github.com/Charg/technitium-kube-operator/commit/2b2de3501581b38f96961a341cdfd3d700464e0d))
* **blocklist:** manage block lists and allow/block overrides via serverRef ([#64](https://github.com/Charg/technitium-kube-operator/issues/64)) ([4ba8c92](https://github.com/Charg/technitium-kube-operator/commit/4ba8c9211f7980c668b0130b1221af305288adc2))
* **ci:** added provenance to images/chart ([6a22657](https://github.com/Charg/technitium-kube-operator/commit/6a22657f1a0f86e124544259416bedeb5ff6b3c8))
* **ci:** added release-please ([9c3f014](https://github.com/Charg/technitium-kube-operator/commit/9c3f014a45a889fb0cdbfcf6d0c6492c64a31c7a))
* **ci:** added renovate config ([8fd36b0](https://github.com/Charg/technitium-kube-operator/commit/8fd36b0d6bf4ddacd75680c52935a4f3738b179c))
* **ci:** init envrc ([2c40bf6](https://github.com/Charg/technitium-kube-operator/commit/2c40bf60681368d93d734b39b3fd5722b64c505c))
* **cluster:** finalizer teardown and PVC retention policy ([#57](https://github.com/Charg/technitium-kube-operator/issues/57)) ([6182c2f](https://github.com/Charg/technitium-kube-operator/commit/6182c2faa364dc4fb0ff5d3e217986db84c6c0dc))
* **cluster:** orchestrate cluster init/join across provisioned pods ([#56](https://github.com/Charg/technitium-kube-operator/issues/56)) ([17197b1](https://github.com/Charg/technitium-kube-operator/commit/17197b1415c25ff9dda07f78156be8b258dc0d10))
* **cluster:** per-node addressing and cluster status scaffolding ([#54](https://github.com/Charg/technitium-kube-operator/issues/54)) ([d40e2c5](https://github.com/Charg/technitium-kube-operator/commit/d40e2c5a67f7431edb11bc25295f4760002f71d6))
* **cluster:** provision spec.replicas independent instances ([#55](https://github.com/Charg/technitium-kube-operator/issues/55)) ([a98e849](https://github.com/Charg/technitium-kube-operator/commit/a98e849f67469f651ee0d103ce1d374f3f01ce69))
* **config:** wire manager connection to Technitium server ([#25](https://github.com/Charg/technitium-kube-operator/issues/25)) ([51215b3](https://github.com/Charg/technitium-kube-operator/commit/51215b3f8bc095f6612ccff2c3bd66896955f4ac))
* **controller:** finalizer-based zone deletion with orphan option ([#33](https://github.com/Charg/technitium-kube-operator/issues/33)) ([dab6972](https://github.com/Charg/technitium-kube-operator/commit/dab6972b683ee95447cbc481935f1ba6eee6670d))
* **controller:** first-boot bootstrap and Ready gating ([#43](https://github.com/Charg/technitium-kube-operator/issues/43)) ([#47](https://github.com/Charg/technitium-kube-operator/issues/47)) ([38ac9c6](https://github.com/Charg/technitium-kube-operator/commit/38ac9c6fa1a367417be71751b1998332689afaa8))
* **controller:** implement Zone create/update reconciliation ([#28](https://github.com/Charg/technitium-kube-operator/issues/28)) ([44d7ed0](https://github.com/Charg/technitium-kube-operator/commit/44d7ed0c6e94b9ead5728d37c1f070c276c340db))
* **controller:** provision TechnitiumCluster workload ([#42](https://github.com/Charg/technitium-kube-operator/issues/42)) ([#46](https://github.com/Charg/technitium-kube-operator/issues/46)) ([0a74463](https://github.com/Charg/technitium-kube-operator/commit/0a7446323b7871b15c9378d65d1a449781d06b29))
* **controller:** report Ready/Progressing/Degraded conditions and printer columns ([#31](https://github.com/Charg/technitium-kube-operator/issues/31)) ([7caecaa](https://github.com/Charg/technitium-kube-operator/commit/7caecaa45cd94dc1a95e939b802c3797a317e6e4))
* **dhcp:** manage DHCP scopes and reservations per serverRef ([#73](https://github.com/Charg/technitium-kube-operator/issues/73)) ([421d236](https://github.com/Charg/technitium-kube-operator/commit/421d23639a943336b83078b1b7031cd0aa6349f9)), closes [#68](https://github.com/Charg/technitium-kube-operator/issues/68)
* **dnsapp:** install and configure DNS apps per serverRef ([#72](https://github.com/Charg/technitium-kube-operator/issues/72)) ([e89c685](https://github.com/Charg/technitium-kube-operator/commit/e89c6856752884db426e5ea753037d890bb846b3)), closes [#67](https://github.com/Charg/technitium-kube-operator/issues/67)
* **dnssec:** sign and unsign zones per serverRef ([#70](https://github.com/Charg/technitium-kube-operator/issues/70)) ([4ad1762](https://github.com/Charg/technitium-kube-operator/commit/4ad17626c3284115a17ee108e7dd2b83e95815a3)), closes [#65](https://github.com/Charg/technitium-kube-operator/issues/65)
* init helm chart ([3baf18d](https://github.com/Charg/technitium-kube-operator/commit/3baf18da225d35f23a7f4f682f3355e31e6a4422))
* **record:** manage DNS records via serverRef ([#58](https://github.com/Charg/technitium-kube-operator/issues/58)) ([7db1760](https://github.com/Charg/technitium-kube-operator/commit/7db1760d62ff1588f6e3a750556d6eb86d394f65))
* **settings:** manage server settings via serverRef ([#63](https://github.com/Charg/technitium-kube-operator/issues/63)) ([c39a644](https://github.com/Charg/technitium-kube-operator/commit/c39a644923ef374594d1ee7d85ef950002403ea4))
* target Zones at a managed instance via serverRef ([#48](https://github.com/Charg/technitium-kube-operator/issues/48)) ([5df7ef7](https://github.com/Charg/technitium-kube-operator/commit/5df7ef7c8ed31344fc8327779d9fadd938e839fa))
* **technitium:** add Technitium DNS Server API client ([#17](https://github.com/Charg/technitium-kube-operator/issues/17)) ([49d9ef4](https://github.com/Charg/technitium-kube-operator/commit/49d9ef40810c7fa59c523aceff8e8988524aad85))
* **webhook:** validate Zone spec and default type via admission webhook ([#34](https://github.com/Charg/technitium-kube-operator/issues/34)) ([110442a](https://github.com/Charg/technitium-kube-operator/commit/110442a0e5c79bcc09986ba741b8d744461e6491))
* **zone:** define Zone CRD API types ([#15](https://github.com/Charg/technitium-kube-operator/issues/15)) ([49080ba](https://github.com/Charg/technitium-kube-operator/commit/49080bad41b8ea4c5ee0b1a423c6adf5a135bbc0))
* **zone:** forwarder DNSSEC validation and proxy support ([#71](https://github.com/Charg/technitium-kube-operator/issues/71)) ([d1eb66d](https://github.com/Charg/technitium-kube-operator/commit/d1eb66dcbe28e31ea46c4dc2f8f583d54acd106f)), closes [#66](https://github.com/Charg/technitium-kube-operator/issues/66)


### Bug Fixes

* **ci:** use just instead of make ([5953f48](https://github.com/Charg/technitium-kube-operator/commit/5953f489525f3c7abe856290fb9c3315e347998d))
* **cluster:** gate Ready on primary-reported convergence ([#60](https://github.com/Charg/technitium-kube-operator/issues/60)) ([4972d85](https://github.com/Charg/technitium-kube-operator/commit/4972d85e688bf801c58cfbdaad1314f737233c2f))
* **deps:** update go dependencies (non-major) ([#36](https://github.com/Charg/technitium-kube-operator/issues/36)) ([d95843e](https://github.com/Charg/technitium-kube-operator/commit/d95843e675678e89717d4bbe471217c08e38bced))
