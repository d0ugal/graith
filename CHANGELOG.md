# Changelog

## [0.3.0](https://github.com/d0ugal/graith/compare/v0.2.1...v0.3.0) (2026-04-19)


### Features

* add confirmation prompt for destructive gr delete ([bc3cb14](https://github.com/d0ugal/graith/commit/bc3cb141df091c7e33175dc235fecfedd142921a)), closes [#49](https://github.com/d0ugal/graith/issues/49)
* add dev release workflow for pre-release builds ([74f9f51](https://github.com/d0ugal/graith/commit/74f9f5166cb8755f6458ce4c3ecd98b35dd2f9d0))
* add live-updating dashboard command ([05d4d4d](https://github.com/d0ugal/graith/commit/05d4d4d94d2a2a4f0364c1f2285e9cd61c2a24fb)), closes [#24](https://github.com/d0ugal/graith/issues/24)
* add MCP server mode for programmatic session management ([96ca8b8](https://github.com/d0ugal/graith/commit/96ca8b83b1cbeb1b21d24292105a79beae9b641b)), closes [#32](https://github.com/d0ugal/graith/issues/32)
* add message retention/cleanup to prevent unbounded SQLite growth ([2231b14](https://github.com/d0ugal/graith/commit/2231b144eadc8e38aa22d4166714bb3b309e3eb4)), closes [#20](https://github.com/d0ugal/graith/issues/20)
* add shell completions for flag values ([0820412](https://github.com/d0ugal/graith/commit/0820412ffd7a07bd8e32cf78c098a5261ac9b53d)), closes [#45](https://github.com/d0ugal/graith/issues/45)
* add status bar renderer ([a235db1](https://github.com/d0ugal/graith/commit/a235db14be197f5e0ad3b5b41cce5b8e36875abb))
* add status request/response protocol messages ([292792e](https://github.com/d0ugal/graith/commit/292792e88536fbf46aa975206a6292e84c87f021))
* add status_bar config section ([98e9a3d](https://github.com/d0ugal/graith/commit/98e9a3d5389463eae517b5c69ed82d90b4b844ba))
* add TotalUnread query to message store ([765b558](https://github.com/d0ugal/graith/commit/765b558056243041a7077e8de78cd5d4d4aa5fac))
* config hot-reload without daemon restart ([602be96](https://github.com/d0ugal/graith/commit/602be96a25e7dae68b821c5e5bab7095f371b89d)), closes [#40](https://github.com/d0ugal/graith/issues/40)
* dev release publishes gr-dev to existing tap ([a860eb4](https://github.com/d0ugal/graith/commit/a860eb40bb296e36f89c1b2e40d666dfd36480cd))
* handle status control message in daemon ([9b4455f](https://github.com/d0ugal/graith/commit/9b4455fa5bef6d88172e7e7aaf993440d76def8f))
* include sender and reply hint in msg send notification ([d244b61](https://github.com/d0ugal/graith/commit/d244b61e9be27008d84f6c1cc9849dfeb236c8bc))
* integrate status bar into passthrough loop ([a0c65ed](https://github.com/d0ugal/graith/commit/a0c65edff7074e145b83b931672fbc36406f92ff))
* notify agent when sending a direct message ([be13fc1](https://github.com/d0ugal/graith/commit/be13fc181ff16b5a290f6a440c199f77dfdd71f0))
* notify when a new graith release is available ([bda8198](https://github.com/d0ugal/graith/commit/bda81987d8911f9f9bdca8e294cca0227d019d1a)), closes [#65](https://github.com/d0ugal/graith/issues/65)
* show help on naked gr, add subcommand aliases ([61b2e0b](https://github.com/d0ugal/graith/commit/61b2e0b63bfc03fd92fa4e6d9466becfb428f783)), closes [#69](https://github.com/d0ugal/graith/issues/69)
* upgrade charmbracelet libraries to v2 (charm.land) ([ca8efe6](https://github.com/d0ugal/graith/commit/ca8efe6551705e3564e1eadd9f6cbc718a0534e1))
* wire up status bar in attach command ([bbabaf2](https://github.com/d0ugal/graith/commit/bbabaf29fa425a44dc132cce59d3887b414c7c32))


### Bug Fixes

* address CI review feedback and stdlib vulnerabilities ([67951c6](https://github.com/d0ugal/graith/commit/67951c65f7454a9db74e4ef24ff8180072c5a225))
* address PR review feedback for overlay tests ([771002e](https://github.com/d0ugal/graith/commit/771002e282061cf2cd097e267e28ccbabada178d))
* address review feedback on dashboard PR ([e51b1bd](https://github.com/d0ugal/graith/commit/e51b1bdb91c82a435d1e1228b8a36eac42fc634a))
* address review feedback on delete confirmation ([3cf8049](https://github.com/d0ugal/graith/commit/3cf804921e9e65bb40b70759881fb1f7b226b477))
* align struct tags to satisfy tagalign linter ([f1295b2](https://github.com/d0ugal/graith/commit/f1295b2d2256d7860b7f13bb6fdc94cf9cdbad9a))
* close pipe before PTY cleanup to prevent deadlock in CI ([c793cab](https://github.com/d0ugal/graith/commit/c793cab365ed7b29fa6c4c60f650aa08d81b1011))
* improve agent activity status detection reliability ([69c0c3a](https://github.com/d0ugal/graith/commit/69c0c3a02cb3e7114596294df50309cb5df08fc0))
* remove dead code and unify duplicates ([0980752](https://github.com/d0ugal/graith/commit/098075220447210ad14e855146cf4c895582c640)), closes [#48](https://github.com/d0ugal/graith/issues/48)
* remove ineffectual assignment flagged by golangci-lint ([0c14cdf](https://github.com/d0ugal/graith/commit/0c14cdf113903f0d9d6f602811321af69fb07ef8))
* resolve dupword lint warning in overlay test comment ([566da11](https://github.com/d0ugal/graith/commit/566da1145122d6cfff983b3855578ddc77075feb))
* stabilize TestSessionEcho on macOS ([0ca23b9](https://github.com/d0ugal/graith/commit/0ca23b9a4aa50b2796a43e1c0ef88094eb5b0d85))
* update fuzz test for Detect's new outputAge parameter ([32114f4](https://github.com/d0ugal/graith/commit/32114f4e4f5b67a03cb947696943f1c7de19d46e))
* update github.com/charmbracelet/ultraviolet digest to 6cf7526 ([6a3516b](https://github.com/d0ugal/graith/commit/6a3516b87f2cf1c14a874498191f413a904a44d1))
* update module github.com/charmbracelet/colorprofile to v0.4.3 ([1024ac1](https://github.com/d0ugal/graith/commit/1024ac111d935d1427ffcd24458fb26d2c8c600b))
* update module github.com/charmbracelet/x/ansi to v0.11.7 ([83439d4](https://github.com/d0ugal/graith/commit/83439d485ee55fc0f6d0cc8bb1474b389100d4ed))
* update module github.com/mattn/go-isatty to v0.0.22 ([9d6dbaa](https://github.com/d0ugal/graith/commit/9d6dbaa59e54e56699ab4d681d1fd8c838f1b055))
* update module github.com/mattn/go-runewidth to v0.0.24 ([e695bc7](https://github.com/d0ugal/graith/commit/e695bc71a7c70265a5549ec2c4997ce5894a5240))
* update module github.com/sahilm/fuzzy to v0.1.2 ([8ca7de9](https://github.com/d0ugal/graith/commit/8ca7de99ed04b6684f9ebfd7444a8b83a960dfdc))
* update module github.com/segmentio/asm to v1.2.1 ([deec1bd](https://github.com/d0ugal/graith/commit/deec1bd156068ee03d17ac1f8630d13b66f98017))
* update module github.com/spf13/pflag to v1.0.10 ([da5513a](https://github.com/d0ugal/graith/commit/da5513a0471d93097685d32af9387a134e31a7aa))
* update module golang.org/x/oauth2 to v0.36.0 ([5466212](https://github.com/d0ugal/graith/commit/5466212a7cf86cafc451813bf843197c70c97b79))
* update module golang.org/x/sys to v0.45.0 ([bb751b3](https://github.com/d0ugal/graith/commit/bb751b38c188b91a59cddeeaf58ebb2c6476a82e))
* update module golang.org/x/text to v0.37.0 ([0ee4448](https://github.com/d0ugal/graith/commit/0ee44481e2d6871c2b43706102be2d19e8e8cc07))
* update module golang.org/x/tools to v0.45.0 ([6d5290a](https://github.com/d0ugal/graith/commit/6d5290a7fbb3032f57940e4a070666ac5722edce))
* update module modernc.org/libc to v1.72.5 ([005b1a7](https://github.com/d0ugal/graith/commit/005b1a7d1748e9e512f149ceeefd02c29b49f6b5))
* update module modernc.org/libc to v1.72.5 ([44cfa05](https://github.com/d0ugal/graith/commit/44cfa052a2991d80725e3909d82e2f6bad311e74))
* update module modernc.org/libc to v2 ([2c880d2](https://github.com/d0ugal/graith/commit/2c880d2412aa4f8ad7ccb198c5be23e8e4706c08))
* update module modernc.org/sqlite to v1.52.0 ([d1fb952](https://github.com/d0ugal/graith/commit/d1fb9521b299d39e2cdca39c75d2021c5c0fb4e8))
* update test to use renamed ShortDuration function ([a98e87f](https://github.com/d0ugal/graith/commit/a98e87fb7cf60159f7794010a3d1e84437addf2a))
* use goreleaser snapshot instead of nightly (pro feature) ([b501f15](https://github.com/d0ugal/graith/commit/b501f15ca0524b0d8c79476e8b0be6247a9c0af3))
* use internal/git package and include remote branches in --base completion ([6ee9f4e](https://github.com/d0ugal/graith/commit/6ee9f4e14fd1615aa5364a72da5ccf574a5ac0dd))
* use protocol.Version after rebase on latest main ([0df2abd](https://github.com/d0ugal/graith/commit/0df2abda31c0737426ead26bd2b691c636c50399))
* use release --snapshot --skip=publish to produce archives ([3cec9bf](https://github.com/d0ugal/graith/commit/3cec9bfc354f80b65a56a88ffbfa0eb49d47e237))
* use tagged switch to satisfy staticcheck QF1003 ([89081f2](https://github.com/d0ugal/graith/commit/89081f2b9006d401f23b852b6b85495209793285))

## [0.2.1](https://github.com/d0ugal/graith/compare/v0.2.0...v0.2.1) (2026-04-18)


### Bug Fixes

* resolve golangci-lint warnings in render and handler ([850f706](https://github.com/d0ugal/graith/commit/850f706125f53fac75786ed015ff0c31993b042b))
* split goreleaser into separate tag-triggered workflow ([a1719aa](https://github.com/d0ugal/graith/commit/a1719aacbbcb6a25675693a4f63627df9c2afc94))

## [0.2.0](https://github.com/d0ugal/graith/compare/v0.1.1...v0.2.0) (2026-04-15)


### Features

* add daemon-side vt10x screen model with ANSI repaint renderer ([5b31c21](https://github.com/d0ugal/graith/commit/5b31c21711f744e4a9fb769afbff14956a25e28f))
* add gr approvals command and highlight approval status in list ([1abe27d](https://github.com/d0ugal/graith/commit/1abe27d91dec1dea8501412e9806aa8ba9524e8d))
* add notifications on agent status changes ([04e48d6](https://github.com/d0ugal/graith/commit/04e48d68745ec2f2355bcea0261396c5c7c1a52a))
* add screen_snapshot protocol message for color-accurate restore ([8a1bf0f](https://github.com/d0ugal/graith/commit/8a1bf0f5dededbbdbede5fb269b4c26b7aa355f0))
* remove alt screen from overlay to eliminate flash ([e4f67be](https://github.com/d0ugal/graith/commit/e4f67bef2e28a29c91492316ee53a190de168e89))
* restore screen with ANSI repaint frame after overlay dismiss ([1ae353c](https://github.com/d0ugal/graith/commit/1ae353c369a8f00d65f3c36914b062436a5b75e5))


### Bug Fixes

* attach scrollback replay before registering writer ([#12](https://github.com/d0ugal/graith/issues/12)) ([8f83170](https://github.com/d0ugal/graith/commit/8f83170c13f34b18f9dd57a532a49a7071b33041))
* close existing connections on shutdown ([#53](https://github.com/d0ugal/graith/issues/53)) ([13530aa](https://github.com/d0ugal/graith/commit/13530aaa2b4cfed180db17d3a43864259b476291))
* goroutine leak in msg_sub follow path ([#13](https://github.com/d0ugal/graith/issues/13)) ([2b2e776](https://github.com/d0ugal/graith/commit/2b2e776305111e2427ea358629528daf1c02e662))
* guard type assertion in RunOverlay against unexpected exit ([#15](https://github.com/d0ugal/graith/issues/15)) ([ef8f06d](https://github.com/d0ugal/graith/commit/ef8f06dcbc5efbae5498b9f8e96e7872a438af8c))
* prevent panic on empty keybinding string ([#16](https://github.com/d0ugal/graith/issues/16)) ([de88a9f](https://github.com/d0ugal/graith/commit/de88a9f06f54b28fdc06aea9981c92029166d2eb))
* release lock before blocking waits in StopAll and Delete ([#52](https://github.com/d0ugal/graith/issues/52)) ([3b47e2e](https://github.com/d0ugal/graith/commit/3b47e2ec450cc447706aa8708600211455c93ac2))
* session Close races with readLoop on scrollback ([#14](https://github.com/d0ugal/graith/issues/14)) ([d205fbf](https://github.com/d0ugal/graith/commit/d205fbfcb2324f7907292ec343efbd2ab2397588))
* surface config parse errors instead of silent fallback ([#17](https://github.com/d0ugal/graith/issues/17)) ([cc58969](https://github.com/d0ugal/graith/commit/cc58969b81c71004132c6b093aad9871e46d1292))

## [0.1.1](https://github.com/d0ugal/graith/compare/v0.1.0...v0.1.1) (2026-04-15)


### Bug Fixes

* fetch tags after release-please before goreleaser ([b3406e4](https://github.com/d0ugal/graith/commit/b3406e4bbd3f65a5fdfc3cb9d57324c0fb1721ed))

## [0.1.0](https://github.com/d0ugal/graith/compare/v0.0.1...v0.1.0) (2026-04-15)


### Features

* add ctrl+b n/p shortcuts to cycle through sessions ([#1](https://github.com/d0ugal/graith/issues/1)) ([5d22c9b](https://github.com/d0ugal/graith/commit/5d22c9bc0fc20aa9dec96a7d2c541f0056669ba5))
* add gr type command for PTY input injection ([#5](https://github.com/d0ugal/graith/issues/5)) ([eb4d096](https://github.com/d0ugal/graith/commit/eb4d0969180ac0fc05d5793c7ddc2803854acc14))
* add scrollback preview background to session overlay ([#4](https://github.com/d0ugal/graith/issues/4)) ([9cd1afd](https://github.com/d0ugal/graith/commit/9cd1afd291d82caa8687d30282bf0f86fb406b17))
* auto-stop idle sessions after configurable timeout ([#3](https://github.com/d0ugal/graith/issues/3)) ([c489fb7](https://github.com/d0ugal/graith/commit/c489fb7ac77f8e1c43a5d06a4fade422c84b6c64))


### Bug Fixes

* align overlay columns with fixed-width padding ([8c6e435](https://github.com/d0ugal/graith/commit/8c6e4355d87e929d0239187be224461e8ac2b8b1))
* drain PTY output before signalling session done ([63179a1](https://github.com/d0ugal/graith/commit/63179a15df2820060129887db190df0bfa775712))
* dynamic overlay panel size and improved ANSI stripping ([a1846ad](https://github.com/d0ugal/graith/commit/a1846ad50b54a692d4307ce034b5e9897445422f))
* preview not loading on overlay open ([4cd982d](https://github.com/d0ugal/graith/commit/4cd982d7e21bfc47cd72511758c8e4bd803b83d6))
* resize PTY to attaching client's terminal on attach ([829fe72](https://github.com/d0ugal/graith/commit/829fe7248751a5adeeea1819e8282e5a7cbe809c))
* show n/p shortcuts in overlay help bar ([5e95f63](https://github.com/d0ugal/graith/commit/5e95f6366a1c459fd0daeb717dbe6fcaaec98426))
* sort sessions within overlay groups alphabetically ([a794780](https://github.com/d0ugal/graith/commit/a794780957578f71ab4c73c2069b47d0bc64b8ac))
* update type.go imports to d0ugal module path ([45187bc](https://github.com/d0ugal/graith/commit/45187bc975cf3ef1e4a1cdd7bad838dcb652b0fd))
* use ansi-aware truncation for overlay columns and preview ([2d0fea7](https://github.com/d0ugal/graith/commit/2d0fea7dbf39149ec9989f71c0167a18e2897519))
* use RELEASE_TOKEN for release-please to trigger CI on PRs ([7ecdf32](https://github.com/d0ugal/graith/commit/7ecdf32f2146456b5c1c77fc8ae4b6503a39f19f))
* use VT emulator for scrollback preview rendering ([cb463bd](https://github.com/d0ugal/graith/commit/cb463bdb4556d269dd3f456436936bb2d868e237))
