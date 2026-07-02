# Changelog

## [0.63.0](https://github.com/d0ugal/graith/compare/v0.62.0...v0.63.0) (2026-07-02)


### Features

* add hidden gr man command to generate man pages ([ef3c37e](https://github.com/d0ugal/graith/commit/ef3c37e18fc2505e7dc7d3151b89e5b8e7760307))
* build and release-attach apk (Alpine) packages ([1d946af](https://github.com/d0ugal/graith/commit/1d946af150deb8f2e3054bf6dfa029154f140196))
* **ci:** prune old package versions from the apt/yum repo on release ([eecdd61](https://github.com/d0ugal/graith/commit/eecdd61a5cb19c1e7bd46a1d233b96a396810d45))
* scaffold guarded AUR (Arch) publishing ([8a014a4](https://github.com/d0ugal/graith/commit/8a014a462a52829a9d5fae225557f90a2aea3da0))

## [0.62.0](https://github.com/d0ugal/graith/compare/v0.61.0...v0.62.0) (2026-07-01)


### Features

* **ci:** publish signed apt/yum repo to graith-repo on release ([fb593f7](https://github.com/d0ugal/graith/commit/fb593f77b6132ebccea4f88aebe04fb81e7161af))

## [0.61.0](https://github.com/d0ugal/graith/compare/v0.60.1...v0.61.0) (2026-07-01)


### Features

* build .deb and .rpm packages via GoReleaser nfpm ([a4cb551](https://github.com/d0ugal/graith/commit/a4cb551a617224226869b9a82f180ae98172a536))
* make binaries and packages reproducible ([488e356](https://github.com/d0ugal/graith/commit/488e3562835417831a1903e6f3836c298e26bff6))


### Bug Fixes

* update module modernc.org/libc to v1.73.5 ([e7d18fd](https://github.com/d0ugal/graith/commit/e7d18fda5ffdf1111ac3f1ada92f7049bb2bd1a7))
* update module modernc.org/libc to v2 ([3d74720](https://github.com/d0ugal/graith/commit/3d747203492dcc9d21153da706e3c1ce15104a9e))

## [0.60.1](https://github.com/d0ugal/graith/compare/v0.60.0...v0.60.1) (2026-06-26)


### Bug Fixes

* update module modernc.org/libc to v1.73.5 ([a58bec5](https://github.com/d0ugal/graith/commit/a58bec567cda4dca360e8cec0f6fb00cceb6b735))

## [0.60.0](https://github.com/d0ugal/graith/compare/v0.59.1...v0.60.0) (2026-06-26)


### Features

* **overlay:** collapsible messages with focus-to-expand + timestamps ([13e36ca](https://github.com/d0ugal/graith/commit/13e36ca35ba02d53d15c0cc2454659cf4e490c6a))
* PR/CI in overlay column + status bar, and merge-conflict notifications ([7ad7794](https://github.com/d0ugal/graith/commit/7ad77948c3a6027cbe40e11c3e80c5ce2f5943d7))


### Bug Fixes

* address ship-it review findings (pr-ci-visibility) ([ffe5ac5](https://github.com/d0ugal/graith/commit/ffe5ac58f83dad033ee8d5640f4779912b29cc95))
* **overlay:** address collapsible-messages review findings ([f620a92](https://github.com/d0ugal/graith/commit/f620a9242c681308f257d1d0d90257daa8d727db))

## [0.59.1](https://github.com/d0ugal/graith/compare/v0.59.0...v0.59.1) (2026-06-25)


### Bug Fixes

* **daemon:** wait for exit watchers in StopAll to fix test flakiness ([cb97b4d](https://github.com/d0ugal/graith/commit/cb97b4dafbe1584b700d249463f1fc1354251ddc))

## [0.59.0](https://github.com/d0ugal/graith/compare/v0.58.0...v0.59.0) (2026-06-25)


### Features

* add PR & CI awareness with agent notifications ([034a7fd](https://github.com/d0ugal/graith/commit/034a7fd5540ef28439dc91732bc6a756056f040f))
* **overlay:** add message viewer overlay (ctrl+b m) ([77d954f](https://github.com/d0ugal/graith/commit/77d954f0d7b8698f7b40407b760b9de546b57735))
* **resume:** capture native agent session id to resume conversations ([355b392](https://github.com/d0ugal/graith/commit/355b3929e3840483b8a640ffdb8099c2b955380d))


### Bug Fixes

* address ship-it review findings (claude) ([42d00c0](https://github.com/d0ugal/graith/commit/42d00c05d5a777f603ec17f1dd66929d1c47fe5c))
* address ship-it review findings (codex) ([5da873c](https://github.com/d0ugal/graith/commit/5da873c873765a95895e09b144dcf18e44d2b031))
* **overlay:** address message-overlay review findings ([ec4a44e](https://github.com/d0ugal/graith/commit/ec4a44e527a190829a6d59267467ba0bd0645a11))
* **overlay:** avoid SELECT * in Conversation query (unqueryvet lint) ([28b9f92](https://github.com/d0ugal/graith/commit/28b9f929cec3e0c8c8586844614c5590cd7aae68))
* **resume:** wire Fork capture, dedupe migrate, harden id capture ([265d77d](https://github.com/d0ugal/graith/commit/265d77dad2d8a1f2c57a8ca7b30d015556fba4ea))
* satisfy gocritic appendAssign in pr-watch ([91053c4](https://github.com/d0ugal/graith/commit/91053c422f07e13f4d9f0a88a563fa2709b07b98))

## [0.58.0](https://github.com/d0ugal/graith/compare/v0.57.0...v0.58.0) (2026-06-25)


### Features

* add gr migrate for cross-agent conversation take-over ([dd9f1f4](https://github.com/d0ugal/graith/commit/dd9f1f458786cabccc8f6fa4d3f7574542ac3e75))
* **overlay:** add agent-type selector to create-session form ([35b337d](https://github.com/d0ugal/graith/commit/35b337d246dabb6699c81ab7be32449f4c8702cc))

## [0.57.0](https://github.com/d0ugal/graith/compare/v0.56.1...v0.57.0) (2026-06-24)


### Features

* **overlay:** add stop action and expand restart into a menu ([85f4c9d](https://github.com/d0ugal/graith/commit/85f4c9d2855fd9dbb699474fec5cf48aceb54f11))


### Bug Fixes

* address tribunal review of MCP isolation ([c806a6a](https://github.com/d0ugal/graith/commit/c806a6ae7e62d563053396f0779a518484410a9f))
* authorize the update handler to prevent reparenting bypass ([264090c](https://github.com/d0ugal/graith/commit/264090c8187eae01588ba4b2e0ca38c71ab83d98)), closes [#568](https://github.com/d0ugal/graith/issues/568)
* grant orchestrator elevated privileges to manage all sessions ([ac9f039](https://github.com/d0ugal/graith/commit/ac9f0392d1682de2b91b8ba2aed3b71ef37737b6)), closes [#566](https://github.com/d0ugal/graith/issues/566)
* isolate per-session MCP processes via template expansion ([d6203a6](https://github.com/d0ugal/graith/commit/d6203a60902447af8356f98ac3a4d501cbe1a6bc)), closes [#571](https://github.com/d0ugal/graith/issues/571)
* restrict parent-clearing to orchestrator and human CLI ([35f6edb](https://github.com/d0ugal/graith/commit/35f6edb497cdc3324ef79ebebe96508470030d71))

## [0.56.1](https://github.com/d0ugal/graith/compare/v0.56.0...v0.56.1) (2026-06-24)


### Bug Fixes

* avoid data race in TestForkUsesSourceBaseBranch cleanup ([ed42343](https://github.com/d0ugal/graith/commit/ed42343a8dfbc2df617199f962b77535211f0474))
* tolerate already-exited PTY in TestTypeExitedSessionFails ([4663534](https://github.com/d0ugal/graith/commit/4663534492b1cfccacd6869fbb99a26d56ac0182))

## [0.56.0](https://github.com/d0ugal/graith/compare/v0.55.0...v0.56.0) (2026-06-24)


### Features

* add number key shortcuts to session select overlay ([8f24ab1](https://github.com/d0ugal/graith/commit/8f24ab1513edef284efa55b806d75adf13bd34da))
* make overlay shortcut keys configurable via shortcut_keys setting ([496cbf4](https://github.com/d0ugal/graith/commit/496cbf4cdca207b31446eb7c2405b521dfe119bc))

## [0.55.0](https://github.com/d0ugal/graith/compare/v0.54.1...v0.55.0) (2026-06-24)


### Features

* add number key shortcuts to session select overlay ([2deaa74](https://github.com/d0ugal/graith/commit/2deaa74f4ad849526b939d93f09bcf36e0ee5ebe))


### Bug Fixes

* improve number key labels and strengthen overlay tests ([6e27823](https://github.com/d0ugal/graith/commit/6e27823ce0551f7ce33f7545bc4fc91fb384f86d))
* update module github.com/pelletier/go-toml/v2 to v2.4.2 ([635c7dd](https://github.com/d0ugal/graith/commit/635c7dd1a3f5efae11eb54c02eeaf24c696e51ad))

## [0.54.1](https://github.com/d0ugal/graith/compare/v0.54.0...v0.54.1) (2026-06-21)


### Bug Fixes

* set explicit cwd for MCP server processes to survive worktree deletion ([6670fb5](https://github.com/d0ugal/graith/commit/6670fb58cfda0bcfeb9edd2f1ac763ae3bad4d27))

## [0.54.0](https://github.com/d0ugal/graith/compare/v0.53.1...v0.54.0) (2026-06-21)


### Features

* add first-class inbox messaging with auto-resume ([da10ebe](https://github.com/d0ugal/graith/commit/da10ebed2bc07cfd23316331e0f3328a5f5ff09a))


### Bug Fixes

* resolve HOME and CWD for daemon git-pull subprocess ([169aa16](https://github.com/d0ugal/graith/commit/169aa163649e0b12c79d85345e3bf35ce336b4c7)), closes [#551](https://github.com/d0ugal/graith/issues/551)
* return error on UserHomeDir failure, use t.Chdir in test ([514f351](https://github.com/d0ugal/graith/commit/514f3515eff83a8e9baa9af9632efa536f9c3100))

## [0.53.1](https://github.com/d0ugal/graith/compare/v0.53.0...v0.53.1) (2026-06-20)


### Bug Fixes

* update module github.com/pelletier/go-toml/v2 to v2.4.1 ([1c3474f](https://github.com/d0ugal/graith/commit/1c3474faa6304749dfb1246f58f9b116a7d17937))

## [0.53.0](https://github.com/d0ugal/graith/compare/v0.52.1...v0.53.0) (2026-06-18)


### Features

* add `gr restart --children` to restart all descendant sessions ([6e4d381](https://github.com/d0ugal/graith/commit/6e4d381c9d622c4d936e02882b532b3bf9821635)), closes [#481](https://github.com/d0ugal/graith/issues/481)


### Bug Fixes

* address review tribunal findings for restart --children ([fd1544c](https://github.com/d0ugal/graith/commit/fd1544c9e5ac64b2b2eda76699c6863b06083538))
* address tribunal findings — async notify and correct inbox stream ([cfd5871](https://github.com/d0ugal/graith/commit/cfd58711b8547e04b52d08000b7f899f47cc74ab))
* also clear ParentID in Delete's StatusCreating early-return path ([91ffbc0](https://github.com/d0ugal/graith/commit/91ffbc049d88203256effa2c246c198597c14433))
* clear dangling ParentID on children when a session is deleted ([4597add](https://github.com/d0ugal/graith/commit/4597add859646346e2bf9eedf008abbdf9b83b4e))
* move inbox notification from client-side to daemon-side ([8fdba44](https://github.com/d0ugal/graith/commit/8fdba4414f205dbd3d390e14d2af1ff970cbda49))
* re-read parentID under lock to prevent stale reparenting ([b4afe08](https://github.com/d0ugal/graith/commit/b4afe0840780189d1f4da52833b42023a651f53d))
* remove dead enrichment data code from hook stdin path ([8d16591](https://github.com/d0ugal/graith/commit/8d1659179e02807e93fbf1d30ae171cdea1503fb)), closes [#534](https://github.com/d0ugal/graith/issues/534)
* reparent children to grandparent when a session is deleted ([9de9006](https://github.com/d0ugal/graith/commit/9de90061ef6cbc1dea6b6714eb5849a7f3b7f263)), closes [#522](https://github.com/d0ugal/graith/issues/522)

## [0.52.1](https://github.com/d0ugal/graith/compare/v0.52.0...v0.52.1) (2026-06-17)


### Bug Fixes

* allow direct messaging between any authenticated sessions ([6242727](https://github.com/d0ugal/graith/commit/624272727c78b638d697b45869ddcf8efbc0a4c5)), closes [#536](https://github.com/d0ugal/graith/issues/536)
* make msg send notification best-effort for cross-tree targets ([3446a20](https://github.com/d0ugal/graith/commit/3446a20c8bd317568852d9b5470d063f2b203721))
* make TestTypeWakesSleepingAgent reliable on macOS ([563a8ee](https://github.com/d0ugal/graith/commit/563a8eea49c4cdb2f1e78b93bfa4a86d06bfbb14))

## [0.52.0](https://github.com/d0ugal/graith/compare/v0.51.0...v0.52.0) (2026-06-17)


### Features

* add `gr update` command for session property mutations ([e161631](https://github.com/d0ugal/graith/commit/e161631bfa1895b84242fa8b89f9def78ae113e5))
* add create-session form to overlay and ctrl+b c ([e521853](https://github.com/d0ugal/graith/commit/e5218538a1f1fef390e565ab43c64bf23730d5a4))
* add per-session token auth to prevent agent impersonation ([a9c9a6b](https://github.com/d0ugal/graith/commit/a9c9a6b598c33845d5a01deead741807e0c61778))
* add scenarios for declarative multi-session orchestration ([778b9a0](https://github.com/d0ugal/graith/commit/778b9a0dcccb26f2962c92025614f5a23a8dfd11))
* idle-timeout for gr type when user is attached and typing ([b3d9dee](https://github.com/d0ugal/graith/commit/b3d9dee06ca57093fc26bb5af58338bbfb4524af))
* implement scenario Phase 2 — resume, task-done, add, parallel creation ([2c72497](https://github.com/d0ugal/graith/commit/2c72497a6dd73d25c301b9c66f1cccf37bb9ffb4))


### Bug Fixes

* address final tribunal findings for scenarios ([661e331](https://github.com/d0ugal/graith/commit/661e331db8dc3fc16755d1e2ad2dbae525f5ae56))
* address review tribunal findings for agent auth ([a5c92c6](https://github.com/d0ugal/graith/commit/a5c92c63cc6b8693a3e6884912ff7c839b2d47b4))
* address tribunal findings for scenarios feature ([ebd91a4](https://github.com/d0ugal/graith/commit/ebd91a4dc1ad6b8333ebca6150aca4b9d9794063))
* address tribunal findings in idle-timeout implementation ([91634c9](https://github.com/d0ugal/graith/commit/91634c9d37ee4bcf654ff9089be5e7a7e4a3573f))
* make TestStopAllWaitsConcurrently reliable on macOS ([4411b30](https://github.com/d0ugal/graith/commit/4411b30882ce3eaf309bfa3232ba10f7784c908f))
* prevent data race on SessionIDs in StopScenario and DeleteScenario ([207198a](https://github.com/d0ugal/graith/commit/207198a3af8ccf2c0320dec27ebb3d7a85fd2090))
* resolve data race in scenario CLI tests ([7b9d782](https://github.com/d0ugal/graith/commit/7b9d78236e067ef86b63a8cbd9a3d54a97e44e88))
* resolve golangci-lint warnings in overlay and createinput ([33b0bd6](https://github.com/d0ugal/graith/commit/33b0bd6c188220f80507ce2b11fd079ae5f1609d))
* resolve symlinks in test path comparison for macOS CI ([d3ec1be](https://github.com/d0ugal/graith/commit/d3ec1beabe9ea43f052a67e48377760d572bb070))
* resolve tribunal findings and add shared session support for scenarios ([d6f6b01](https://github.com/d0ugal/graith/commit/d6f6b01552c72286445395fd874d512257cc5347))
* resolve tribunal R2 findings and add scenario overlay grouping ([a215f1f](https://github.com/d0ugal/graith/commit/a215f1fd6f73d7914a9b780194bd7883d9c5fc8f))
* resolve tribunal R3 findings in overlay scenario view ([99b1dfd](https://github.com/d0ugal/graith/commit/99b1dfdc48a0bb83eba621ca6f47e8ac4c94783d))
* space-to-dash Text field, add form interaction tests, apply tribunal fixes ([508d965](https://github.com/d0ugal/graith/commit/508d965923d9c82ec148ec02040c07241e67c70d))
* update github.com/charmbracelet/ultraviolet digest to f39628c ([0d2ec53](https://github.com/d0ugal/graith/commit/0d2ec533342f4e17ba8e0f40598d27c6889ccfa8))
* update module modernc.org/sqlite to v1.53.0 ([0608cb9](https://github.com/d0ugal/graith/commit/0608cb9df23bf3ebc514b2f9b923d675554b1393))

## [0.51.0](https://github.com/d0ugal/graith/compare/v0.50.0...v0.51.0) (2026-06-12)


### Features

* improve crash diagnostics with signal detection, mass-exit warnings, and peak RSS ([dc70074](https://github.com/d0ugal/graith/commit/dc7007448c27395726d612588b4c225e42b7c9f0)), closes [#519](https://github.com/d0ugal/graith/issues/519)


### Bug Fixes

* only count crash exits toward mass-exit detection ([ab29b3a](https://github.com/d0ugal/graith/commit/ab29b3ae1e1155f86776942a64d5f430ee0c67fd))

## [0.50.0](https://github.com/d0ugal/graith/compare/v0.49.0...v0.50.0) (2026-06-09)


### Features

* add `gr path` command to print session worktree path ([44ba0c0](https://github.com/d0ugal/graith/commit/44ba0c0adb1085c86f7284a20a3f94de76a97c5c))


### Bug Fixes

* allow same-version daemon restart to preserve sessions ([6008655](https://github.com/d0ugal/graith/commit/6008655ae7d5b67a0978ceedda695a17c9661bd6))

## [0.49.0](https://github.com/d0ugal/graith/compare/v0.48.0...v0.49.0) (2026-06-06)


### Features

* add --skip-model-validation flag to gr new ([dafa8e0](https://github.com/d0ugal/graith/commit/dafa8e01c1d6c709ca3b706bf37a0e6c1835a240))


### Bug Fixes

* stop auto-restarting orchestrator on sandbox config reload ([f935015](https://github.com/d0ugal/graith/commit/f93501529c5b593ced2b6e2e9b77ec94b02c470c))
* use non-existent command in skip-validation test to avoid TempDir cleanup race ([d1168b7](https://github.com/d0ugal/graith/commit/d1168b73d4722c3b91e95d187ea00eb62e8ce9e2))

## [0.48.0](https://github.com/d0ugal/graith/compare/v0.47.0...v0.48.0) (2026-06-04)


### Features

* auto-resume stopped/errored sessions on attach ([81f2094](https://github.com/d0ugal/graith/commit/81f2094fec71b833a92d9585773ba33359b291b3))


### Bug Fixes

* address tribunal findings in sweep loops ([e068d2b](https://github.com/d0ugal/graith/commit/e068d2b9399bad9274466809ab9e589ad1eb8011))
* align Agent struct tags for tagalign linter ([617edd1](https://github.com/d0ugal/graith/commit/617edd154ef3474c7975064f27b0724959ce7c07))
* make gr msg send --children recursive and deduplicate descendant walk ([d41e433](https://github.com/d0ugal/graith/commit/d41e433b9d4f1291dc0e5a1ff3848013e6e1c5b9)), closes [#506](https://github.com/d0ugal/graith/issues/506)
* pre-trust cursor workspaces to prevent concurrent cli-config.json corruption ([70a9e30](https://github.com/d0ugal/graith/commit/70a9e302706d895aad95bcc2f16763a67f2fc1a2))
* prevent stale attached client from killing upgraded daemon ([645a94b](https://github.com/d0ugal/graith/commit/645a94b6d3716adcfa469b4b47e4b30c9a7241d9))
* sweep for late-arriving descendants in DeleteWithChildren and StopWithChildren ([543e758](https://github.com/d0ugal/graith/commit/543e7581efbe8c418ae6cafd0a189e7ae1137973))
* use status-neutral log message for auto-resume on attach ([685972e](https://github.com/d0ugal/graith/commit/685972e52122545b1f2bb0c1d1b429b350ae9ea8))

## [0.47.0](https://github.com/d0ugal/graith/compare/v0.46.0...v0.47.0) (2026-06-04)


### Features

* validate protocol version on handshake ([a203719](https://github.com/d0ugal/graith/commit/a203719f5c4bbbaf89267401f095017b9bae9a0b)), closes [#50](https://github.com/d0ugal/graith/issues/50)


### Bug Fixes

* block --config flag inside graith sessions to prevent sandbox bypass ([c4d4b37](https://github.com/d0ugal/graith/commit/c4d4b3752eb02e9822193e91c2c1ecb0d5c61c02)), closes [#331](https://github.com/d0ugal/graith/issues/331)
* close PTY/log handles and remove stale map entries on session exit ([d4832f0](https://github.com/d0ugal/graith/commit/d4832f0c899339b0fe668333accf12fa009f8da8)), closes [#220](https://github.com/d0ugal/graith/issues/220)
* eliminate torn frames from two-write pattern in FrameWriter ([1c002c4](https://github.com/d0ugal/graith/commit/1c002c48590ac7bd87024deffb59faf0ba1e3c22)), closes [#212](https://github.com/d0ugal/graith/issues/212)
* prevent adoptedWaitLoop from hanging on PID reuse ([4de8791](https://github.com/d0ugal/graith/commit/4de879145689c9d6ba0ccc6b0eee50237ede4ceb)), closes [#253](https://github.com/d0ugal/graith/issues/253)
* protect sm.cfg reads from data races with applyConfig ([3183d62](https://github.com/d0ugal/graith/commit/3183d627136b297d6b4f0e819fd9d5ceca451981)), closes [#214](https://github.com/d0ugal/graith/issues/214)
* reject paths containing colons in sandbox Wrap ([f273677](https://github.com/d0ugal/graith/commit/f2736772d68199582b58ddb064a15e503876b1d2)), closes [#226](https://github.com/d0ugal/graith/issues/226)
* replace single attachedWriter with writer slice for multi-subscriber support ([54cb4a8](https://github.com/d0ugal/graith/commit/54cb4a88e6f7a8b2246b4a9920983953ab716fbf)), closes [#219](https://github.com/d0ugal/graith/issues/219)
* stop sessions concurrently in StopAll and use fresh shutdown context ([cf86abe](https://github.com/d0ugal/graith/commit/cf86abee4ad0240deffdd69e58325b05e1d57a4a)), closes [#229](https://github.com/d0ugal/graith/issues/229)
* use lock-free reads in Scrollback for concurrent access ([11579d3](https://github.com/d0ugal/graith/commit/11579d364934d8ad0f3fb1b25dff79895fc37a97)), closes [#242](https://github.com/d0ugal/graith/issues/242)

## [0.46.0](https://github.com/d0ugal/graith/compare/v0.45.0...v0.46.0) (2026-06-02)


### Features

* add configurable sandbox access for orchestrator sessions ([4a14e73](https://github.com/d0ugal/graith/commit/4a14e73d098c4bc48b58c9b47448e510a6f9456f))
* hide git info in overlay for shared worktree sessions ([5c79df5](https://github.com/d0ugal/graith/commit/5c79df52e05407e92127754efab8e6f541a08b46))


### Bug Fixes

* address round 1 code review tribunal findings ([c643a52](https://github.com/d0ugal/graith/commit/c643a5273f5bcb4ed6369c5c595a7c5231244779))
* address round 1 tribunal findings on shared worktree design doc ([f35087e](https://github.com/d0ugal/graith/commit/f35087eda515cbcc8f8f816848194d0cb012f6ee))

## [0.45.0](https://github.com/d0ugal/graith/compare/v0.44.0...v0.45.0) (2026-06-01)


### Features

* add automatic lifecycle status updates to sessions ([ba8922d](https://github.com/d0ugal/graith/commit/ba8922d7e7c03d8c6d4f574c089c6a8ffc0062d8))
* attach directly from overlay filter with single Enter ([be350dc](https://github.com/d0ugal/graith/commit/be350dc9940b5111bdcf61eec2c677193426a9f9))


### Bug Fixes

* address all 14 tribunal findings in lifecycle status design doc ([3d8a84c](https://github.com/d0ugal/graith/commit/3d8a84c110c09e07667913a7574764a3bf58614b))
* address golangci-lint findings in lifecycle code ([d69fd8f](https://github.com/d0ugal/graith/commit/d69fd8f462dde8c4d2bd91f5d0e9db2b1aeba43e))
* address round 2 review findings in lifecycle status design doc ([b7f05b1](https://github.com/d0ugal/graith/commit/b7f05b126bcbb2dee049a521c8c7a6d7dd540c2c))

## [0.44.0](https://github.com/d0ugal/graith/compare/v0.43.0...v0.44.0) (2026-06-01)


### Features

* add auto-refresh to overlay session list ([de35087](https://github.com/d0ugal/graith/commit/de350879c25d6b6f99d13a01e66212ef993250fd))
* block nested graith sessions ([5b497df](https://github.com/d0ugal/graith/commit/5b497dfaff1389d7cf46a97aa450de51f68ff56c))
* stagger restart-all to keep overlay responsive ([6504bb0](https://github.com/d0ugal/graith/commit/6504bb0105eff423fbd0f374043a36cff41057a6))

## [0.43.0](https://github.com/d0ugal/graith/compare/v0.42.0...v0.43.0) (2026-05-31)


### Features

* add configurable prompt field to orchestrator config ([54aaae4](https://github.com/d0ugal/graith/commit/54aaae4068a3425c8899ec7efd338b001ca6ea30))

## [0.42.0](https://github.com/d0ugal/graith/compare/v0.41.1...v0.42.0) (2026-05-29)


### Features

* add orchestrator session — daemon-managed singleton agent ([ee32d90](https://github.com/d0ugal/graith/commit/ee32d90138bd3cebe2406f67e7127bf5816df66d))
* add periodic git pull for maintenance repos ([4516cce](https://github.com/d0ugal/graith/commit/4516cce42200267c14fac82f28de78ec63fd344e))


### Bug Fixes

* address review tribunal findings for git-pull feature ([a8343dc](https://github.com/d0ugal/graith/commit/a8343dc4d81da4f605b25c0f08fd8ba8a5308bd9))
* address round 1 review findings ([e1559d4](https://github.com/d0ugal/graith/commit/e1559d48c3af70f590bcff2cffebdb604783bad5))
* address round 2 review findings ([05d139d](https://github.com/d0ugal/graith/commit/05d139d4794819ddbd5c3105ab68ea05b8f4f1b3))
* address round 2 review findings for git-pull ([9f14c95](https://github.com/d0ugal/graith/commit/9f14c95a01dc71e57254cbd83a46f19f8e0f1523))
* align leaf node indentation with parent nodes in session picker ([0ab8306](https://github.com/d0ugal/graith/commit/0ab830666e99d2acaf3673950fb18ed8ebcd621c))
* improve overlay height utilization and preserve preview beside panel ([724faba](https://github.com/d0ugal/graith/commit/724faba61f086f1d587f7c4d58ab381c651f7c27))
* remove ineffectual assignments flagged by golangci-lint ([f9acc89](https://github.com/d0ugal/graith/commit/f9acc897a19c98da86f836b75df17c0f59edfb65))
* resolve symlinks in active session check and fix lint ([4fa98de](https://github.com/d0ugal/graith/commit/4fa98de44891cd06795cc4141f1703d7a0a1b677))

## [0.41.1](https://github.com/d0ugal/graith/compare/v0.41.0...v0.41.1) (2026-05-29)


### Bug Fixes

* improve validate_model error reporting and output parsing ([bd04d45](https://github.com/d0ugal/graith/commit/bd04d4531de80b887eb8dd0e7c0092aa55a6d0b4))

## [0.41.0](https://github.com/d0ugal/graith/compare/v0.40.0...v0.41.0) (2026-05-28)


### Features

* add collapse/expand for child sessions in overlay picker ([b755dfc](https://github.com/d0ugal/graith/commit/b755dfc935936eab94269285c21da664ed2fbc8e))


### Bug Fixes

* address tribunal review findings for collapse feature ([ca0f572](https://github.com/d0ugal/graith/commit/ca0f572ac058d5a4eb575e1e60b11416a302f2c6))
* create shared store directory at startup ([e0db5b4](https://github.com/d0ugal/graith/commit/e0db5b4fb9f2b8ee56aa913b342e2392fa5afc87))
* cursor falls back to ancestor when currentSessionID is collapsed ([8639190](https://github.com/d0ugal/graith/commit/8639190a5755ae2682ab1c014c3fbbf38b851c46))
* prevent upgrade storm and socket deletion race ([6db6749](https://github.com/d0ugal/graith/commit/6db67495036edc420fa69a04c7a508fe1dd79cef))

## [0.40.0](https://github.com/d0ugal/graith/compare/v0.39.0...v0.40.0) (2026-05-28)


### Features

* add --shared store examples to agent prompt ([252adff](https://github.com/d0ugal/graith/commit/252adffe5c2b59a565a27659c3fcca3f777f2f57))
* add shared store and repo column to store ls ([eeb7548](https://github.com/d0ugal/graith/commit/eeb7548b8573c62f5dc3a9b93fefc184b1fb7aeb))


### Bug Fixes

* address tribunal review findings for share→tmp rename ([e71aa54](https://github.com/d0ugal/graith/commit/e71aa54dab00d27895d51eb09515bc9e0fc07988))
* address tribunal review findings for shared store ([a2b7bc5](https://github.com/d0ugal/graith/commit/a2b7bc57edd2845e040489d15fe522f9598262d2))
* error when --shared and --repo are both provided ([769fe70](https://github.com/d0ugal/graith/commit/769fe7038272653b3a2aa8344aa141d5340a5161))
* prevent stale mcp-proxy and mcp server from killing daemon on restart ([89b50c2](https://github.com/d0ugal/graith/commit/89b50c2be739fef8f527c733286afa90ce765fa7))

## [0.39.0](https://github.com/d0ugal/graith/compare/v0.38.0...v0.39.0) (2026-05-27)


### Features

* rename share directory to tmp, GRAITH_SHARE_PATH to GRAITH_TMPDIR ([41ce4b6](https://github.com/d0ugal/graith/commit/41ce4b6b247d5fd596a468888a9bd7b8010700d8))


### Bug Fixes

* update module github.com/pelletier/go-toml/v2 to v2.4.0 ([05992f0](https://github.com/d0ugal/graith/commit/05992f0bf2783d7b666414094b3f22d07518ac1b))

## [0.38.0](https://github.com/d0ugal/graith/compare/v0.37.0...v0.38.0) (2026-05-25)


### Features

* add --all/-a flag to gr store list for cross-repo listing ([b229b3f](https://github.com/d0ugal/graith/commit/b229b3f0d668771029f251164275e7d2896c6adc))

## [0.37.0](https://github.com/d0ugal/graith/compare/v0.36.0...v0.37.0) (2026-05-24)


### Features

* handle restart in picker overlay, add R for restart-all ([d42672c](https://github.com/d0ugal/graith/commit/d42672cc75fade345fc8bd0f52e0fa1ea88ab030))


### Bug Fixes

* skip non-existent sandbox dirs instead of failing session creation ([69cb1a6](https://github.com/d0ugal/graith/commit/69cb1a69314881ed0a2aaa9728238da46b7611f6))

## [0.36.0](https://github.com/d0ugal/graith/compare/v0.35.0...v0.36.0) (2026-05-23)


### Features

* add gr store append command for line-oriented data ([5044149](https://github.com/d0ugal/graith/commit/504414914791037315f70a5390bc040be891bd4d))
* add powerline-style status bar separators ([6574661](https://github.com/d0ugal/graith/commit/657466100dce03b88b2328b335a0482af82b1472))


### Bug Fixes

* case-insensitive .git and store.lock validation in store keys ([cadf891](https://github.com/d0ugal/graith/commit/cadf891974f8cb8b1106e485507b347a701d600a))
* init store dir at session creation for sandbox access ([#452](https://github.com/d0ugal/graith/issues/452)) ([2d1ed93](https://github.com/d0ugal/graith/commit/2d1ed93c6c8e587313cfe8c0abcda46c39f02960))

## [0.35.0](https://github.com/d0ugal/graith/compare/v0.34.0...v0.35.0) (2026-05-23)


### Features

* add store Init with git repo setup ([8192ea6](https://github.com/d0ugal/graith/commit/8192ea6fcc3ac90bf0c514baf6f4e14a009345c6))
* add store List and Remove with empty parent cleanup ([e6894c0](https://github.com/d0ugal/graith/commit/e6894c0ec3b03b3453a5205356eab2e92da869ae))
* add store package with key validation and path helpers ([9cd4ea1](https://github.com/d0ugal/graith/commit/9cd4ea1f599000924cabef555632950dcc77fb15))
* add store Put, Get, CommitMessage with file locking ([7626baa](https://github.com/d0ugal/graith/commit/7626baa5f72150a439ecdf47fecfa5d25db9deba))
* list all stores when no repo context is available ([7923204](https://github.com/d0ugal/graith/commit/79232046dfe7954a0ec12e1385c132a654403770))
* make agent prompt configurable via config ([af3bf0f](https://github.com/d0ugal/graith/commit/af3bf0f697b7c6829a2f5f1896b43354936a3465))
* rewrite store CLI to use flat files instead of daemon ([37cb732](https://github.com/d0ugal/graith/commit/37cb73214f836db2e376ebba1a5288339af1ccee))


### Bug Fixes

* address tribunal review findings for store refactor ([2d73b7b](https://github.com/d0ugal/graith/commit/2d73b7b7d7102c40eb410a49723009084bd99a77))
* use switch for agent prompt check in doctor (gocritic) ([0245fcb](https://github.com/d0ugal/graith/commit/0245fcb02835de8d5f2b3f82cc67708d67bf1e92))

## [0.34.0](https://github.com/d0ugal/graith/compare/v0.33.1...v0.34.0) (2026-05-21)


### Features

* add per-repo share directory with TMPDIR override ([4fcb37b](https://github.com/d0ugal/graith/commit/4fcb37be6394c141ddd52e181a6092615e9dd4d5))


### Bug Fixes

* address tribunal review findings for share directory ([d4f4c83](https://github.com/d0ugal/graith/commit/d4f4c83783ef25ccd61771ae58a70b4665867e82))

## [0.33.1](https://github.com/d0ugal/graith/compare/v0.33.0...v0.33.1) (2026-05-21)


### Bug Fixes

* use config.ResolvePath for store --repo flag and check SendControl error ([5e4e556](https://github.com/d0ugal/graith/commit/5e4e5565add37d6cd64c143f3b1c1e98b9a2a98f))

## [0.33.0](https://github.com/d0ugal/graith/compare/v0.32.0...v0.33.0) (2026-05-21)


### Features

* add DocStore SQLite backend for shared document storage ([a105eec](https://github.com/d0ugal/graith/commit/a105eecdd6a115a0ad393dc1a604c387eed0f053))
* add gr store put/get/list/rm CLI commands ([750bfbd](https://github.com/d0ugal/graith/commit/750bfbd6dc9f34c40d0df7b3aca5117d6e7864dd))
* add store protocol message types ([124b089](https://github.com/d0ugal/graith/commit/124b089e4adb8d008463bbf18d55c2c89da45ee5))
* wire DocStore into daemon handler and startup ([3f65627](https://github.com/d0ugal/graith/commit/3f656274846a3369a1d698d595ee50541134a584))


### Bug Fixes

* address code quality review for docstore ([60d02d9](https://github.com/d0ugal/graith/commit/60d02d959b04ac7114d09677bebc939cff2543b5))
* address tribunal review findings for document store ([16af6b0](https://github.com/d0ugal/graith/commit/16af6b040b5099423cfb5d90e92fd36fa379ac9d))

## [0.32.0](https://github.com/d0ugal/graith/compare/v0.31.0...v0.32.0) (2026-05-21)


### Features

* add gr status CLI command ([32337e6](https://github.com/d0ugal/graith/commit/32337e63bc74170f5604f803be846143bee2001a))
* add set_status handler and SetSummary/ClearSummary methods ([fee8e1d](https://github.com/d0ugal/graith/commit/fee8e1d53926943169763a349dbdf523bfcbb0b2))
* add StatusConfig with TTL duration parsing ([1dd772f](https://github.com/d0ugal/graith/commit/1dd772f270963c94ea1405f8e74b72e687657583))
* add summary status fields to SessionInfo and SetStatusMsg ([25ba2be](https://github.com/d0ugal/graith/commit/25ba2bea46f5dba20aeb2dea408fb6b32f85215b))
* add summary status fields to SessionState (v7 migration) ([b05751c](https://github.com/d0ugal/graith/commit/b05751cc08c294627e01aec0d3a7af92d9110f63))
* auto-inject graith prompt into agent sessions ([ceacd83](https://github.com/d0ugal/graith/commit/ceacd83fc4e49e4bfefd507b5b4130573baaa372))
* persist LastOutputAt on session exit ([91b8911](https://github.com/d0ugal/graith/commit/91b89113e8917594b9576ef2ed1365119f116e78))
* replace Branch column with Summary, rename Last to Output ([5b1e62d](https://github.com/d0ugal/graith/commit/5b1e62dddc3dd75b678ede63c07b2a09993aed10))
* two-tier summary resolution in toSessionInfo with expiry logic ([432e03f](https://github.com/d0ugal/graith/commit/432e03f042081b0f0c05c723c54c78c0ee9e06ea))


### Bug Fixes

* address tribunal review findings for agent prompt injection ([dff6951](https://github.com/d0ugal/graith/commit/dff695185f722865e6d3c1d428901dd689e615ae))
* rewrite if-else chain as switch for gocritic lint ([7eb35ee](https://github.com/d0ugal/graith/commit/7eb35ee962e3759841a6fb304528e187c9426882))

## [0.31.0](https://github.com/d0ugal/graith/compare/v0.30.1...v0.31.0) (2026-05-20)


### Features

* add starred sessions concept ([0d9093d](https://github.com/d0ugal/graith/commit/0d9093d9eedcaca39b69af364b1f183e708deca8))


### Bug Fixes

* address tribunal review findings for starred sessions ([8bf97d3](https://github.com/d0ugal/graith/commit/8bf97d3f9a43c8eb9eabdf85e27caaa6d4b336df))
* update github.com/charmbracelet/ultraviolet digest to 2399af7 ([9668e1c](https://github.com/d0ugal/graith/commit/9668e1c2fffb1cbc75f38c78c4ae7d2fb0135230))
* update module modernc.org/libc to v1.73.4 ([d6d261f](https://github.com/d0ugal/graith/commit/d6d261fc9e31df8a7a7ce3429555e7b4e929336a))

## [0.30.1](https://github.com/d0ugal/graith/compare/v0.30.0...v0.30.1) (2026-05-17)


### Bug Fixes

* address tribunal review findings for terminal reset ([1faf0ca](https://github.com/d0ugal/graith/commit/1faf0ca7463fd782ac19341aacc2d813804b2e1e))
* reset terminal state on detach to clean up screen and mouse modes ([d80e7b4](https://github.com/d0ugal/graith/commit/d80e7b453cb34582205d67526ae32bd31718d6f6))

## [0.30.0](https://github.com/d0ugal/graith/compare/v0.29.2...v0.30.0) (2026-05-17)


### Features

* add --children and --parent flags to gr msg send ([337207c](https://github.com/d0ugal/graith/commit/337207c6e11d8ff35171029f4a4a085663c8cdef))
* add --children flag to gr stop ([ad1fad1](https://github.com/d0ugal/graith/commit/ad1fad126a0d9fa0919eec2c497382e098e577cc))
* add agent detection package ([590cf7b](https://github.com/d0ugal/graith/commit/590cf7b7958c99acd07a88d73ee19d39e1307587))
* add Children/ExcludeRoot fields to StopMsg and DeleteMsg ([3e3fec4](https://github.com/d0ugal/graith/commit/3e3fec4e3e0755d08a8ddfb0852f03ed60fe936b))
* add StatusChangedAt field to session state ([af2a0e0](https://github.com/d0ugal/graith/commit/af2a0e04efe7b7e265043db521140ad4c314016b))
* add StopWithChildren to SessionManager ([73b7133](https://github.com/d0ugal/graith/commit/73b7133a41781a12dc8b5636aeb6d67daf2170f2))
* add view cycling to session overlay (left/right arrows) ([08b0321](https://github.com/d0ugal/graith/commit/08b03213b216cf071a7a029b878608569b2e3e4d))
* add view mode types with filter and sort functions ([daa08e9](https://github.com/d0ugal/graith/commit/daa08e9a8d657442ad2ac4dd0be804f6b55dbf14))
* auto-enable JSON output in agent environments ([a7b734f](https://github.com/d0ugal/graith/commit/a7b734fcd28062961354b3265004bb9692a27802))
* auto-resolve GRAITH_SESSION_ID for delete --children ([e9d920b](https://github.com/d0ugal/graith/commit/e9d920bbcb961e43f1204a71d90f02f68d41871f))
* expose StatusChangedAt in session info protocol ([a664fe4](https://github.com/d0ugal/graith/commit/a664fe43b3906b176fbd57d9c006d82df2adc350))
* filter and delete respect current view mode ([9f86366](https://github.com/d0ugal/graith/commit/9f863663129c6aedbb623d94836bfece2d3c6a21))
* handle stop-with-children and ExcludeRoot in handler ([9d61694](https://github.com/d0ugal/graith/commit/9d616949ab8f9dd595f6386724397b39fd14216e))
* track StatusChangedAt on agent status transitions ([ca96fac](https://github.com/d0ugal/graith/commit/ca96fac835a5f7d1ad09155c92c784c540e83ee4))


### Bug Fixes

* address review findings for delete idempotency ([f58b89a](https://github.com/d0ugal/graith/commit/f58b89a65c71e23660785d4308225daa6b3cf0c9))
* address tribunal review findings ([60b9f55](https://github.com/d0ugal/graith/commit/60b9f55aa776cb13ee26b0eed7b664fb2df39b7b))
* clone session state before unlock in Create and Fork ([807f8e1](https://github.com/d0ugal/graith/commit/807f8e104046fe108bc99f50ff72f9230c0a7266))
* handle StatusCreating in DeleteWithChildren ([ffddda0](https://github.com/d0ugal/graith/commit/ffddda0257f78fb46598296c17ba5877d78dc6fa))
* make GR_AGENT_MODE=0 override agent detection at call time ([69e5bd8](https://github.com/d0ugal/graith/commit/69e5bd8f448963393ffa588163176949fcc1bbf3))
* make session delete idempotent and race-safe ([b4594ab](https://github.com/d0ugal/graith/commit/b4594ab4326a3b8e23d899913e1c521ced78ae60))
* release daemon mutex during blocking Create/Fork/Resume operations ([985f786](https://github.com/d0ugal/graith/commit/985f786b94543482d7f3e800f9f7b9ff713535ae)), closes [#218](https://github.com/d0ugal/graith/issues/218)
* remove unused viewMode.name() method ([e026a1b](https://github.com/d0ugal/graith/commit/e026a1be2fc25a013656c0b95c79841b8904a8e7))
* set StatusChangedAt on Phase 1 session creation ([c2ca024](https://github.com/d0ugal/graith/commit/c2ca0249419267190fbbc56667db60b5c64ce83f))
* suppress SA9003 empty-branch lint in msg send notifications ([3a65f85](https://github.com/d0ugal/graith/commit/3a65f85911d7108c2dfed56aa71fb9091d66048d))

## [0.29.2](https://github.com/d0ugal/graith/compare/v0.29.1...v0.29.2) (2026-05-16)


### Bug Fixes

* address review findings for notification injection fix ([e56e213](https://github.com/d0ugal/graith/commit/e56e213a65dd86f4b15badfeab18b0015c244750))
* block resume and notifications for unsafe persisted session names ([655becf](https://github.com/d0ugal/graith/commit/655becfb1ac7eb933008dcff734dcfa7d8670200))
* buffer statusbar setup/teardown into single writes ([dd4ed16](https://github.com/d0ugal/graith/commit/dd4ed167f11c5b5f70eea82edd60adeaa6e3857d))
* don't set parent when creating session via ctrl+b c ([eb5b913](https://github.com/d0ugal/graith/commit/eb5b9136adceb48e7ba781447d30cafb138a1bdf))
* prevent shell command injection in notification commands ([f441122](https://github.com/d0ugal/graith/commit/f44112277eb4da4cc1ef91a394eb095648c9c644))
* propagate git teardown errors on session delete ([a92ef18](https://github.com/d0ugal/graith/commit/a92ef188960a9342cbe0fa6ec760c74bfc30ad9b)), closes [#258](https://github.com/d0ugal/graith/issues/258)
* send scrollback history before starting live forwarding on attach ([384b5f4](https://github.com/d0ugal/graith/commit/384b5f4fba77fe2f8cb62b069dd54befb141b7f8)), closes [#266](https://github.com/d0ugal/graith/issues/266)
* serialize concurrent stdout writes in passthrough mode ([1a7bf57](https://github.com/d0ugal/graith/commit/1a7bf577fbf93b6852a0520b04fb991f24671100)), closes [#216](https://github.com/d0ugal/graith/issues/216)
* use fmt.Fprintf in teardown to satisfy staticcheck QF1012 ([d747aed](https://github.com/d0ugal/graith/commit/d747aede2961ada1ddd12a7cc80360670e669098))
* validate session names to prevent injection across subsystems ([bc64c1c](https://github.com/d0ugal/graith/commit/bc64c1c8c563ce9cd516bddfe71ae2c78a41afef)), closes [#222](https://github.com/d0ugal/graith/issues/222)

## [0.29.1](https://github.com/d0ugal/graith/compare/v0.29.0...v0.29.1) (2026-05-16)


### Bug Fixes

* address review tribunal findings in cursor cleanup ([10f8899](https://github.com/d0ugal/graith/commit/10f88997e20395a67a6626ceb9867e7477ff5e9b))
* clean up stale message cursors during cleanup ([34561c3](https://github.com/d0ugal/graith/commit/34561c333f102231fc0f61c8ba334549ef1a3686)), closes [#254](https://github.com/d0ugal/graith/issues/254)
* clear stale preview when filter yields no matching sessions ([57cf4b5](https://github.com/d0ugal/graith/commit/57cf4b5f290a81e1e222bab228396eecf8f8b821))
* fall back to default agent in MCP createSession ([d52859b](https://github.com/d0ugal/graith/commit/d52859b09416a28741dad09a30e08411a06f332c)), closes [#232](https://github.com/d0ugal/graith/issues/232)
* make git tests hermetic against user signing config ([d58fb8d](https://github.com/d0ugal/graith/commit/d58fb8d8cddfdb1afff687bb1cbbf21c99d43bbc)), closes [#228](https://github.com/d0ugal/graith/issues/228)
* migrate approvals_enabled to agent_hooks in state ([f41a057](https://github.com/d0ugal/graith/commit/f41a057f9c00dcb261855d1f28410f6904b5f51d)), closes [#208](https://github.com/d0ugal/graith/issues/208)
* re-read cleanup config each iteration to respect hot-reload ([9ea5a92](https://github.com/d0ugal/graith/commit/9ea5a92ec1548915d16d5c1b4464fe3db29d23b6))
* refresh preview when overlay filter changes selection ([bd7b226](https://github.com/d0ugal/graith/commit/bd7b2264ad699099988096cabaedb863d513ae2d)), closes [#243](https://github.com/d0ugal/graith/issues/243)
* suppress bogus stopped event when deleting running session ([505ad05](https://github.com/d0ugal/graith/commit/505ad05b4595e1799161b595bb9ba19742a7713c)), closes [#225](https://github.com/d0ugal/graith/issues/225)
* suppress non-zero exit status from shell subprocesses ([027f5a8](https://github.com/d0ugal/graith/commit/027f5a875b4799b78705dfef72de64bee2642285))
* surface shell launch errors instead of silently reattaching ([e9ee02b](https://github.com/d0ugal/graith/commit/e9ee02b3b93f69e52dd4222fbe145d4d1ebd5aed)), closes [#240](https://github.com/d0ugal/graith/issues/240)
* use applyConfig in cleanup test to match real hot-reload path ([69f3cac](https://github.com/d0ugal/graith/commit/69f3cacf374e0b0c876ac08129459c614eb7b56a))

## [0.29.0](https://github.com/d0ugal/graith/compare/v0.28.1...v0.29.0) (2026-05-15)


### Features

* detect orphaned worktrees and fix false-positive PID check in gr doctor ([4d45c1c](https://github.com/d0ugal/graith/commit/4d45c1c46c22fd1be9871049a4b9fc4fe8449b7d))
* show parent-child tree hierarchy in session picker overlay ([8e1dcbf](https://github.com/d0ugal/graith/commit/8e1dcbf6179b30232ba18f8c50375cb5f37c826d))


### Bug Fixes

* address review tribunal findings in orphaned worktree detection ([af268c1](https://github.com/d0ugal/graith/commit/af268c135c67dee55d8bc67b2023665083352bbf))
* render cycle members as roots in overlay tree ([39d8880](https://github.com/d0ugal/graith/commit/39d88801fb993c98f30f78a4da19de19c210db36))
* split gr type PTY writes so TUI frameworks treat Enter as submit ([b9345c8](https://github.com/d0ugal/graith/commit/b9345c814222b325f6dd4718e810dee43cc35018))

## [0.28.1](https://github.com/d0ugal/graith/compare/v0.28.0...v0.28.1) (2026-05-15)


### Bug Fixes

* update module modernc.org/libc to v1.73.2 ([dd1fc14](https://github.com/d0ugal/graith/commit/dd1fc1425d99e271129e39133b415a3cad092763))
* update module modernc.org/libc to v1.73.3 ([ccf778b](https://github.com/d0ugal/graith/commit/ccf778b8a0eb2f0031c482ce04618355134f46e8))

## [0.28.0](https://github.com/d0ugal/graith/compare/v0.27.3...v0.28.0) (2026-05-13)


### Features

* enrich gr doctor with daemon diagnostics and structured output ([85edfee](https://github.com/d0ugal/graith/commit/85edfeea6e80f83238cf2e3a2a6187b1ef5c050c))


### Bug Fixes

* address review tribunal findings in gr doctor ([d658037](https://github.com/d0ugal/graith/commit/d658037e102055916f91a042c5e878832c2f44e3))
* address tribunal review findings in docs ([1dfe728](https://github.com/d0ugal/graith/commit/1dfe728c0f91eea71dbd8990cf9ff078b6ff2e6a))
* correct stale template vars, flags, and paths in docs ([505c101](https://github.com/d0ugal/graith/commit/505c101f1d808cf69fc5364f88732263c970c5c3))

## [0.27.3](https://github.com/d0ugal/graith/compare/v0.27.2...v0.27.3) (2026-05-13)


### Bug Fixes

* set DataDir and LogDir in test helper to prevent log file leaks ([6b501e6](https://github.com/d0ugal/graith/commit/6b501e62e8698476dc422692f2fc5a7544028b9f))

## [0.27.2](https://github.com/d0ugal/graith/compare/v0.27.1...v0.27.2) (2026-05-13)


### Bug Fixes

* parse model IDs from validate_model output with descriptions ([57b62f2](https://github.com/d0ugal/graith/commit/57b62f263bd63aa849888fd54c3709814ee71d96))

## [0.27.1](https://github.com/d0ugal/graith/compare/v0.27.0...v0.27.1) (2026-05-13)


### Bug Fixes

* only add hooks dir to sandbox read_dirs when it exists ([b08a750](https://github.com/d0ugal/graith/commit/b08a7502b66eef01cd5c37bff90f3303a55765ba))

## [0.27.0](https://github.com/d0ugal/graith/compare/v0.26.0...v0.27.0) (2026-05-12)


### Features

* show stale config indicator in session selector overlay ([e2c1169](https://github.com/d0ugal/graith/commit/e2c1169eafe5591506c45e4cbcb78165974a592e))
* validate --model flag against agent's supported models ([170c79b](https://github.com/d0ugal/graith/commit/170c79b45eaf02638bb648c182decffdc5f1d94a))


### Bug Fixes

* add json tags to Agent struct, fix stale indicator alignment ([f37b79d](https://github.com/d0ugal/graith/commit/f37b79d8e95a77ead70c3ecfb9edcbff213c810f))
* align struct tags to satisfy tagalign linter ([4994787](https://github.com/d0ugal/graith/commit/499478750d8bf56f4d255ce65c6888599ac4aacc))
* move model validation before mutex, add exec timeout ([386e0b6](https://github.com/d0ugal/graith/commit/386e0b6be60f68ebed5882134ea4ffa0c25ce16e))

## [0.26.0](https://github.com/d0ugal/graith/compare/v0.25.0...v0.26.0) (2026-05-11)


### Features

* allow restarting running sessions from session selector ([e717ffa](https://github.com/d0ugal/graith/commit/e717ffa2f370cf38b42bd90247375d3ac08b6c2a))
* always enable agent hooks, remove --agent-hooks flag ([3f0907d](https://github.com/d0ugal/graith/commit/3f0907d8c3d4cd02a62e3a3bec6d654156484c8f))


### Bug Fixes

* add saveState in Restart stop path, fix flaky MCP stderr test ([d23348a](https://github.com/d0ugal/graith/commit/d23348a7fd29d70fbe7ac5a97e04b49af679860f))
* don't block agents without hook support, add cursor hooks ([75e9fb0](https://github.com/d0ugal/graith/commit/75e9fb005b214ffcd0b7a9436e0dd3474d329255)), closes [#389](https://github.com/d0ugal/graith/issues/389)

## [0.25.0](https://github.com/d0ugal/graith/compare/v0.24.3...v0.25.0) (2026-05-09)


### Features

* enable agent hooks by default on gr new ([7951698](https://github.com/d0ugal/graith/commit/7951698af08443ae5702b740b77345d9c4444273))

## [0.24.3](https://github.com/d0ugal/graith/compare/v0.24.2...v0.24.3) (2026-05-09)


### Bug Fixes

* allow user config to disable auto-injected MCP servers ([4a211a3](https://github.com/d0ugal/graith/commit/4a211a3a699512b0dbe7bd0a79c65e65f13a1af0))
* register auto-injected graith MCP server with MCPManager ([3b8a29c](https://github.com/d0ugal/graith/commit/3b8a29cd0cf539c666164c671027582aa9209b8a))

## [0.24.2](https://github.com/d0ugal/graith/compare/v0.24.1...v0.24.2) (2026-05-09)


### Bug Fixes

* replace post_install with caveats in Homebrew formula ([3cbbf6c](https://github.com/d0ugal/graith/commit/3cbbf6c006dd15044c4adf720f4095a851288d8a))
* update module charm.land/lipgloss/v2 to v2.0.4 ([bfd52c4](https://github.com/d0ugal/graith/commit/bfd52c4d68f1b5ef8b656043f3f3f7bf5d6d2831))

## [0.24.1](https://github.com/d0ugal/graith/compare/v0.24.0...v0.24.1) (2026-05-08)


### Bug Fixes

* clean up unused param, add mcp_config to log, add integration test ([35b9a1e](https://github.com/d0ugal/graith/commit/35b9a1e850798168093ffc3d8fdcdb15dbcfcaf0))
* use --mcp-config instead of --settings for Claude Code MCP servers ([d3e00de](https://github.com/d0ugal/graith/commit/d3e00debba571dbeed98e82b38b6136e458f8be6))

## [0.24.0](https://github.com/d0ugal/graith/compare/v0.23.1...v0.24.0) (2026-05-08)


### Features

* add --model flag to gr new ([e1c8487](https://github.com/d0ugal/graith/commit/e1c848744101f529d7b63c13f649d4cde6741560)), closes [#367](https://github.com/d0ugal/graith/issues/367)


### Bug Fixes

* map hook decision values to agent-specific schemas ([79269df](https://github.com/d0ugal/graith/commit/79269dfb852ae5395f9287dfc12a040e87d611bb))
* persist model in session state for resume/fork, add to MCP ([f28df0e](https://github.com/d0ugal/graith/commit/f28df0e0713701a3125067ebcf3123ecfea0937a))
* show requested model in session info when hook model is empty ([d7e5bed](https://github.com/d0ugal/graith/commit/d7e5bedbd852a83f63b5f77ebefd93a91cd3097c))

## [0.23.1](https://github.com/d0ugal/graith/compare/v0.23.0...v0.23.1) (2026-05-07)


### Bug Fixes

* strip description= prefix from jsonschema struct tags ([f0633a5](https://github.com/d0ugal/graith/commit/f0633a50bcbbe632d64586758b071d1a4a2822cb))
* update module modernc.org/libc to v1.73.1 ([03a11dc](https://github.com/d0ugal/graith/commit/03a11dc3352c15644816268f0da340611f06a972))

## [0.23.0](https://github.com/d0ugal/graith/compare/v0.22.0...v0.23.0) (2026-05-07)


### Features

* implement daemon-managed MCP proxy (Proposal 2) ([e1adc92](https://github.com/d0ugal/graith/commit/e1adc9200bce151ea8bb998b4944a39575cff845))


### Bug Fixes

* address lint issues in MCPServerConfig ([7fdb520](https://github.com/d0ugal/graith/commit/7fdb5207320d68f582baf3087041d92e257d28e2))
* address review tribunal findings for MCP injection ([f01d1f3](https://github.com/d0ugal/graith/commit/f01d1f3ccd134e3d05c5f2118c7383b40dc2f3c3))
* eliminate stdout write race between PTY data and status bar ticker ([8cde424](https://github.com/d0ugal/graith/commit/8cde4249135de6fabdd90c3d66938e4301585f9c))

## [0.22.0](https://github.com/d0ugal/graith/compare/v0.21.0...v0.22.0) (2026-05-07)


### Features

* add MCP server injection for agent sessions ([5d5d321](https://github.com/d0ugal/graith/commit/5d5d321122646cee05b1f9d57f8904bad516a8d2))
* refresh sandbox config from current settings on resume and fork ([ff614e0](https://github.com/d0ugal/graith/commit/ff614e0cd946fabfbdf04a56ce486f0dba17df80)), closes [#361](https://github.com/d0ugal/graith/issues/361)
* start Chrome with remote debugging for sandboxed agents ([33a8d34](https://github.com/d0ugal/graith/commit/33a8d34ffa8b18c9988996baa8a5a49e6763c8a8)), closes [#359](https://github.com/d0ugal/graith/issues/359)


### Bug Fixes

* address lint issues in MCPServerConfig ([d318c10](https://github.com/d0ugal/graith/commit/d318c10176ff7e8166cf2f54bc0c15d19af83dab))
* address review tribunal findings for Chrome remote debugging ([570828f](https://github.com/d0ugal/graith/commit/570828f59077b348efb47246509d0792a49bc3a8))
* address review tribunal findings for MCP injection ([eb31c07](https://github.com/d0ugal/graith/commit/eb31c07efd323eb0b4a0a99c8d13b135a6bdd79d))
* clean up leaked process in sandbox resume test ([cd50907](https://github.com/d0ugal/graith/commit/cd50907b984a1fe6574df3e74e12f09a136c1bf9))
* wait for PTY exit in sandbox resume tests to prevent TempDir cleanup race ([19a1ab6](https://github.com/d0ugal/graith/commit/19a1ab666c5901c5b871eab75937407353923010))


### Reverts

* remove Chrome-specific code in favor of MCP injection ([f3bb77f](https://github.com/d0ugal/graith/commit/f3bb77f56e5728d50a7c57fbe537f4e24c9dd163))

## [0.21.0](https://github.com/d0ugal/graith/compare/v0.20.1...v0.21.0) (2026-05-04)


### Features

* add data_dir config option to override worktree base path ([e2f123a](https://github.com/d0ugal/graith/commit/e2f123aef7bdb2ada4cfd0bf7bfdbc05e8bcfbe6)), closes [#355](https://github.com/d0ugal/graith/issues/355)


### Bug Fixes

* address review feedback for data_dir config ([555e417](https://github.com/d0ugal/graith/commit/555e417dcda4d4e24828676d02e43b09cac7c83e))

## [0.20.1](https://github.com/d0ugal/graith/compare/v0.20.0...v0.20.1) (2026-05-04)


### Bug Fixes

* populate snapshots in DeleteWithChildren so cleanup actually runs ([5021633](https://github.com/d0ugal/graith/commit/5021633a2c1bb28448a73fdb99bef9508291c615))

## [0.20.0](https://github.com/d0ugal/graith/compare/v0.19.0...v0.20.0) (2026-05-04)


### Features

* add parent/child relationships to sessions ([26ac5ea](https://github.com/d0ugal/graith/commit/26ac5eab0efbc1763310b5771647b9181a45f6da))

## [0.19.0](https://github.com/d0ugal/graith/compare/v0.18.1...v0.19.0) (2026-05-04)


### Features

* add includes and singleton config, IncludedRepoState, git worktree helpers ([3ef3b35](https://github.com/d0ugal/graith/commit/3ef3b35d2b75e20e9c782bbff76cde190562398e))
* externalize config defaults and add gr config commands ([23d0584](https://github.com/d0ugal/graith/commit/23d0584aa36dbf50d6ca76936f4da786cf538dee))
* implement multi-repo includes sessions ([1103eed](https://github.com/d0ugal/graith/commit/1103eeda6d4d9e21dfd98f30854bcb56cf727bd6))


### Bug Fixes

* ack inbox messages in check-inbox hook to prevent duplicates [#277](https://github.com/d0ugal/graith/issues/277) ([e5dc0a6](https://github.com/d0ugal/graith/commit/e5dc0a6acc2bd8ee8ede1857ce703a6c3e195a6f))
* add default sandbox paths for agy/gemini agent [#207](https://github.com/d0ugal/graith/issues/207) ([b361ec7](https://github.com/d0ugal/graith/commit/b361ec7f1533584f3322a82bd27fe75ab7abf850))
* add regression test for check-inbox ack at the CLI layer ([57c8f03](https://github.com/d0ugal/graith/commit/57c8f034679acd2a317a2ee7c962fc3b6543bc4f))
* address code review feedback for config commands ([da73ce4](https://github.com/d0ugal/graith/commit/da73ce40fbcd596e1fad6181216db353772c2d27))
* address review tribunal findings for multi-repo includes ([40570ce](https://github.com/d0ugal/graith/commit/40570ce21538c28bca589ae467a3efcf703d74ac))
* also clean cwd and add trailing-slash test cases ([4e5e502](https://github.com/d0ugal/graith/commit/4e5e502c9bed128549583b3e9049c843f09a1471))
* also shell-quote gr binary path in Claude hook commands ([e453432](https://github.com/d0ugal/graith/commit/e4534325ade2f99aaa442ea48ef5cb0178134c9a))
* clean up git worktree in TestForkUsesSourceBaseBranch ([67555cc](https://github.com/d0ugal/graith/commit/67555cc45be27078c09dcc305c50020522a0742c))
* clear connection deadline after handshake in ConnectFast/ConnectForApproval ([bee5e57](https://github.com/d0ugal/graith/commit/bee5e57aaaaafc2b68167228fafc98d9124ba5c1)), closes [#224](https://github.com/d0ugal/graith/issues/224)
* consume unterminated OSC sequences in StripANSI [#278](https://github.com/d0ugal/graith/issues/278) ([463703e](https://github.com/d0ugal/graith/commit/463703e9b8328e3c8f44b1344bfb5ae1e7765c9a))
* convert if-else chain to switch to satisfy gocritic linter ([fecfc13](https://github.com/d0ugal/graith/commit/fecfc13da1488e67b479207958832272d97a2148))
* correct claude fork args to use --resume with --fork-session ([1775067](https://github.com/d0ugal/graith/commit/17750672732a3c23d608688423b7fe72d774da35))
* escape single quotes in gr binary path for codex hook scripts [#252](https://github.com/d0ugal/graith/issues/252) ([8d5242f](https://github.com/d0ugal/graith/commit/8d5242f93a6d96c3770e0d9c4c97e960f27dc3a5))
* exercise formatToolDetail paths in narrow terminal test ([dea3e71](https://github.com/d0ugal/graith/commit/dea3e71c7a2e5e65da0da6032f37970c751b6b88))
* include never-attached sessions in --stale filter [#262](https://github.com/d0ugal/graith/issues/262) ([fc79d55](https://github.com/d0ugal/graith/commit/fc79d5529e7049490e6be1b13a5e23ef8c5a1b69))
* log saveState error on attach instead of discarding it ([1f96553](https://github.com/d0ugal/graith/commit/1f965531cf5c5a4fb715e3f70be15b07a65df4d7))
* make initTempGitRepo deterministic with git init -b main ([c4611a9](https://github.com/d0ugal/graith/commit/c4611a930841652e16cb34d5740c856843be2d87))
* output errors as JSON when --json flag is set [#269](https://github.com/d0ugal/graith/issues/269) ([f6c1694](https://github.com/d0ugal/graith/commit/f6c16945d3070021730e7a4d692e5aca13ee32be))
* persist LastAttachedAt to disk on attach ([#279](https://github.com/d0ugal/graith/issues/279)) ([8872015](https://github.com/d0ugal/graith/commit/8872015503d9e2fe027433992049006cce1fc0ca))
* prevent panic in approval overlay on narrow terminals [#271](https://github.com/d0ugal/graith/issues/271) ([db6d08c](https://github.com/d0ugal/graith/commit/db6d08ca2365c38b44d8682ddfde57ab4772068e))
* reject negative durations in ParseDurationWithDays [#230](https://github.com/d0ugal/graith/issues/230) ([881dd3e](https://github.com/d0ugal/graith/commit/881dd3e77d0a532ae2677e47030e17df03be7d3e))
* reject null/empty payloads in DecodePayload ([#268](https://github.com/d0ugal/graith/issues/268)) ([aa213a4](https://github.com/d0ugal/graith/commit/aa213a4390cf03e96006e4a52a241be375e8a867))
* resolve sender name from session state in msg_pub handler ([#270](https://github.com/d0ugal/graith/issues/270)) ([9c7c0e3](https://github.com/d0ugal/graith/commit/9c7c0e3aa131cd44bda06cb1b020a64887233425))
* return error when both --prompt and --prompt-file are specified ([b29c778](https://github.com/d0ugal/graith/commit/b29c77886bababba6d29806ba14e9e7ac787b924)), closes [#234](https://github.com/d0ugal/graith/issues/234)
* support local-only repos in session creation [#267](https://github.com/d0ugal/graith/issues/267) ([bc3c744](https://github.com/d0ugal/graith/commit/bc3c7443d9af3ba5e20ef2f5b23946bd7c64a6fa))
* update module github.com/sahilm/fuzzy to v0.1.3 ([94c9ef1](https://github.com/d0ugal/graith/commit/94c9ef12db0e0c2959d2af6141d480d76646993d))
* use CreateTemp for atomic config reset to guarantee 0600 permissions ([482636d](https://github.com/d0ugal/graith/commit/482636d942339bead17c341fe183aab12da313c5))
* use nearest OSC terminator instead of preferring BEL ([cd332a8](https://github.com/d0ugal/graith/commit/cd332a84ae1d910ce105020dc147a4784120864c))
* use path-boundary matching in info command ([1118a50](https://github.com/d0ugal/graith/commit/1118a504435e3d528becee02a5e1c855bf3c6446)), closes [#231](https://github.com/d0ugal/graith/issues/231)
* use source.BaseBranch in fork instead of source.Branch [#255](https://github.com/d0ugal/graith/issues/255) ([0f67f1c](https://github.com/d0ugal/graith/commit/0f67f1cd3d4823e89b31067b33fa484fbc44ad45))

## [0.18.1](https://github.com/d0ugal/graith/compare/v0.18.0...v0.18.1) (2026-05-01)


### Bug Fixes

* always watch config file for changes and log sandbox config diffs ([a4fdd7a](https://github.com/d0ugal/graith/commit/a4fdd7afcdfbfb26e0cb6695442f3ae10429a72a))
* log full sandbox opts (read_dirs, write_dirs, features, workdir) on session create/fork/resume ([725f12c](https://github.com/d0ugal/graith/commit/725f12c68522e15fa7d44c9e808c198ff3e1da79))

## [0.18.0](https://github.com/d0ugal/graith/compare/v0.17.0...v0.18.0) (2026-05-01)


### Features

* add GRAITH_PROFILE support to config layer ([f06095a](https://github.com/d0ugal/graith/commit/f06095ac615edee31843bfbf9b045268c27bb7db))
* add in-place sessions for repos without remotes ([d4ac9f2](https://github.com/d0ugal/graith/commit/d4ac9f280ad3679d04bc4eb532073025c5d86bd5))
* add profile to handshake protocol with shared builder and mismatch rejection ([aa5f60a](https://github.com/d0ugal/graith/commit/aa5f60a727bc3064f736b02523c6ac34a6e1c812))
* propagate GRAITH_PROFILE to agent env and guard legacy cleanup ([cce201f](https://github.com/d0ugal/graith/commit/cce201f42ab6276e9deec5d6da7525165ccb09e1))
* show profile indicator in overlay, list, and doctor for non-default profiles ([ca309e7](https://github.com/d0ugal/graith/commit/ca309e7cc59697a0ae17cf0fa2c73882974bbac3))


### Bug Fixes

* address Codex review findings for GRAITH_PROFILE ([558d4bf](https://github.com/d0ugal/graith/commit/558d4bfd79a17904787cc004fc7089cb5e8b757a))
* address Codex review findings for in-place sessions ([ddafeba](https://github.com/d0ugal/graith/commit/ddafebab6c9cd12a25a3cf59a6b69c690277269c))
* align struct tags in MCP CreateSessionInput to satisfy tagalign linter ([bf22bc3](https://github.com/d0ugal/graith/commit/bf22bc3f6a6ab9e658535531a1dfaf73e2ae92b7))
* resolve profile independently of --config path in LoadOrDefault ([a56195f](https://github.com/d0ugal/graith/commit/a56195f0fbfdd136ff25c5bde26e6b94e055d89b))

## [0.17.0](https://github.com/d0ugal/graith/compare/v0.16.7...v0.17.0) (2026-04-30)


### Features

* expand globs in sandbox read_dirs and write_dirs ([89a14c6](https://github.com/d0ugal/graith/commit/89a14c6ba86a5b79a153b5873aca9f310163cd07))


### Bug Fixes

* allow duplicate session names ([9e3f3dc](https://github.com/d0ugal/graith/commit/9e3f3dc8fb9e5063194f166079f4080c0f7efaf1))
* remove trailing newline in TestRename to pass whitespace linter ([4a48c14](https://github.com/d0ugal/graith/commit/4a48c1439e1d83bb34c992c039fe21c3eee83106))

## [0.16.7](https://github.com/d0ugal/graith/compare/v0.16.6...v0.16.7) (2026-04-30)


### Bug Fixes

* use os.MkdirTemp with retry cleanup in TestResumeResetsIdleSince ([f0e427a](https://github.com/d0ugal/graith/commit/f0e427a270cbf0c41729fdfd72f91badad3782f4))

## [0.16.6](https://github.com/d0ugal/graith/compare/v0.16.5...v0.16.6) (2026-04-30)


### Bug Fixes

* clean up PTY session in TestResumeResetsIdleSince to avoid TempDir race ([6f9685c](https://github.com/d0ugal/graith/commit/6f9685c2ea617c76e985b1a85601179ff08fed38))
* clear IdleSince on Resume, make watchSession tests deterministic ([a1252d2](https://github.com/d0ugal/graith/commit/a1252d25c1f000c49aee28011fabb29a0ce0e06d))
* prevent stale watchSession from corrupting resumed session state ([736b84e](https://github.com/d0ugal/graith/commit/736b84ecc16e54a4236f7d8e579cf0c8ad615a3f))
* restore exec upgrade for auto-restart to preserve sessions ([8abc3a1](https://github.com/d0ugal/graith/commit/8abc3a10b9b72c54d5c27a215f217706cdb5fbc2))
* satisfy SA2001 by reading state inside the lock barrier ([647a5cd](https://github.com/d0ugal/graith/commit/647a5cd65961b98eea7161e69fa0b0463b5517b2))
* synchronous PTY cleanup in TestResumeResetsIdleSince ([4d77d2b](https://github.com/d0ugal/graith/commit/4d77d2b0a72b97fd11c7b62e13897804f1eb5942))
* use single tmpDir with LogDir set in TestResumeResetsIdleSince ([94f3510](https://github.com/d0ugal/graith/commit/94f3510d57f8b07d5da8004b6aac0a316d05ed42))
* verify daemon version after exec upgrade to catch stale restarts ([16b0290](https://github.com/d0ugal/graith/commit/16b0290e363ea61fafced7b6ca79f7eb7bf62911))

## [0.16.5](https://github.com/d0ugal/graith/compare/v0.16.4...v0.16.5) (2026-04-30)


### Bug Fixes

* address code review feedback on overlay delete safety ([462c80d](https://github.com/d0ugal/graith/commit/462c80dad00dbce27cf78eeb1444c472a46c32c7))
* address review feedback — snapshot os.Environ, harden dupe test ([7db10dd](https://github.com/d0ugal/graith/commit/7db10dda2b690c6b4598d51a1475826675894576))
* align struct tags and remove unused sandboxOpts method ([794d84b](https://github.com/d0ugal/graith/commit/794d84b65da7e8d109de8d006df07628cd53e18f))
* backfill stream_hwm on upgrade, use monotonic upsert ([c18d585](https://github.com/d0ugal/graith/commit/c18d585ba6bfa9cfee8244c200db5c9c5ad82852))
* bind dashboard delete/stop confirmation to session ID, not cursor index ([986143c](https://github.com/d0ugal/graith/commit/986143c1b22d246c81a4372e6af63e1aba53abcb)), closes [#237](https://github.com/d0ugal/graith/issues/237)
* cancel stop confirmation when target session stops during refresh ([6c454dd](https://github.com/d0ugal/graith/commit/6c454dd9f61d33380e4c7d8e590cce30f814fd50))
* clamp approval deadline and improve API per code review ([b228252](https://github.com/d0ugal/graith/commit/b228252bf78b9c33a20008f7fdfd91173c38c5a0))
* clean up log file on Create/Fork rollback ([9ae7052](https://github.com/d0ugal/graith/commit/9ae70527c593fe29f516df813657ce17cb139f77))
* close connection when kicking replaced attach client ([708ce58](https://github.com/d0ugal/graith/commit/708ce586704d24cc7445da4ab1cd0bf72d3c01aa)), closes [#264](https://github.com/d0ugal/graith/issues/264)
* eliminate watchSession race and fd leak in saveState rollback ([d12d879](https://github.com/d0ugal/graith/commit/d12d8791d8559bf8a3effb76b3994c3b381134e1))
* ensure buildEnv overrides take effect over parent environment ([d52e8df](https://github.com/d0ugal/graith/commit/d52e8dfcf23701827f165ef798367137c9a4df82)), closes [#265](https://github.com/d0ugal/graith/issues/265)
* error when --agent-hooks used with unsupported agent type ([7f1c6a1](https://github.com/d0ugal/graith/commit/7f1c6a15c506f11b94700f8e668b3f411a4b77e9)), closes [#274](https://github.com/d0ugal/graith/issues/274)
* fsync temp file before rename in writeFileAtomic ([1c5c16c](https://github.com/d0ugal/graith/commit/1c5c16ca80e9cc97dd1fd964febf95cc3931e377))
* gate ChannelData on IsAttachedClient to reject input immediately ([e479c07](https://github.com/d0ugal/graith/commit/e479c07150d7e6632f64799ed7357b62220c3d80))
* grant agy sandbox access to ~/.gemini ([d8cc410](https://github.com/d0ugal/graith/commit/d8cc410bdfc0609a49fd1bcaf30d7ff1502de4ee))
* guard socket cleanup on confirmed daemon stop ([feab6a7](https://github.com/d0ugal/graith/commit/feab6a7951343be50203d38302bf4e655b60a0cf))
* harden Resume path for shared worktree sessions ([303de4c](https://github.com/d0ugal/graith/commit/303de4cce2786274bf0ee901694e98d45e1b68da))
* keep overlay open after deleting a session ([53a60c3](https://github.com/d0ugal/graith/commit/53a60c35586da97c5c2595524a138184b263a81a))
* merge user agent configs with defaults instead of replacing ([0917664](https://github.com/d0ugal/graith/commit/0917664c124fffdd6d04ed3f008980dbfff9570b)), closes [#256](https://github.com/d0ugal/graith/issues/256)
* move Resume shared-worktree guard before hook injection ([31173ac](https://github.com/d0ugal/graith/commit/31173ac2c1b3c88d68cbe01f0d2b02051f63e90f))
* normalize sandbox paths before persisting to prevent cwd-dependent drift ([5451cb6](https://github.com/d0ugal/graith/commit/5451cb6e65164384bdb66f3fa627ee8cf91268a5))
* parse mixed day+time durations like 7d12h in ParseDurationWithDays ([81f43f2](https://github.com/d0ugal/graith/commit/81f43f23d465286d3608ea91ce7367914e5e9127)), closes [#280](https://github.com/d0ugal/graith/issues/280)
* persist AgentHooks on fork, clean up hook files on error ([bc9a9b1](https://github.com/d0ugal/graith/commit/bc9a9b112a412a2be20f0ed1468cd1b9d61964d2))
* persist sandbox config at session creation to prevent resume/fork drift ([93cbcd6](https://github.com/d0ugal/graith/commit/93cbcd6086b39c1d47366146c1cfa3a35293a0ce)), closes [#276](https://github.com/d0ugal/graith/issues/276)
* preserve stream high-water mark across message cleanup ([d26c36f](https://github.com/d0ugal/graith/commit/d26c36fbb274d869277eb6eb0e97e61fa9389fba)), closes [#275](https://github.com/d0ugal/graith/issues/275)
* prune orphaned acked_messages rows during cleanup ([9bab4b7](https://github.com/d0ugal/graith/commit/9bab4b744301b8430937efcf1c890e4dff13732c))
* reject --share-worktree when sandbox is disabled ([fedaaba](https://github.com/d0ugal/graith/commit/fedaaba53e3e95479fd695021a11dbc67ab65a9f)), closes [#245](https://github.com/d0ugal/graith/issues/245)
* reject duplicate session names in Create, Fork, and Rename ([1527348](https://github.com/d0ugal/graith/commit/152734847c2abcca2c1ac9c7d2cd2b255cdeefd9)), closes [#273](https://github.com/d0ugal/graith/issues/273)
* reject fork of no-repo sessions ([8a0b9e9](https://github.com/d0ugal/graith/commit/8a0b9e9d2fe00fbf150e2b20e81774acf953cd52)), closes [#246](https://github.com/d0ugal/graith/issues/246)
* rename variable to avoid shadowing predeclared 'real' ([dc630f5](https://github.com/d0ugal/graith/commit/dc630f5679fd4639bf9597c520dd877f319d48d4))
* resolve symlinks in RepoPathAllowed and validate PIDs before signaling ([9816b18](https://github.com/d0ugal/graith/commit/9816b18b14bbed51e3a1c7b296493091f62a9812)), closes [#248](https://github.com/d0ugal/graith/issues/248)
* return error and roll back state when saveState fails after session creation ([81b2d2c](https://github.com/d0ugal/graith/commit/81b2d2c7349c2128d359698a10d7df40029b11c0)), closes [#247](https://github.com/d0ugal/graith/issues/247)
* send SIGWINCH after type input to wake agent process ([d5625a6](https://github.com/d0ugal/graith/commit/d5625a691f9221f64278d3d0b82fbe5d00a379ee)), closes [#309](https://github.com/d0ugal/graith/issues/309)
* show unsaved work warnings in overlay delete confirmation ([3cb9684](https://github.com/d0ugal/graith/commit/3cb9684270d2d8331f8d2834416bd55e25f4984f))
* sort overlay session picker alphabetically by name ([5d9e8e0](https://github.com/d0ugal/graith/commit/5d9e8e01da8d4418af8c55d1b4beaeaa34392c9d)), closes [#310](https://github.com/d0ugal/graith/issues/310)
* stricter PID parsing, cleanup stale files, align client guard ([3bc4e85](https://github.com/d0ugal/graith/commit/3bc4e85f7e28df6521df39aa8ba7c6070bb13723))
* sync parent directory after rename, wrap close error ([d84f0b4](https://github.com/d0ugal/graith/commit/d84f0b45fbdf2310136dea236d04f560592045e8))
* thread-filtered --ack no longer marks other threads as read ([5b09e23](https://github.com/d0ugal/graith/commit/5b09e23915cd8b7343148db67c77cefe2cc83e33)), closes [#259](https://github.com/d0ugal/graith/issues/259)
* treat EPERM as alive in isPIDAlive to prevent duplicate daemons ([cbdb763](https://github.com/d0ugal/graith/commit/cbdb7632fb12f9299ea235987cc5fd5739de5933)), closes [#250](https://github.com/d0ugal/graith/issues/250)
* update stale comments referencing old time-based sort order ([1577648](https://github.com/d0ugal/graith/commit/1577648ce851007004c9dd65a4a7cee36ba93ca8))
* use configured approval timeout for hook connection deadline ([6fae2b0](https://github.com/d0ugal/graith/commit/6fae2b0564f0961de6233af91ae44a67f8d5c01b)), closes [#244](https://github.com/d0ugal/graith/issues/244)
* use direct SIGWINCH signal instead of same-size Setsize in Poke ([0602d95](https://github.com/d0ugal/graith/commit/0602d95dfffddc75fe9ffa92b42f991db994f2b3))
* use exact basename match in IsGraithDaemon, add symlink edge case tests ([74db1a3](https://github.com/d0ugal/graith/commit/74db1a3d0504b5c493d2a61ee34de1ec445605eb))
* use strict PID parsing in client's stopDaemonByPID ([591c2b0](https://github.com/d0ugal/graith/commit/591c2b017c26c7ea250ec9ab5e82d9627553eb68))
* validate PID before signaling in StopDaemon ([a9946f2](https://github.com/d0ugal/graith/commit/a9946f23ebe7d404cfa25b63d08d6d77a512fe7e)), closes [#236](https://github.com/d0ugal/graith/issues/236)

## [0.16.4](https://github.com/d0ugal/graith/compare/v0.16.3...v0.16.4) (2026-04-29)


### Bug Fixes

* use clean restart for auto-upgrade, prefer PATH in resolveExecutable ([e656e3c](https://github.com/d0ugal/graith/commit/e656e3c45b6a7d3a7c7cb02a2de06900590439b1))

## [0.16.3](https://github.com/d0ugal/graith/compare/v0.16.2...v0.16.3) (2026-04-29)


### Bug Fixes

* auto-restart daemon on version mismatch after upgrades ([520d5e9](https://github.com/d0ugal/graith/commit/520d5e9b7f4fd594cfa1271f3ef3b4a385024e4b))

## [0.16.2](https://github.com/d0ugal/graith/compare/v0.16.1...v0.16.2) (2026-04-29)


### Bug Fixes

* make TestSessionAttachDetach more robust against PTY timing ([08fe01e](https://github.com/d0ugal/graith/commit/08fe01e4f3b8a894d5f106e874b68d759071cd0d))
* make TestSessionAttachDetach more robust against PTY timing ([bb9d4a3](https://github.com/d0ugal/graith/commit/bb9d4a35f3678f5cb67fd948a234e1c4dc4c3c5d))

## [0.16.1](https://github.com/d0ugal/graith/compare/v0.16.0...v0.16.1) (2026-04-27)


### Bug Fixes

* only add hooks dir to sandbox read paths when agent hooks are enabled ([250cf57](https://github.com/d0ugal/graith/commit/250cf572375284ea3cc832fc4d42343e143210f3))

## [0.16.0](https://github.com/d0ugal/graith/compare/v0.15.0...v0.16.0) (2026-04-27)


### Features

* add batch delete/stop with --repo, --stopped, --stale filters ([0cb53b8](https://github.com/d0ugal/graith/commit/0cb53b8f0e5fc62b03dd0c2671eb5391c623edf8))

## [0.15.0](https://github.com/d0ugal/graith/compare/v0.14.0...v0.15.0) (2026-04-27)


### Features

* rename --approvals to --agent-hooks with all-or-nothing semantics ([e9d1422](https://github.com/d0ugal/graith/commit/e9d1422a0c5f37eb5a68c32b54055ea735c83836))

## [0.14.0](https://github.com/d0ugal/graith/compare/v0.13.0...v0.14.0) (2026-04-27)


### Features

* add logging to approval request handling ([c0dc4f0](https://github.com/d0ugal/graith/commit/c0dc4f03c84b203c590b28aadff84217b8b21ef1))
* improve approval overlay formatting ([96d673d](https://github.com/d0ugal/graith/commit/96d673d0554f826c0d70d49fe1ca6bbcbc8a9a66))
* improved approval overlay with detail panel ([5a300f8](https://github.com/d0ugal/graith/commit/5a300f89cb3457f7062e03495268ce096777111c))
* inject unread inbox messages on session start ([09373a2](https://github.com/d0ugal/graith/commit/09373a24404fb6a2aab90b71c79a5fd33638e32b))
* make approval hooks opt-in per session with --approvals flag ([de9a87c](https://github.com/d0ugal/graith/commit/de9a87cdfd080e72bddce01c513e80e1e38ff35e))
* red status bar and approval status for pending approvals ([e5479de](https://github.com/d0ugal/graith/commit/e5479de9fdd7600d78ffc3f0c4a6d4db3e7d7148))


### Bug Fixes

* handle Kitty keyboard protocol release events and encoded follow-up keys ([10668bb](https://github.com/d0ugal/graith/commit/10668bb7248fa32128336ecdb3518837bc337b4a))
* remove TODO comment that triggers godox lint ([ca41d6c](https://github.com/d0ugal/graith/commit/ca41d6ceb406e06dee41e9b0d5081d775074e4d4))
* replace naked returns in parseKittyCSIu to satisfy nakedret lint ([6a18513](https://github.com/d0ugal/graith/commit/6a18513cb7c1fc012b95b484eea5af43ea80005e))

## [0.13.0](https://github.com/d0ugal/graith/compare/v0.12.5...v0.13.0) (2026-04-26)


### Features

* add --share-worktree flag for read-only worktree sharing ([47aeddb](https://github.com/d0ugal/graith/commit/47aeddb125d8223dc6537e61c1153701db1b0bc4)), closes [#183](https://github.com/d0ugal/graith/issues/183)
* add approval overlay UI and passthrough integration ([2ba9e47](https://github.com/d0ugal/graith/commit/2ba9e47f7ac4336b0515382903d718ffc1ee1321))
* add cross-session approval system protocol, config, and daemon ([52c9b7d](https://github.com/d0ugal/graith/commit/52c9b7ded3bba437b0cd4fa9c4259cee7c2b4617))
* add gr approve-request CLI and wire hooks ([2086661](https://github.com/d0ugal/graith/commit/208666149e36ef546ca8e1ea2af1b190605c65e1))


### Bug Fixes

* resolve stale binary path during daemon upgrade ([2d55f6a](https://github.com/d0ugal/graith/commit/2d55f6a3b337ef36c6ef5048e926c57a697cf23a))
* rewrite if-else chains to switch for gocritic lint ([0c0762d](https://github.com/d0ugal/graith/commit/0c0762db96d8b0e2406b6e220ab0226cf364ca32))

## [0.12.5](https://github.com/d0ugal/graith/compare/v0.12.4...v0.12.5) (2026-04-26)


### Bug Fixes

* exclude _system.* streams from unread count and topic listing ([28e108d](https://github.com/d0ugal/graith/commit/28e108d48a7d9fa2483a1fcc99a13ce6c63f2016))
* scope status bar unread count to session inbox only ([e7ff386](https://github.com/d0ugal/graith/commit/e7ff3869b050c82e59c2999e1305c14d60a728a2))

## [0.12.4](https://github.com/d0ugal/graith/compare/v0.12.3...v0.12.4) (2026-04-26)


### Bug Fixes

* restore n/p as next/prev session, use c for create ([5bbe38b](https://github.com/d0ugal/graith/commit/5bbe38bbb52dde9db5f4e38c0cec02b78d7c0b16))

## [0.12.3](https://github.com/d0ugal/graith/compare/v0.12.2...v0.12.3) (2026-04-26)


### Bug Fixes

* include config dir in sandbox read paths for hook scripts ([3099533](https://github.com/d0ugal/graith/commit/309953329888628f9e90bbb4b31628945c626485))

## [0.12.2](https://github.com/d0ugal/graith/compare/v0.12.1...v0.12.2) (2026-04-26)


### Bug Fixes

* include gr binary and socket paths in sandbox for hooks ([07f955d](https://github.com/d0ugal/graith/commit/07f955d31b0268d70858e5f8a2cd753d40714ea0))
* simplify hooks — call gr directly, drop shell script wrapper ([6086597](https://github.com/d0ugal/graith/commit/60865979a7fc2af8b34cf7aae4f4aeadf212fd0f))
* use correct Claude Code hooks settings schema (matcher+hooks) ([6ec9650](https://github.com/d0ugal/graith/commit/6ec96505b6561ab2cc811bd1bd2cfb923ac30359))

## [0.12.1](https://github.com/d0ugal/graith/compare/v0.12.0...v0.12.1) (2026-04-25)


### Bug Fixes

* auto-include agent config dirs in sandbox read/write paths ([5c2bc86](https://github.com/d0ugal/graith/commit/5c2bc86a85c648047d1a571388858ac27253d6e8))
* use daemon restart instead of reload in homebrew post_install ([dd2d7b0](https://github.com/d0ugal/graith/commit/dd2d7b0b4178d304f74bb7036b52be406c05d935))

## [0.12.0](https://github.com/d0ugal/graith/compare/v0.11.0...v0.12.0) (2026-04-25)


### Features

* add back-and-forth session switching (ctrl+b l) ([a23853f](https://github.com/d0ugal/graith/commit/a23853f57c6412c26af80d8940b96b21b6dd2890)), closes [#164](https://github.com/d0ugal/graith/issues/164)
* add gr restart command and overlay restart action ([6fb118a](https://github.com/d0ugal/graith/commit/6fb118adb63db92774fd7198b93c6762cf440d8b)), closes [#155](https://github.com/d0ugal/graith/issues/155)


### Bug Fixes

* use ~/.config for config path instead of macOS Application Support ([4e19c47](https://github.com/d0ugal/graith/commit/4e19c476eea4ae71aebf4e311a5b22379b3773c3))
* use tuple swap for prevSessionID (gocritic valSwap) ([2c59f0d](https://github.com/d0ugal/graith/commit/2c59f0d528df747026c39b6124f885fb151c159c))

## [0.11.0](https://github.com/d0ugal/graith/compare/v0.10.0...v0.11.0) (2026-04-23)


### Features

* add allowed_repo_paths config to restrict session creation ([5ab3a01](https://github.com/d0ugal/graith/commit/5ab3a01c730885aaee38eb21c60696b0a6bf5eb6))
* add Codex lifecycle hook injection ([8a367ea](https://github.com/d0ugal/graith/commit/8a367ea7f3a7519241e6e16208ba6c319342c7dd))
* add enrichment types to protocol ([4c1ad76](https://github.com/d0ugal/graith/commit/4c1ad762ce3b0cac4e9c2b44f35d987f18acdd35))
* add hook report ingestion and gr report-status command ([44e4cd9](https://github.com/d0ugal/graith/commit/44e4cd911eea32479448868ac6ea58869c2e589e))
* add safehouse checks to gr doctor ([72b0e92](https://github.com/d0ugal/graith/commit/72b0e920dfec338153c9cfa74d634c75d31b34eb))
* add sandbox fields to state, protocol, and CLI ([f703621](https://github.com/d0ugal/graith/commit/f7036211b5437b4097429c51ec3720d692d8215c))
* add sandbox package for safehouse command wrapping ([0e23f71](https://github.com/d0ugal/graith/commit/0e23f71d320e72e2c0fa7c27e5344e3b88b9a794))
* add SandboxConfig to config schema with merge semantics ([bb4d7ba](https://github.com/d0ugal/graith/commit/bb4d7baa87e37ee9abd3a9b17ae2b672861ab954))
* add StatusReportMsg to wire protocol ([976fe55](https://github.com/d0ugal/graith/commit/976fe550e244b226f685fa2e591812947795649d))
* Claude hook injection and authority layer ([4a1c450](https://github.com/d0ugal/graith/commit/4a1c450a62d2501d96e6015aeecddfe2b6621540))
* enrichment data pipeline — cost, tokens, model, tool in UI ([77bf41e](https://github.com/d0ugal/graith/commit/77bf41ecce518b4dd1ff8a512e11d5bea4d478bc))
* wire safehouse sandbox into Create, Resume, and Fork ([a0c0e83](https://github.com/d0ugal/graith/commit/a0c0e83ba73ce3fcee2bdc72a34676aa411a1503))


### Bug Fixes

* address 5 review findings from Codex ([bc95d5f](https://github.com/d0ugal/graith/commit/bc95d5f14e2c091a373ffd99c8a0f51b21340b8a))
* clean up legacy daemon on startup after socket path change ([302e36c](https://github.com/d0ugal/graith/commit/302e36c9b57f2259b8ddb97175c2144c9bedddbc))
* expand ~ and relative paths in sandbox read/write dirs ([876bfa7](https://github.com/d0ugal/graith/commit/876bfa745dd6f4c86e91e030ca653a25552cade6))
* fail closed when sandbox is enabled but safehouse unavailable ([bdcd85d](https://github.com/d0ugal/graith/commit/bdcd85d55961b708ee6bc595fe86045603aa6118))
* honor per-agent sandbox enablement and custom command paths ([aac5fde](https://github.com/d0ugal/graith/commit/aac5fde95cb9bdc737471a15f1634d86038f2110))
* lint — gofmt alignment and switch over if/else chains ([4143a35](https://github.com/d0ugal/graith/commit/4143a3558bec39c7bb5fc1e14a55f8514f41ad23))
* make sandbox config-only, remove CLI override flags ([f0cf954](https://github.com/d0ugal/graith/commit/f0cf954fbba224e46f46eea25441374579921e21))
* move daemon socket fallback out of /tmp ([5998f98](https://github.com/d0ugal/graith/commit/5998f98c2e6ea78c5367c5a0eb9895094a09fc77))
* update module golang.org/x/term to v0.44.0 ([7cef080](https://github.com/d0ugal/graith/commit/7cef0804cc75628339bce3ea8f5692671e6d312d))

## [0.10.0](https://github.com/d0ugal/graith/compare/v0.9.0...v0.10.0) (2026-04-22)


### Features

* fork sessions with agent conversation history ([fd618ad](https://github.com/d0ugal/graith/commit/fd618adce3e2af44083a9efe7ef7a522b9b9a2ab))


### Bug Fixes

* send type input and newline as a single PTY write ([0bf6b0d](https://github.com/d0ugal/graith/commit/0bf6b0d8bdb2ad335446bf57f43b006766452540)), closes [#151](https://github.com/d0ugal/graith/issues/151)

## [0.9.0](https://github.com/d0ugal/graith/compare/v0.8.0...v0.9.0) (2026-04-22)


### Features

* add ctrl+b n (new) and ctrl+b f (fork) keybindings ([f2a8721](https://github.com/d0ugal/graith/commit/f2a8721d45c7181b5e10448503a74261f7824785))

## [0.8.0](https://github.com/d0ugal/graith/compare/v0.7.0...v0.8.0) (2026-04-22)


### Features

* simplify daemon subcommands and auto-reload on brew upgrade ([dde12ec](https://github.com/d0ugal/graith/commit/dde12ec7c96b61ee426e3f3723e5eaf8ad3a7a55))

## [0.7.0](https://github.com/d0ugal/graith/compare/v0.6.1...v0.7.0) (2026-04-22)


### Features

* redesign status bar with colors and fleet summary ([81802d0](https://github.com/d0ugal/graith/commit/81802d08898d351f74d13b0f037edfd1fff30686))


### Bug Fixes

* update module golang.org/x/sync to v0.21.0 ([b2e860c](https://github.com/d0ugal/graith/commit/b2e860c5cfb273a389d6060eed813f113a3429ca))
* update module golang.org/x/sys to v0.46.0 ([bfb3beb](https://github.com/d0ugal/graith/commit/bfb3bebce6bffd09b12e484e9de27c10e63c84c5))

## [0.6.1](https://github.com/d0ugal/graith/compare/v0.6.0...v0.6.1) (2026-04-22)


### Bug Fixes

* reduce unknown agent status after daemon restart ([8db590d](https://github.com/d0ugal/graith/commit/8db590d3058a00e319f6d3229198eac9f486cd3b))
* stop boosting current session to top of sort order ([dc88a8b](https://github.com/d0ugal/graith/commit/dc88a8bdd82c2d22f2806aeddc5dc89501a3c131))
* use byte-bounded scrollback replay and event-based grace period ([f1e6d45](https://github.com/d0ugal/graith/commit/f1e6d45d0504f861c539cd72635d9553fb18bf8a))

## [0.6.0](https://github.com/d0ugal/graith/compare/v0.5.1...v0.6.0) (2026-04-22)


### Features

* color-code session status in overlay ([396ddd3](https://github.com/d0ugal/graith/commit/396ddd354c902fec32edbb7ea0875f4f09387fc8))

## [0.5.1](https://github.com/d0ugal/graith/compare/v0.5.0...v0.5.1) (2026-04-22)


### Bug Fixes

* reset filter cursor and align next/prev session order with overlay ([c8d5b21](https://github.com/d0ugal/graith/commit/c8d5b213a3855524683281ab51214b524cff72d8))

## [0.5.0](https://github.com/d0ugal/graith/compare/v0.4.0...v0.5.0) (2026-04-22)


### Features

* redesign session switcher overlay ([e16addf](https://github.com/d0ugal/graith/commit/e16addf9e2743ad643d6affbdbbad2e829c62e2e)), closes [#80](https://github.com/d0ugal/graith/issues/80)


### Bug Fixes

* add graith binary to .gitignore ([47cbe31](https://github.com/d0ugal/graith/commit/47cbe313f67dc4ebe30b038ee3fcb6e8166db516))

## [0.4.0](https://github.com/d0ugal/graith/compare/v0.3.1...v0.4.0) (2026-04-21)


### Features

* include repo name in worktree directory path ([53ecb47](https://github.com/d0ugal/graith/commit/53ecb47a7e143e1754ea41fa3e50571e48709db3))


### Bug Fixes

* update github.com/charmbracelet/ultraviolet digest to 35bcb73 ([aac69c2](https://github.com/d0ugal/graith/commit/aac69c2c36fe4acedcf79c5cb5aecefacc50aadf))
* update module modernc.org/libc to v1.73.0 ([8aad981](https://github.com/d0ugal/graith/commit/8aad9815f90be30dfa852e9a8ca9812911d78517))

## [0.3.1](https://github.com/d0ugal/graith/compare/v0.3.0...v0.3.1) (2026-04-19)


### Bug Fixes

* delete dev tag before goreleaser snapshot ([823281d](https://github.com/d0ugal/graith/commit/823281d474257e1ace5ebfc68cb32ddbfb4f5b32))

## [0.3.0](https://github.com/d0ugal/graith/compare/v0.2.1...v0.3.0) (2026-04-19)


### Features

* add confirmation prompt for destructive gr delete ([356c8eb](https://github.com/d0ugal/graith/commit/356c8eb9714fe0a5b1b53e477c8884a4e0a50e83)), closes [#49](https://github.com/d0ugal/graith/issues/49)
* add dev release workflow for pre-release builds ([852c4a9](https://github.com/d0ugal/graith/commit/852c4a9e9895d930007b0c3f7817b4c43ee48376))
* add live-updating dashboard command ([925a897](https://github.com/d0ugal/graith/commit/925a8970d4d1f7aa95d336204c9efb1e649a870c)), closes [#24](https://github.com/d0ugal/graith/issues/24)
* add MCP server mode for programmatic session management ([8fdac1c](https://github.com/d0ugal/graith/commit/8fdac1cad4d5db4d2df56c6fad2948964b759df3)), closes [#32](https://github.com/d0ugal/graith/issues/32)
* add message retention/cleanup to prevent unbounded SQLite growth ([f98de76](https://github.com/d0ugal/graith/commit/f98de76170f360327e89002d76a5d18c3475fa5e)), closes [#20](https://github.com/d0ugal/graith/issues/20)
* add shell completions for flag values ([b5b4dca](https://github.com/d0ugal/graith/commit/b5b4dca6c21f2efd738928cf475db4bcf4bfa4dc)), closes [#45](https://github.com/d0ugal/graith/issues/45)
* add status bar renderer ([2683921](https://github.com/d0ugal/graith/commit/2683921403e8e0e0c08248d0361cb5203758ed60))
* add status request/response protocol messages ([b692f6f](https://github.com/d0ugal/graith/commit/b692f6faf33ff92734777150ed51329603cbcb37))
* add status_bar config section ([4796ee5](https://github.com/d0ugal/graith/commit/4796ee57419aff59ff4cd5e17a3dada20490daab))
* add TotalUnread query to message store ([1f9077c](https://github.com/d0ugal/graith/commit/1f9077c769411d35c117ad3be1588093dc997370))
* config hot-reload without daemon restart ([e34e1a9](https://github.com/d0ugal/graith/commit/e34e1a9287ad2c015a4d2a7c023c666847886365)), closes [#40](https://github.com/d0ugal/graith/issues/40)
* dev release publishes gr-dev to existing tap ([4272a8d](https://github.com/d0ugal/graith/commit/4272a8d056e508ed1e738fae838f8d2d4875da88))
* handle status control message in daemon ([9f37923](https://github.com/d0ugal/graith/commit/9f37923fcb9ac8bc5b81d1dcfea064d09fc9d56a))
* include sender and reply hint in msg send notification ([305067a](https://github.com/d0ugal/graith/commit/305067a244a6d35640b0cf366f0c147a3e3a5394))
* integrate status bar into passthrough loop ([c83ff81](https://github.com/d0ugal/graith/commit/c83ff81e377cee13980887aba4d0f80712c65f1a))
* notify agent when sending a direct message ([76e8684](https://github.com/d0ugal/graith/commit/76e868492927d3452a01b9d4ae4558cccac4c564))
* notify when a new graith release is available ([ff024b1](https://github.com/d0ugal/graith/commit/ff024b1fb62bb9a0fc36d7dbb408da80bae841c9)), closes [#65](https://github.com/d0ugal/graith/issues/65)
* show help on naked gr, add subcommand aliases ([35136d9](https://github.com/d0ugal/graith/commit/35136d9990ca145a883f3abf6ce88f19bd213e7d)), closes [#69](https://github.com/d0ugal/graith/issues/69)
* upgrade charmbracelet libraries to v2 (charm.land) ([baa1779](https://github.com/d0ugal/graith/commit/baa17797bb9f8f3f03e429f0d1c90e6f156056ca))
* wire up status bar in attach command ([31b17ee](https://github.com/d0ugal/graith/commit/31b17ee1cb3d2d939344515503b3274baaf4af67))


### Bug Fixes

* address CI review feedback and stdlib vulnerabilities ([08e3f09](https://github.com/d0ugal/graith/commit/08e3f090ee46743688aa524370948cd19d0da566))
* address PR review feedback for overlay tests ([86ae852](https://github.com/d0ugal/graith/commit/86ae85208d34a833aabb034b0758da46e4a89e8f))
* address review feedback on dashboard PR ([2ca782a](https://github.com/d0ugal/graith/commit/2ca782a3269dd32d435e45ee462aee6996f45dce))
* address review feedback on delete confirmation ([7f672c3](https://github.com/d0ugal/graith/commit/7f672c3cc4ec77d884c9fdb7bef1a4b08542d0ac))
* align struct tags to satisfy tagalign linter ([7faec26](https://github.com/d0ugal/graith/commit/7faec2616bedcf567e7756ddff9d67a2f577e0ff))
* close pipe before PTY cleanup to prevent deadlock in CI ([fa568b4](https://github.com/d0ugal/graith/commit/fa568b48dfdf05175a636b553f40189cbd256095))
* improve agent activity status detection reliability ([096b336](https://github.com/d0ugal/graith/commit/096b336c7cd3703ef2fca1fb2640cf8080db43a7))
* remove dead code and unify duplicates ([0ad8049](https://github.com/d0ugal/graith/commit/0ad80492936501ab8be7ecba1f83f88188cb97f4)), closes [#48](https://github.com/d0ugal/graith/issues/48)
* remove ineffectual assignment flagged by golangci-lint ([d44c301](https://github.com/d0ugal/graith/commit/d44c301a1a28e5a0c1c7c3de2e934e21a21b0d4b))
* resolve dupword lint warning in overlay test comment ([229e77c](https://github.com/d0ugal/graith/commit/229e77c066da0e496a968c3467384ce740675f1d))
* stabilize TestSessionEcho on macOS ([c0592d2](https://github.com/d0ugal/graith/commit/c0592d208539ecd6dbd3f93468e98b8b68feb438))
* update fuzz test for Detect's new outputAge parameter ([b02ed0a](https://github.com/d0ugal/graith/commit/b02ed0a89ab36c45e6789e9f5c378b9d9d9ba528))
* update github.com/charmbracelet/ultraviolet digest to 6cf7526 ([acc4777](https://github.com/d0ugal/graith/commit/acc47778ba828b3a78e800862a311ffd68422fc5))
* update module github.com/charmbracelet/colorprofile to v0.4.3 ([8d6674c](https://github.com/d0ugal/graith/commit/8d6674c3e49557d08c734c3908c7faae4d36da54))
* update module github.com/charmbracelet/x/ansi to v0.11.7 ([6e3b6f2](https://github.com/d0ugal/graith/commit/6e3b6f2f31a3681742656bce69416240a784f342))
* update module github.com/mattn/go-isatty to v0.0.22 ([af83489](https://github.com/d0ugal/graith/commit/af83489f1b9f90e05381de846392c1cbf77241ac))
* update module github.com/mattn/go-runewidth to v0.0.24 ([e085988](https://github.com/d0ugal/graith/commit/e0859886190cac6e58f718761713224d1c159357))
* update module github.com/sahilm/fuzzy to v0.1.2 ([c444819](https://github.com/d0ugal/graith/commit/c444819f933af077022f16169d9ec3bf0dab63a7))
* update module github.com/segmentio/asm to v1.2.1 ([e0e6017](https://github.com/d0ugal/graith/commit/e0e60177db8180d590dc50b1618c4b2276be9410))
* update module github.com/spf13/pflag to v1.0.10 ([b56f987](https://github.com/d0ugal/graith/commit/b56f987b78edff3ac7b881642816471e008a022f))
* update module golang.org/x/oauth2 to v0.36.0 ([8eb1852](https://github.com/d0ugal/graith/commit/8eb1852ae718e45b9d4586ebe2f426185a8831eb))
* update module golang.org/x/sys to v0.45.0 ([630081c](https://github.com/d0ugal/graith/commit/630081c1ed3b2fcd9796924c76f353a31ef91390))
* update module golang.org/x/text to v0.37.0 ([037cb93](https://github.com/d0ugal/graith/commit/037cb936b658fa7f6c94108f7f9f1d28c7cb562a))
* update module golang.org/x/tools to v0.45.0 ([74657b1](https://github.com/d0ugal/graith/commit/74657b1d57a5a9fa6616a62fb72f63864a6b497c))
* update module modernc.org/libc to v1.72.5 ([51bafad](https://github.com/d0ugal/graith/commit/51bafada734730997ae85fb9fb08b5478d45ab22))
* update module modernc.org/libc to v1.72.5 ([c0a70c7](https://github.com/d0ugal/graith/commit/c0a70c7533fbcf60412ce08e925fd7dd5e5897d1))
* update module modernc.org/libc to v2 ([8352ea3](https://github.com/d0ugal/graith/commit/8352ea3fa3063dcfa7474920c054047ef3881b59))
* update module modernc.org/sqlite to v1.52.0 ([b627d76](https://github.com/d0ugal/graith/commit/b627d7685badb588e7f282137ed92b5d363451e5))
* update test to use renamed ShortDuration function ([031b10b](https://github.com/d0ugal/graith/commit/031b10b054b9a490d93144210807f893639ec0dd))
* use goreleaser snapshot instead of nightly (pro feature) ([4cd18e7](https://github.com/d0ugal/graith/commit/4cd18e724df3d0d90849cf8fc5595cb6cde1e3c7))
* use internal/git package and include remote branches in --base completion ([bbb0854](https://github.com/d0ugal/graith/commit/bbb0854326c51f713a45c047b87ec77b7377e9ca))
* use protocol.Version after rebase on latest main ([f1b61cd](https://github.com/d0ugal/graith/commit/f1b61cd50eb3fe11044a0cb52095538242ac6840))
* use release --snapshot --skip=publish to produce archives ([04b8c1d](https://github.com/d0ugal/graith/commit/04b8c1d342c7b3d530ddd0b00c4fabb1cc4aab47))
* use tagged switch to satisfy staticcheck QF1003 ([2488a0d](https://github.com/d0ugal/graith/commit/2488a0d57aefd683aad3b4a4f894d94443c0f0d5))

## [0.2.1](https://github.com/d0ugal/graith/compare/v0.2.0...v0.2.1) (2026-04-18)


### Bug Fixes

* resolve golangci-lint warnings in render and handler ([7d72367](https://github.com/d0ugal/graith/commit/7d7236766591a00e904278f39ecda02d223a68ff))
* split goreleaser into separate tag-triggered workflow ([3c134ce](https://github.com/d0ugal/graith/commit/3c134ce88f394baa97bd2fc697d5e02f36a0fcab))

## [0.2.0](https://github.com/d0ugal/graith/compare/v0.1.1...v0.2.0) (2026-04-15)


### Features

* add daemon-side vt10x screen model with ANSI repaint renderer ([2c7c001](https://github.com/d0ugal/graith/commit/2c7c001f970694e7b7ca68069d1d179d4fda9acc))
* add gr approvals command and highlight approval status in list ([1969b35](https://github.com/d0ugal/graith/commit/1969b357f44a61bae563db4760870c7e916d40d9))
* add notifications on agent status changes ([aab02b3](https://github.com/d0ugal/graith/commit/aab02b376e22cb6085ea5be89346c920150ecb1d))
* add screen_snapshot protocol message for color-accurate restore ([7325df7](https://github.com/d0ugal/graith/commit/7325df7017d211c8d64037a10668e22a5927dbaf))
* remove alt screen from overlay to eliminate flash ([01557aa](https://github.com/d0ugal/graith/commit/01557aa19a450d7f20d1f7e219ae88a281968637))
* restore screen with ANSI repaint frame after overlay dismiss ([34933b3](https://github.com/d0ugal/graith/commit/34933b3616bef2957452dae5bdde3f63947d953a))


### Bug Fixes

* attach scrollback replay before registering writer ([#12](https://github.com/d0ugal/graith/issues/12)) ([41f5304](https://github.com/d0ugal/graith/commit/41f53041659dfcd8e94dced18c96ffb378574bab))
* close existing connections on shutdown ([#53](https://github.com/d0ugal/graith/issues/53)) ([815e200](https://github.com/d0ugal/graith/commit/815e2005dbd2a70a6beaa18c66c17e75b77c4aa5))
* goroutine leak in msg_sub follow path ([#13](https://github.com/d0ugal/graith/issues/13)) ([20a0780](https://github.com/d0ugal/graith/commit/20a0780e4743ef57737586de0e0ecc63712ac215))
* guard type assertion in RunOverlay against unexpected exit ([#15](https://github.com/d0ugal/graith/issues/15)) ([fa0f173](https://github.com/d0ugal/graith/commit/fa0f17393dbbede626cbc382fa6ba2cf3b809c41))
* prevent panic on empty keybinding string ([#16](https://github.com/d0ugal/graith/issues/16)) ([fc94d04](https://github.com/d0ugal/graith/commit/fc94d04fec37e033a9f8cb3482bf6df8e32aa354))
* release lock before blocking waits in StopAll and Delete ([#52](https://github.com/d0ugal/graith/issues/52)) ([9144c12](https://github.com/d0ugal/graith/commit/9144c125d8b3e7275d6fdd4a0ae86de3add9a87e))
* session Close races with readLoop on scrollback ([#14](https://github.com/d0ugal/graith/issues/14)) ([73c78ab](https://github.com/d0ugal/graith/commit/73c78abafacb29e384f64b4905c43253ba4815fa))
* surface config parse errors instead of silent fallback ([#17](https://github.com/d0ugal/graith/issues/17)) ([d7f6f85](https://github.com/d0ugal/graith/commit/d7f6f8539ae18ae71878dabab11aac3935a4914a))

## [0.1.1](https://github.com/d0ugal/graith/compare/v0.1.0...v0.1.1) (2026-04-15)


### Bug Fixes

* fetch tags after release-please before goreleaser ([ab2817e](https://github.com/d0ugal/graith/commit/ab2817eb90fc033143b07de1dd3deda90989ec21))

## [0.1.0](https://github.com/d0ugal/graith/compare/v0.0.1...v0.1.0) (2026-04-15)


### Features

* add ctrl+b n/p shortcuts to cycle through sessions ([#1](https://github.com/d0ugal/graith/issues/1)) ([ed6e48d](https://github.com/d0ugal/graith/commit/ed6e48db9b0ea8718a954f0bdd164e73cd57f18c))
* add gr type command for PTY input injection ([#5](https://github.com/d0ugal/graith/issues/5)) ([0284b40](https://github.com/d0ugal/graith/commit/0284b40a7e066f17e7570b55395745f58b19149a))
* add scrollback preview background to session overlay ([#4](https://github.com/d0ugal/graith/issues/4)) ([f100972](https://github.com/d0ugal/graith/commit/f10097231dab609186bcecce6a4614bb5dc5dd59))
* auto-stop idle sessions after configurable timeout ([#3](https://github.com/d0ugal/graith/issues/3)) ([227e389](https://github.com/d0ugal/graith/commit/227e3890dea5c1f35a0d9933a066cbe71066f9ae))


### Bug Fixes

* align overlay columns with fixed-width padding ([edc6a44](https://github.com/d0ugal/graith/commit/edc6a4473c1976d6c4a4750b1459760795362008))
* drain PTY output before signalling session done ([95bc31f](https://github.com/d0ugal/graith/commit/95bc31f32f1b3069f8f6e9a370512815325fd067))
* dynamic overlay panel size and improved ANSI stripping ([6902c95](https://github.com/d0ugal/graith/commit/6902c95f6b6ac62c3b1a3a3e2433f41f231ddd1a))
* preview not loading on overlay open ([4acdd11](https://github.com/d0ugal/graith/commit/4acdd11e29d4d53a61cd22777fdf020daf2470fb))
* resize PTY to attaching client's terminal on attach ([26895ce](https://github.com/d0ugal/graith/commit/26895ce404d565cab62e0fc96be0c6f9d2fec0d3))
* show n/p shortcuts in overlay help bar ([6b47a43](https://github.com/d0ugal/graith/commit/6b47a436a937f1a9bd759382de0af69ffd581708))
* sort sessions within overlay groups alphabetically ([fe69957](https://github.com/d0ugal/graith/commit/fe6995759128bbf1118780362c9c46de4285e7f0))
* update type.go imports to d0ugal module path ([a66afc3](https://github.com/d0ugal/graith/commit/a66afc3b7cedbc96c7994009c8e45eb8e2253184))
* use ansi-aware truncation for overlay columns and preview ([d17dba1](https://github.com/d0ugal/graith/commit/d17dba1212c5d6040c1062bf8e6e1ba9d2b16ace))
* use RELEASE_TOKEN for release-please to trigger CI on PRs ([011fc0a](https://github.com/d0ugal/graith/commit/011fc0a0daf13291a0c71298f41293e6910184ac))
* use VT emulator for scrollback preview rendering ([083803d](https://github.com/d0ugal/graith/commit/083803df014c54bd36bc19d15c68d5e9e14a1482))
