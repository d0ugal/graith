# Changelog

## [0.20.0](https://github.com/d0ugal/graith/compare/v0.19.0...v0.20.0) (2026-06-11)


### Features

* add parent/child relationships to sessions ([cff6ec1](https://github.com/d0ugal/graith/commit/cff6ec108deac43497a04d3af648c9e4018f763a))

## [0.19.0](https://github.com/d0ugal/graith/compare/v0.18.1...v0.19.0) (2026-06-11)


### Features

* add includes and singleton config, IncludedRepoState, git worktree helpers ([0172775](https://github.com/d0ugal/graith/commit/0172775919dd21974325b18945ce5a30e2c54ef0))
* externalize config defaults and add gr config commands ([ddcb24a](https://github.com/d0ugal/graith/commit/ddcb24a64c91f342a1f57de3eb569d2d00222bdb))
* implement multi-repo includes sessions ([164d195](https://github.com/d0ugal/graith/commit/164d19543dcef4fa9022ac2364593a5cb16a200a))


### Bug Fixes

* ack inbox messages in check-inbox hook to prevent duplicates [#277](https://github.com/d0ugal/graith/issues/277) ([9980053](https://github.com/d0ugal/graith/commit/99800531dd81a3004fb599a0b3bf4bda6243164d))
* add default sandbox paths for agy/gemini agent [#207](https://github.com/d0ugal/graith/issues/207) ([a1da7d7](https://github.com/d0ugal/graith/commit/a1da7d75bc4780f042b2d2ce976eb06d154d2664))
* add regression test for check-inbox ack at the CLI layer ([e6ac977](https://github.com/d0ugal/graith/commit/e6ac9779dfbe08fcb4eab2658c4c40d730fb2493))
* address code review feedback for config commands ([9316ff0](https://github.com/d0ugal/graith/commit/9316ff03c4cbd3fb40ae48af0a4423ec7f0a649d))
* address review tribunal findings for multi-repo includes ([68ab932](https://github.com/d0ugal/graith/commit/68ab9322d8db7a831d56a6b9eec52a80eecfe59b))
* also clean cwd and add trailing-slash test cases ([2435289](https://github.com/d0ugal/graith/commit/2435289dfd0b69f891640c527cf26e066a41a3b8))
* also shell-quote gr binary path in Claude hook commands ([ff145e0](https://github.com/d0ugal/graith/commit/ff145e07717848a11e11cc1e3777cd8fc7ce4eca))
* clean up git worktree in TestForkUsesSourceBaseBranch ([404e44f](https://github.com/d0ugal/graith/commit/404e44f3989572ba52acfcfc58a254241ce1008e))
* clear connection deadline after handshake in ConnectFast/ConnectForApproval ([dca858c](https://github.com/d0ugal/graith/commit/dca858cbf47b81b4d2b3df11789a6638c6274d5c)), closes [#224](https://github.com/d0ugal/graith/issues/224)
* consume unterminated OSC sequences in StripANSI [#278](https://github.com/d0ugal/graith/issues/278) ([c6dbf18](https://github.com/d0ugal/graith/commit/c6dbf18e37974d0e10f6b6983e3b4071ebd75c36))
* convert if-else chain to switch to satisfy gocritic linter ([f7d94fe](https://github.com/d0ugal/graith/commit/f7d94fe3e738210236051b3570f082e13354838c))
* correct claude fork args to use --resume with --fork-session ([ca7fb77](https://github.com/d0ugal/graith/commit/ca7fb77c6935e49bfbeabd4493d4eaecda9cf446))
* escape single quotes in gr binary path for codex hook scripts [#252](https://github.com/d0ugal/graith/issues/252) ([b173c37](https://github.com/d0ugal/graith/commit/b173c372693823100240c46051aec400fa695f5c))
* exercise formatToolDetail paths in narrow terminal test ([8d3a66a](https://github.com/d0ugal/graith/commit/8d3a66a5ee6160b7456c6cb235ee49d8c2c48067))
* include never-attached sessions in --stale filter [#262](https://github.com/d0ugal/graith/issues/262) ([bceb9cc](https://github.com/d0ugal/graith/commit/bceb9cc9d8c850778f14ca615544d370035da330))
* log saveState error on attach instead of discarding it ([582ed1f](https://github.com/d0ugal/graith/commit/582ed1fb363b94dca4b3a433be33004f1ca789e8))
* make initTempGitRepo deterministic with git init -b main ([fcac9d8](https://github.com/d0ugal/graith/commit/fcac9d8adb87d5387cd8d16ca034672183d0366b))
* output errors as JSON when --json flag is set [#269](https://github.com/d0ugal/graith/issues/269) ([c836c2d](https://github.com/d0ugal/graith/commit/c836c2d6e6e4df32c417110dd2e9334ad4011cb7))
* persist LastAttachedAt to disk on attach ([#279](https://github.com/d0ugal/graith/issues/279)) ([f7e2401](https://github.com/d0ugal/graith/commit/f7e2401e8863ebbc583183de0c59b6895efea414))
* prevent panic in approval overlay on narrow terminals [#271](https://github.com/d0ugal/graith/issues/271) ([d4c0b32](https://github.com/d0ugal/graith/commit/d4c0b320f23ea8be94001ead3639e4eb7fbff93b))
* reject negative durations in ParseDurationWithDays [#230](https://github.com/d0ugal/graith/issues/230) ([a137769](https://github.com/d0ugal/graith/commit/a137769c65b5e9487b10958016348e1460107848))
* reject null/empty payloads in DecodePayload ([#268](https://github.com/d0ugal/graith/issues/268)) ([c6e8d49](https://github.com/d0ugal/graith/commit/c6e8d49eb4950f9c860814b0eed381869602d7b6))
* resolve sender name from session state in msg_pub handler ([#270](https://github.com/d0ugal/graith/issues/270)) ([369c3e6](https://github.com/d0ugal/graith/commit/369c3e619963a33cdcca16af9a478d947d10c87e))
* return error when both --prompt and --prompt-file are specified ([3df3a10](https://github.com/d0ugal/graith/commit/3df3a1043389ccdefd10b6bd8aa912a83afcd5e9)), closes [#234](https://github.com/d0ugal/graith/issues/234)
* support local-only repos in session creation [#267](https://github.com/d0ugal/graith/issues/267) ([754b827](https://github.com/d0ugal/graith/commit/754b827f6b6ac56458554d74685a3ab588b6d6b1))
* update module github.com/sahilm/fuzzy to v0.1.3 ([481bce3](https://github.com/d0ugal/graith/commit/481bce3376c2cbac8f7e7bd1230c393de5ff4050))
* use CreateTemp for atomic config reset to guarantee 0600 permissions ([9b233a5](https://github.com/d0ugal/graith/commit/9b233a5a2128e7dd32f82d242d7bd02201bb6318))
* use nearest OSC terminator instead of preferring BEL ([9ad5933](https://github.com/d0ugal/graith/commit/9ad5933ce209c62098c593702063b90c4daf1c9c))
* use path-boundary matching in info command ([52f401c](https://github.com/d0ugal/graith/commit/52f401cfab9f78451538cdaedb5ed97c5dbb594b)), closes [#231](https://github.com/d0ugal/graith/issues/231)
* use source.BaseBranch in fork instead of source.Branch [#255](https://github.com/d0ugal/graith/issues/255) ([9790752](https://github.com/d0ugal/graith/commit/97907527f6b645a4d0a1fcb86c8c5ab15b7e5650))

## [0.18.1](https://github.com/d0ugal/graith/compare/v0.18.0...v0.18.1) (2026-06-10)


### Bug Fixes

* always watch config file for changes and log sandbox config diffs ([2a2307b](https://github.com/d0ugal/graith/commit/2a2307b7b708b7da22e39b333dba75b64d952336))
* log full sandbox opts (read_dirs, write_dirs, features, workdir) on session create/fork/resume ([6ae9f0e](https://github.com/d0ugal/graith/commit/6ae9f0e7bd1f854068c6e39165b512f90ce6b72e))

## [0.18.0](https://github.com/d0ugal/graith/compare/v0.17.0...v0.18.0) (2026-06-10)


### Features

* add GRAITH_PROFILE support to config layer ([fb01feb](https://github.com/d0ugal/graith/commit/fb01feba6fa232a3d9abe5164be444fb387b2e4f))
* add in-place sessions for repos without remotes ([39fd832](https://github.com/d0ugal/graith/commit/39fd8327b6e816b2bbd28f11d7608ad15c4237de))
* add profile to handshake protocol with shared builder and mismatch rejection ([530f3b2](https://github.com/d0ugal/graith/commit/530f3b24ffe2df5d5bf73f9e15de359212bfeba7))
* propagate GRAITH_PROFILE to agent env and guard legacy cleanup ([e6de49b](https://github.com/d0ugal/graith/commit/e6de49b64c42430272911b09cf33c2ce94700ad1))
* show profile indicator in overlay, list, and doctor for non-default profiles ([b6fdd2f](https://github.com/d0ugal/graith/commit/b6fdd2fa58067c66b7c0749a3db6a46c033eedb0))


### Bug Fixes

* address Codex review findings for GRAITH_PROFILE ([0779e71](https://github.com/d0ugal/graith/commit/0779e710654752ac7a9eabd5e6232cdfe21ecf51))
* address Codex review findings for in-place sessions ([1de9965](https://github.com/d0ugal/graith/commit/1de9965a6ccbe4c46ca6bf87cb992f024be5ad5c))
* align struct tags in MCP CreateSessionInput to satisfy tagalign linter ([3f6cdc9](https://github.com/d0ugal/graith/commit/3f6cdc9b8074add0c54039a909a015dfafbc237f))
* resolve profile independently of --config path in LoadOrDefault ([3628989](https://github.com/d0ugal/graith/commit/3628989481065f4efdd5f57dffbe7fbfc52f20e0))

## [0.17.0](https://github.com/d0ugal/graith/compare/v0.16.7...v0.17.0) (2026-06-10)


### Features

* expand globs in sandbox read_dirs and write_dirs ([74ce38b](https://github.com/d0ugal/graith/commit/74ce38bfcd298f2ed39057d1dcb229a794e68553))


### Bug Fixes

* allow duplicate session names ([78ddf87](https://github.com/d0ugal/graith/commit/78ddf870de488e07044446c21c7f7f61398a0338))
* remove trailing newline in TestRename to pass whitespace linter ([b4e3a82](https://github.com/d0ugal/graith/commit/b4e3a82bb888f7a7e0df7c7b7c2888bd0b8a986e))

## [0.16.7](https://github.com/d0ugal/graith/compare/v0.16.6...v0.16.7) (2026-06-10)


### Bug Fixes

* use os.MkdirTemp with retry cleanup in TestResumeResetsIdleSince ([27cd1c4](https://github.com/d0ugal/graith/commit/27cd1c489e58038ebaee63ef75a281aa4f32775a))

## [0.16.6](https://github.com/d0ugal/graith/compare/v0.16.5...v0.16.6) (2026-06-10)


### Bug Fixes

* clean up PTY session in TestResumeResetsIdleSince to avoid TempDir race ([44e91b2](https://github.com/d0ugal/graith/commit/44e91b2ac5b220b57aa3d46987f9e2b35ed30725))
* clear IdleSince on Resume, make watchSession tests deterministic ([26af24b](https://github.com/d0ugal/graith/commit/26af24b75b5966aeee31a7a7402a16a26aa4324e))
* prevent stale watchSession from corrupting resumed session state ([aa6e5f3](https://github.com/d0ugal/graith/commit/aa6e5f3a996f6c3b1711ec8dd4a37de3b35fc1ae))
* restore exec upgrade for auto-restart to preserve sessions ([d4e3ea3](https://github.com/d0ugal/graith/commit/d4e3ea3700f90b6bd91c02199191c556fb30b22b))
* satisfy SA2001 by reading state inside the lock barrier ([1620306](https://github.com/d0ugal/graith/commit/16203068fa04f97a03542f8fe3564d46bca0c157))
* synchronous PTY cleanup in TestResumeResetsIdleSince ([0926259](https://github.com/d0ugal/graith/commit/092625946511a05737d89c6b7dc00696ddfc07a0))
* use single tmpDir with LogDir set in TestResumeResetsIdleSince ([fb9ac6b](https://github.com/d0ugal/graith/commit/fb9ac6b243e16e45ad795517ac87bbbe87c14e3a))
* verify daemon version after exec upgrade to catch stale restarts ([4432d73](https://github.com/d0ugal/graith/commit/4432d73447bbe4403eb3882a640f91372532459d))

## [0.16.5](https://github.com/d0ugal/graith/compare/v0.16.4...v0.16.5) (2026-06-10)


### Bug Fixes

* address code review feedback on overlay delete safety ([63d8578](https://github.com/d0ugal/graith/commit/63d85787d3e83cc6ceb5d41fabc47062c6a2f179))
* address review feedback — snapshot os.Environ, harden dupe test ([511cc41](https://github.com/d0ugal/graith/commit/511cc41e55725f430699516b7a3acb376d4c42e6))
* align struct tags and remove unused sandboxOpts method ([217c591](https://github.com/d0ugal/graith/commit/217c59144cf894abdc8bb71b280f204f6417a6ce))
* backfill stream_hwm on upgrade, use monotonic upsert ([e9d509d](https://github.com/d0ugal/graith/commit/e9d509d96aa677293fa7f2cdab6df000d2a6da93))
* bind dashboard delete/stop confirmation to session ID, not cursor index ([4d4c8b3](https://github.com/d0ugal/graith/commit/4d4c8b30cf89088449d5b8d9992c39800fd8ec55)), closes [#237](https://github.com/d0ugal/graith/issues/237)
* cancel stop confirmation when target session stops during refresh ([3688e1e](https://github.com/d0ugal/graith/commit/3688e1e7eb8986595699aa66ea33c223ba20f071))
* clamp approval deadline and improve API per code review ([f288247](https://github.com/d0ugal/graith/commit/f2882473cea02f4852b7f334243e18140df87fd3))
* clean up log file on Create/Fork rollback ([5362de2](https://github.com/d0ugal/graith/commit/5362de2cb053132b8e25bcc9e49706d4bdd91950))
* close connection when kicking replaced attach client ([9668f4b](https://github.com/d0ugal/graith/commit/9668f4bb13b765387514885e96714a19f78a3eab)), closes [#264](https://github.com/d0ugal/graith/issues/264)
* eliminate watchSession race and fd leak in saveState rollback ([1dc88eb](https://github.com/d0ugal/graith/commit/1dc88ebb39f8bef5ef9308071d170cd4c2298c6a))
* ensure buildEnv overrides take effect over parent environment ([53b216c](https://github.com/d0ugal/graith/commit/53b216c236962c8c8afa00a35fa7f0b62c58effe)), closes [#265](https://github.com/d0ugal/graith/issues/265)
* error when --agent-hooks used with unsupported agent type ([f00b479](https://github.com/d0ugal/graith/commit/f00b479368918ad9fe2d667acb4ff0a7484be908)), closes [#274](https://github.com/d0ugal/graith/issues/274)
* fsync temp file before rename in writeFileAtomic ([79c7795](https://github.com/d0ugal/graith/commit/79c7795ba02b8a76db3f484290f4eab7ab0de47c))
* gate ChannelData on IsAttachedClient to reject input immediately ([e385e40](https://github.com/d0ugal/graith/commit/e385e4013243f58ab3efc2597d7b0d08b26bf6ae))
* grant agy sandbox access to ~/.gemini ([c998cf0](https://github.com/d0ugal/graith/commit/c998cf08cb56a0cf5f91dd79545288ccae6ee7cc))
* guard socket cleanup on confirmed daemon stop ([e99e423](https://github.com/d0ugal/graith/commit/e99e423b5f479026ff412e2c3fc63053adb0cef2))
* harden Resume path for shared worktree sessions ([9114132](https://github.com/d0ugal/graith/commit/911413209b555672e76f309f679beee43a05e2b1))
* keep overlay open after deleting a session ([9cb4d51](https://github.com/d0ugal/graith/commit/9cb4d514d6e8c66aa1b3a652454d72d25343c009))
* merge user agent configs with defaults instead of replacing ([2c7a77a](https://github.com/d0ugal/graith/commit/2c7a77a0a19788173daec2416dff441b3e9154a5)), closes [#256](https://github.com/d0ugal/graith/issues/256)
* move Resume shared-worktree guard before hook injection ([0bb3c63](https://github.com/d0ugal/graith/commit/0bb3c639ea15fd48790785264dd4b3a2decde38b))
* normalize sandbox paths before persisting to prevent cwd-dependent drift ([738498d](https://github.com/d0ugal/graith/commit/738498dbfc53c673cd92bb4f18ebc1d2fa28f389))
* parse mixed day+time durations like 7d12h in ParseDurationWithDays ([7f5af39](https://github.com/d0ugal/graith/commit/7f5af3960dfd6fba5016227112473b25a2c7f9f8)), closes [#280](https://github.com/d0ugal/graith/issues/280)
* persist AgentHooks on fork, clean up hook files on error ([cd7d20b](https://github.com/d0ugal/graith/commit/cd7d20b36870f0ed993a700ebb6c5df293778675))
* persist sandbox config at session creation to prevent resume/fork drift ([f574080](https://github.com/d0ugal/graith/commit/f574080a1ac3f5bf49df8c1f8eea23e72e0df089)), closes [#276](https://github.com/d0ugal/graith/issues/276)
* preserve stream high-water mark across message cleanup ([b24a0ee](https://github.com/d0ugal/graith/commit/b24a0eedbf4696a30a0fa37a71389084aa14b8bd)), closes [#275](https://github.com/d0ugal/graith/issues/275)
* prune orphaned acked_messages rows during cleanup ([e97b447](https://github.com/d0ugal/graith/commit/e97b44702f6fe172610228118532cacb343a7da7))
* reject --share-worktree when sandbox is disabled ([a4e6ffe](https://github.com/d0ugal/graith/commit/a4e6ffef8f8759326afa3f4a4aee349485867878)), closes [#245](https://github.com/d0ugal/graith/issues/245)
* reject duplicate session names in Create, Fork, and Rename ([7290180](https://github.com/d0ugal/graith/commit/729018084c812a699cefd8cf5019dcaa02702467)), closes [#273](https://github.com/d0ugal/graith/issues/273)
* reject fork of no-repo sessions ([5b2e943](https://github.com/d0ugal/graith/commit/5b2e94354fedda41fe3860b65f69571bf8c8d31e)), closes [#246](https://github.com/d0ugal/graith/issues/246)
* rename variable to avoid shadowing predeclared 'real' ([2530502](https://github.com/d0ugal/graith/commit/253050284ce9e1368d5cdb209a43f745ee7183e8))
* resolve symlinks in RepoPathAllowed and validate PIDs before signaling ([a2f4fc0](https://github.com/d0ugal/graith/commit/a2f4fc0bfab6f0492a3dcb7c5c2d738d2ece54da)), closes [#248](https://github.com/d0ugal/graith/issues/248)
* return error and roll back state when saveState fails after session creation ([ab2d1a5](https://github.com/d0ugal/graith/commit/ab2d1a566913584dfc929f693e3b3f73e6684597)), closes [#247](https://github.com/d0ugal/graith/issues/247)
* send SIGWINCH after type input to wake agent process ([4bedbcd](https://github.com/d0ugal/graith/commit/4bedbcdf15f2c4c5a6617e07a179c749f86be487)), closes [#309](https://github.com/d0ugal/graith/issues/309)
* show unsaved work warnings in overlay delete confirmation ([e35c0f8](https://github.com/d0ugal/graith/commit/e35c0f8908e41e8f54b0d5c76bc7ccc5f686e6d1))
* sort overlay session picker alphabetically by name ([399d15d](https://github.com/d0ugal/graith/commit/399d15d8f01389b847796c5f2ec7cdad77ff6f73)), closes [#310](https://github.com/d0ugal/graith/issues/310)
* stricter PID parsing, cleanup stale files, align client guard ([bd9d732](https://github.com/d0ugal/graith/commit/bd9d732e51e66eedd41f17b8b2c74bc585e08f00))
* sync parent directory after rename, wrap close error ([81183cc](https://github.com/d0ugal/graith/commit/81183cc005f6f3e104fe3b4800a97d1b87328c19))
* thread-filtered --ack no longer marks other threads as read ([5e2671e](https://github.com/d0ugal/graith/commit/5e2671ea5d994f4e6a6022a811bd4495af8ce2ca)), closes [#259](https://github.com/d0ugal/graith/issues/259)
* treat EPERM as alive in isPIDAlive to prevent duplicate daemons ([b6526e0](https://github.com/d0ugal/graith/commit/b6526e06a43d2b1f74103963ca73dad2c8b27b63)), closes [#250](https://github.com/d0ugal/graith/issues/250)
* update stale comments referencing old time-based sort order ([5445051](https://github.com/d0ugal/graith/commit/5445051bf82dcdaef83721b8361f04a833588fa1))
* use configured approval timeout for hook connection deadline ([3668999](https://github.com/d0ugal/graith/commit/36689997b511317bd709e0376966b569a8a97657)), closes [#244](https://github.com/d0ugal/graith/issues/244)
* use direct SIGWINCH signal instead of same-size Setsize in Poke ([e3a44ec](https://github.com/d0ugal/graith/commit/e3a44ec9216f1037d5a9557c6f03c070a379da18))
* use exact basename match in IsGraithDaemon, add symlink edge case tests ([ff1627b](https://github.com/d0ugal/graith/commit/ff1627b180b9edcb46f30547424e13bfa9ea0e09))
* use strict PID parsing in client's stopDaemonByPID ([297d2e7](https://github.com/d0ugal/graith/commit/297d2e7a1c241d71235bfe6af3227535341ffbb0))
* validate PID before signaling in StopDaemon ([50fbf32](https://github.com/d0ugal/graith/commit/50fbf32471c9788d025ff8a2d2daf2b0369e4402)), closes [#236](https://github.com/d0ugal/graith/issues/236)

## [0.16.4](https://github.com/d0ugal/graith/compare/v0.16.3...v0.16.4) (2026-06-10)


### Bug Fixes

* use clean restart for auto-upgrade, prefer PATH in resolveExecutable ([4d2e52c](https://github.com/d0ugal/graith/commit/4d2e52ca7379ef3c610bb8cdc634cb76691279e5))

## [0.16.3](https://github.com/d0ugal/graith/compare/v0.16.2...v0.16.3) (2026-06-10)


### Bug Fixes

* auto-restart daemon on version mismatch after upgrades ([ad1312f](https://github.com/d0ugal/graith/commit/ad1312f16ed5058bd6f466e441d5c28addc0a7ba))

## [0.16.2](https://github.com/d0ugal/graith/compare/v0.16.1...v0.16.2) (2026-06-10)


### Bug Fixes

* make TestSessionAttachDetach more robust against PTY timing ([5457255](https://github.com/d0ugal/graith/commit/5457255a8bd1b0ea34f933691a577c40a6721611))
* make TestSessionAttachDetach more robust against PTY timing ([97fdd5a](https://github.com/d0ugal/graith/commit/97fdd5a15fe93cc0896ca90d7791b2aa6c6d0381))

## [0.16.1](https://github.com/d0ugal/graith/compare/v0.16.0...v0.16.1) (2026-06-09)


### Bug Fixes

* only add hooks dir to sandbox read paths when agent hooks are enabled ([682682f](https://github.com/d0ugal/graith/commit/682682ff6d75da5afdeb457dab9e13e6e4bdb73e))

## [0.16.0](https://github.com/d0ugal/graith/compare/v0.15.0...v0.16.0) (2026-06-09)


### Features

* add batch delete/stop with --repo, --stopped, --stale filters ([26b1d4e](https://github.com/d0ugal/graith/commit/26b1d4e8be9bea95039e696d7269fe6a7172fdd2))

## [0.15.0](https://github.com/d0ugal/graith/compare/v0.14.0...v0.15.0) (2026-06-09)


### Features

* rename --approvals to --agent-hooks with all-or-nothing semantics ([811d1e7](https://github.com/d0ugal/graith/commit/811d1e7158f1fd7c6e1279dd14e436691d51a3ab))

## [0.14.0](https://github.com/d0ugal/graith/compare/v0.13.0...v0.14.0) (2026-06-09)


### Features

* add logging to approval request handling ([dc2a20a](https://github.com/d0ugal/graith/commit/dc2a20a6e120ec6289b7393dc263982cf379d3a3))
* improve approval overlay formatting ([575f925](https://github.com/d0ugal/graith/commit/575f9258056b6849139801531f63392998190350))
* improved approval overlay with detail panel ([3c827ce](https://github.com/d0ugal/graith/commit/3c827cebe01b3f4835101775a70f1921d99e291f))
* inject unread inbox messages on session start ([f775b3c](https://github.com/d0ugal/graith/commit/f775b3c818a83eec90ab9904d68386d411cb8341))
* make approval hooks opt-in per session with --approvals flag ([99792c8](https://github.com/d0ugal/graith/commit/99792c8c8617fefd5f41728b6097b6a33172cbc2))
* red status bar and approval status for pending approvals ([c290dd4](https://github.com/d0ugal/graith/commit/c290dd4ddd197e2f79702a22d6c5e42cdf5676c5))


### Bug Fixes

* handle Kitty keyboard protocol release events and encoded follow-up keys ([82207c6](https://github.com/d0ugal/graith/commit/82207c6581f3c6461b806f9b735c974dd8615fa6))
* remove TODO comment that triggers godox lint ([31a82ba](https://github.com/d0ugal/graith/commit/31a82ba0ac569f8e04596a968b80546e5be882cb))
* replace naked returns in parseKittyCSIu to satisfy nakedret lint ([20c5ef8](https://github.com/d0ugal/graith/commit/20c5ef8fd2f37c54641bb833ed5dbd60e053cd81))

## [0.13.0](https://github.com/d0ugal/graith/compare/v0.12.5...v0.13.0) (2026-06-09)


### Features

* add --share-worktree flag for read-only worktree sharing ([964c569](https://github.com/d0ugal/graith/commit/964c56908e2999bc30e82b9a4762d6993bf74c53)), closes [#183](https://github.com/d0ugal/graith/issues/183)
* add approval overlay UI and passthrough integration ([3084ce7](https://github.com/d0ugal/graith/commit/3084ce791d5d0ad124914f7d2eaa5a423016397d))
* add cross-session approval system protocol, config, and daemon ([3dc1f64](https://github.com/d0ugal/graith/commit/3dc1f645234bb35c2c83c9690e220c368d906ff7))
* add gr approve-request CLI and wire hooks ([3efd8e9](https://github.com/d0ugal/graith/commit/3efd8e94b9c03286632605e6fa7ecbdd3b2122ee))


### Bug Fixes

* resolve stale binary path during daemon upgrade ([a28263d](https://github.com/d0ugal/graith/commit/a28263d8c849a0eaea68eb882a4ab1d94e06d31a))
* rewrite if-else chains to switch for gocritic lint ([fe7e5b8](https://github.com/d0ugal/graith/commit/fe7e5b8ddb99afe7565e84add93ee15c7b46a5c7))

## [0.12.5](https://github.com/d0ugal/graith/compare/v0.12.4...v0.12.5) (2026-06-09)


### Bug Fixes

* exclude _system.* streams from unread count and topic listing ([f515a92](https://github.com/d0ugal/graith/commit/f515a92ea6f59ea320ba46495f55d5c083df3503))
* scope status bar unread count to session inbox only ([1961a68](https://github.com/d0ugal/graith/commit/1961a68046c4791df6c957f9e6e75fdacedb7925))

## [0.12.4](https://github.com/d0ugal/graith/compare/v0.12.3...v0.12.4) (2026-06-09)


### Bug Fixes

* restore n/p as next/prev session, use c for create ([0c43365](https://github.com/d0ugal/graith/commit/0c4336568de0798f31e9499178add6870c820cb5))

## [0.12.3](https://github.com/d0ugal/graith/compare/v0.12.2...v0.12.3) (2026-06-09)


### Bug Fixes

* include config dir in sandbox read paths for hook scripts ([1e92c0f](https://github.com/d0ugal/graith/commit/1e92c0fd3727fd8324cc743251b16d29543462d0))

## [0.12.2](https://github.com/d0ugal/graith/compare/v0.12.1...v0.12.2) (2026-06-09)


### Bug Fixes

* include gr binary and socket paths in sandbox for hooks ([f946dfc](https://github.com/d0ugal/graith/commit/f946dfcaa7c52b0a83b121176f40b7ddd47c4839))
* simplify hooks — call gr directly, drop shell script wrapper ([c4f17be](https://github.com/d0ugal/graith/commit/c4f17be74486adaa7ba4ea53874380a98bcbf40e))
* use correct Claude Code hooks settings schema (matcher+hooks) ([07c1ada](https://github.com/d0ugal/graith/commit/07c1adaf7797464c8e71845dff0c412a68fd3939))

## [0.12.1](https://github.com/d0ugal/graith/compare/v0.12.0...v0.12.1) (2026-06-09)


### Bug Fixes

* auto-include agent config dirs in sandbox read/write paths ([543a47b](https://github.com/d0ugal/graith/commit/543a47b56a8b76b5af1872a2a2a290050bcd2bb8))
* use daemon restart instead of reload in homebrew post_install ([bf7e5c8](https://github.com/d0ugal/graith/commit/bf7e5c8301d17808b55770cfb9d1203556572660))

## [0.12.0](https://github.com/d0ugal/graith/compare/v0.11.0...v0.12.0) (2026-06-09)


### Features

* add back-and-forth session switching (ctrl+b l) ([f67a226](https://github.com/d0ugal/graith/commit/f67a2269cdc01966f63ddc9930a60c4d82e14634)), closes [#164](https://github.com/d0ugal/graith/issues/164)
* add gr restart command and overlay restart action ([e141ec0](https://github.com/d0ugal/graith/commit/e141ec0be86cf70a2e9302d1ab18da7245bc55ed)), closes [#155](https://github.com/d0ugal/graith/issues/155)


### Bug Fixes

* use ~/.config for config path instead of macOS Application Support ([3b049c0](https://github.com/d0ugal/graith/commit/3b049c0df82765a154362439f3279b5f6a4ecd5f))
* use tuple swap for prevSessionID (gocritic valSwap) ([7e9c2ec](https://github.com/d0ugal/graith/commit/7e9c2ecca328711f7cd556efad26609260530d6a))

## [0.11.0](https://github.com/d0ugal/graith/compare/v0.10.0...v0.11.0) (2026-06-08)


### Features

* add allowed_repo_paths config to restrict session creation ([0c93144](https://github.com/d0ugal/graith/commit/0c93144918c75c18f54c7628725a0d3dd9e923b0))
* add Codex lifecycle hook injection ([a1d0574](https://github.com/d0ugal/graith/commit/a1d0574a947fe01545f63c4f65f61ef7e24f6915))
* add enrichment types to protocol ([10f874a](https://github.com/d0ugal/graith/commit/10f874aedc4fd7f35fddc6f26e47e042bc655b44))
* add hook report ingestion and gr report-status command ([25fd792](https://github.com/d0ugal/graith/commit/25fd792f51bef2c165419c7a178283ddb05fc3c7))
* add safehouse checks to gr doctor ([08f1301](https://github.com/d0ugal/graith/commit/08f13010170fa81eed9022575e8ec94f84badf4c))
* add sandbox fields to state, protocol, and CLI ([6af6e99](https://github.com/d0ugal/graith/commit/6af6e99096cc260d69f8aeb18115e3ce687b2caf))
* add sandbox package for safehouse command wrapping ([df46f8c](https://github.com/d0ugal/graith/commit/df46f8cd8dbeed51423929f1831266293c65171c))
* add SandboxConfig to config schema with merge semantics ([ed9a47b](https://github.com/d0ugal/graith/commit/ed9a47b0aac3996611ecb5b32bd893a2ce2df082))
* add StatusReportMsg to wire protocol ([3798290](https://github.com/d0ugal/graith/commit/3798290de35aeb1af1d9ad02613719810fa75818))
* Claude hook injection and authority layer ([5955406](https://github.com/d0ugal/graith/commit/59554068dbed35d78ac2bd49da06fcdb31a0a628))
* enrichment data pipeline — cost, tokens, model, tool in UI ([a5bc322](https://github.com/d0ugal/graith/commit/a5bc322272f4828718c1996023a483034a775fe6))
* wire safehouse sandbox into Create, Resume, and Fork ([531aea4](https://github.com/d0ugal/graith/commit/531aea45b76c65ff7d1697033be4a9c3619880f0))


### Bug Fixes

* address 5 review findings from Codex ([1c90101](https://github.com/d0ugal/graith/commit/1c9010155b9c1188ec2c43ecc139aca51507b99f))
* clean up legacy daemon on startup after socket path change ([b97aedd](https://github.com/d0ugal/graith/commit/b97aedd2eaac442aa2c4e66d49c3796b8cac0911))
* expand ~ and relative paths in sandbox read/write dirs ([cfcf547](https://github.com/d0ugal/graith/commit/cfcf547bfa52440a32bbcc679da426c221008039))
* fail closed when sandbox is enabled but safehouse unavailable ([4a59264](https://github.com/d0ugal/graith/commit/4a59264c49beda42dee6a31c2f7159b71a45da47))
* honor per-agent sandbox enablement and custom command paths ([23cb5c4](https://github.com/d0ugal/graith/commit/23cb5c4364b86755b8af2ea07645fd755b171f9d))
* lint — gofmt alignment and switch over if/else chains ([e8f4c64](https://github.com/d0ugal/graith/commit/e8f4c64f7bebd3c3b45e8dc9e7579e8fcae89978))
* make sandbox config-only, remove CLI override flags ([be08310](https://github.com/d0ugal/graith/commit/be08310c0bfc8b751124a83f7d9799c7e514b966))
* move daemon socket fallback out of /tmp ([b71c4e6](https://github.com/d0ugal/graith/commit/b71c4e621c3237ac7e866013d013bea7aa738cda))
* update module golang.org/x/term to v0.44.0 ([3e19e7d](https://github.com/d0ugal/graith/commit/3e19e7d4ebf53ff43860fcf87de6eda1b3f0f48f))

## [0.10.0](https://github.com/d0ugal/graith/compare/v0.9.0...v0.10.0) (2026-06-08)


### Features

* fork sessions with agent conversation history ([c123ca2](https://github.com/d0ugal/graith/commit/c123ca29fe40545727032747cd334597dd1aa0ed))


### Bug Fixes

* send type input and newline as a single PTY write ([42d0172](https://github.com/d0ugal/graith/commit/42d017209f5918732caea74c6e5338c7a91a76ca)), closes [#151](https://github.com/d0ugal/graith/issues/151)

## [0.9.0](https://github.com/d0ugal/graith/compare/v0.8.0...v0.9.0) (2026-06-08)


### Features

* add ctrl+b n (new) and ctrl+b f (fork) keybindings ([a4bd9ec](https://github.com/d0ugal/graith/commit/a4bd9eccb8a39333c1f02e416d8f1daadc6765cf))

## [0.8.0](https://github.com/d0ugal/graith/compare/v0.7.0...v0.8.0) (2026-06-08)


### Features

* simplify daemon subcommands and auto-reload on brew upgrade ([91a586a](https://github.com/d0ugal/graith/commit/91a586a8281221d3f5c852ab7dcaa588ac198484))

## [0.7.0](https://github.com/d0ugal/graith/compare/v0.6.1...v0.7.0) (2026-06-08)


### Features

* redesign status bar with colors and fleet summary ([3a7e3cf](https://github.com/d0ugal/graith/commit/3a7e3cf60ee4d0e966158497c99975e9f24a5e41))


### Bug Fixes

* update module golang.org/x/sync to v0.21.0 ([ad408f3](https://github.com/d0ugal/graith/commit/ad408f327b85721875a466fbb09eabb24d6e5c61))
* update module golang.org/x/sys to v0.46.0 ([09e327f](https://github.com/d0ugal/graith/commit/09e327fdb93035d4d734f16508c2eeabfa414e36))

## [0.6.1](https://github.com/d0ugal/graith/compare/v0.6.0...v0.6.1) (2026-06-08)


### Bug Fixes

* reduce unknown agent status after daemon restart ([9aac03e](https://github.com/d0ugal/graith/commit/9aac03e36ec9fdbd49b9e53b2e910853978869dc))
* stop boosting current session to top of sort order ([e03ba68](https://github.com/d0ugal/graith/commit/e03ba680138244bf83e5da303b559bb53846b083))
* use byte-bounded scrollback replay and event-based grace period ([009d39d](https://github.com/d0ugal/graith/commit/009d39d7204b761d677e335428ff0501f2536763))

## [0.6.0](https://github.com/d0ugal/graith/compare/v0.5.1...v0.6.0) (2026-06-08)


### Features

* color-code session status in overlay ([f7d5a2c](https://github.com/d0ugal/graith/commit/f7d5a2c7e856561966e9c65441301ceb005be3ed))

## [0.5.1](https://github.com/d0ugal/graith/compare/v0.5.0...v0.5.1) (2026-06-08)


### Bug Fixes

* reset filter cursor and align next/prev session order with overlay ([62a382f](https://github.com/d0ugal/graith/commit/62a382f2921da37ebea9b1c0a5cc322f46066782))

## [0.5.0](https://github.com/d0ugal/graith/compare/v0.4.0...v0.5.0) (2026-06-08)


### Features

* redesign session switcher overlay ([82f3504](https://github.com/d0ugal/graith/commit/82f3504c475cc22b0f8ab88de42c8863ff4127aa)), closes [#80](https://github.com/d0ugal/graith/issues/80)


### Bug Fixes

* add graith binary to .gitignore ([2c233e9](https://github.com/d0ugal/graith/commit/2c233e99ca89489be2999576ef448c36f4a45d2f))

## [0.4.0](https://github.com/d0ugal/graith/compare/v0.3.1...v0.4.0) (2026-06-08)


### Features

* include repo name in worktree directory path ([2f3f1bf](https://github.com/d0ugal/graith/commit/2f3f1bf410271bce71d863cac48e88db3c5071f0))


### Bug Fixes

* update github.com/charmbracelet/ultraviolet digest to 35bcb73 ([aaf9c82](https://github.com/d0ugal/graith/commit/aaf9c8246ffe24645676df306a5af2de9b2178cb))
* update module modernc.org/libc to v1.73.0 ([9b990f6](https://github.com/d0ugal/graith/commit/9b990f61126564acdeeb3a07946ee76cad611880))

## [0.3.1](https://github.com/d0ugal/graith/compare/v0.3.0...v0.3.1) (2026-06-07)


### Bug Fixes

* delete dev tag before goreleaser snapshot ([246f631](https://github.com/d0ugal/graith/commit/246f6312920bc1403e19ea8ab27f7d487a1fae7d))

## [0.3.0](https://github.com/d0ugal/graith/compare/v0.2.1...v0.3.0) (2026-06-07)


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

## [0.2.1](https://github.com/d0ugal/graith/compare/v0.2.0...v0.2.1) (2026-06-07)


### Bug Fixes

* resolve golangci-lint warnings in render and handler ([850f706](https://github.com/d0ugal/graith/commit/850f706125f53fac75786ed015ff0c31993b042b))
* split goreleaser into separate tag-triggered workflow ([a1719aa](https://github.com/d0ugal/graith/commit/a1719aacbbcb6a25675693a4f63627df9c2afc94))

## [0.2.0](https://github.com/d0ugal/graith/compare/v0.1.1...v0.2.0) (2026-06-06)


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

## [0.1.1](https://github.com/d0ugal/graith/compare/v0.1.0...v0.1.1) (2026-06-06)


### Bug Fixes

* fetch tags after release-please before goreleaser ([b3406e4](https://github.com/d0ugal/graith/commit/b3406e4bbd3f65a5fdfc3cb9d57324c0fb1721ed))

## [0.1.0](https://github.com/d0ugal/graith/compare/v0.0.1...v0.1.0) (2026-06-06)


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
