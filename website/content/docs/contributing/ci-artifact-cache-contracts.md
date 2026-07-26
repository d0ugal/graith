---
weight: 37
title: CI artifact contracts
description: Verify CI artifacts against policy, plan, producer-result, and provenance identity
icon: package-check
toc: true
---

# CI artifact contracts

The P5 CI north-star contract lives in `internal/cipolicy`. It defines local,
versioned manifests for proof artifacts. The manifests are canonical JSON with
digest self-checks, and they bind to the P1 policy identity plus the run-plan
and artifact producer-result identities.
Manifest readers use strict JSON decoding, so unknown fields, duplicate object
keys, and trailing JSON values are rejected instead of being ignored.

This is a local contract library. Repository CI validates the library through
native and release artifact tests, but no current workflow consumes these
manifests as a production artifact gate. Real artifact publication and
publication credentials remain with the existing release and docs workflows.

## Artifact manifests

An artifact manifest records:

- policy, plan, producer result, source commit/tree, event, and trust tier;
- mode, coordinate, platform, architecture, cost class, and matrix identity;
- dependency and toolchain digests plus explicit build flags;
- the exact archive member list, mode bits, link targets, sizes, and per-file
  SHA-256 digests;
- producer workflow path, workflow file digest, run ID, run attempt, job
  identity, upload completeness, artifact ID, and artifact digest. The workflow
  path, workflow digest, and job identity must match the P1 trace for the
  producer mode and coordinate.

Consumers verify the manifest and producer provenance before extraction. The
archive must match its digest and contain exactly the manifest members in
canonical order. Extra, missing, duplicate, absolute, traversal, directory,
hardlink, unsupported entry type, and escaping symlink members are rejected.
PAX extended metadata, extended attributes, and GNU long-name, long-link, and
sparse extension records are rejected. GNU-style all-zero tar record padding
after the end marker is accepted up to a bounded cap; non-zero trailing data
and excessive padding are rejected. A file path cannot also be a prefix of any
member path, and case-folded path or directory-prefix collisions are rejected
before extraction. Symlinks must resolve to a declared regular file in the same
manifest. Mode bits must be non-zero, stay within `0o777`, and avoid
setuid/setgid/sticky bits. Mode-bit, line-ending, and content changes are
rejected through per-member checks.

Use `ExtractVerifiedArtifact` only after binding the artifact to the expected
policy, producer plan, producer result, producer mode and coordinate, artifact
type, artifact ID, artifact digest, consumer plan, consumer job, workflow, run
ID, and run attempt. The consumer plan binds the read event and trust tier: an artifact
from one event or trust tier cannot satisfy a different consumer event or tier.
It verifies the complete archive and destination paths before any filesystem
write, rejects unsafe destination symlinks below the requested destination
boundary, requires an existing destination directory to be empty, and fails
without writing partial members when a preflight target already exists.
