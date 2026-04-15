# Changelog

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
