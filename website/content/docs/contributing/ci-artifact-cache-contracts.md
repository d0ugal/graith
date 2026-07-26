---
weight: 37
title: CI artifact and cache contracts
description: Verify CI artifacts and caches against policy, plan, result, and provenance identity
icon: package-check
toc: true
---

# CI artifact and cache contracts

The P5 CI north-star contract lives in `internal/cipolicy`. It defines local,
versioned manifests for proof artifacts and cache entries. The manifests are
canonical JSON with digest self-checks, and they bind to the P1 policy identity
plus the P2 run-plan and result identities.
Manifest readers use strict JSON decoding, so unknown fields, duplicate object
keys, and trailing JSON values are rejected instead of being ignored.

This is a local contract library and fixture suite. Production workflow wiring,
publication credentials, live signing, and the GitHub App evaluator are owned
by later migration packages.

## Artifact manifests

An artifact manifest records:

- policy, plan, result, source commit/tree, event, and trust tier;
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
policy, producer plan, result, producer mode and coordinate, artifact type,
artifact ID, artifact digest, consumer plan, consumer job, workflow, run ID, and
run attempt. The consumer plan binds the read event and trust tier: an artifact
from one event or trust tier cannot satisfy a different consumer event or tier.
It verifies the complete archive and destination paths before any filesystem
write, rejects unsafe destination symlinks below the requested destination
boundary, requires an existing destination directory to be empty, and fails
without writing partial members when a preflight target already exists.

## Cache manifests

A cache manifest records the same source, policy, plan, result, platform,
dependency, toolchain, build-flag, producer, archive format, exact file list,
and checksum identity. The cache key is derived from source, dependency,
toolchain, platform, coordinate, and build-flag identity. The archive format and
file list are not part of the key; they are an independent payload contract that
must match before a restore is allowed. The producer must record the cache key
and digest observed by the cache backend; the constructor does not backfill
missing provenance.

Trust tier is not a cache-key component. It is a separate authorization
boundary: a trusted read accepts only a cache written at the same trust tier.
Same-repository agent and fork outputs remain untrusted even when their cache
key material matches a trusted job.

`ValidateCacheWrite` verifies the producer result, manifest digest, cache key,
payload checksum, exact archive members, producer status, completed upload, and
P1 producer trace. `ValidateCacheRead` then verifies the consumer plan/job
identity, event identity, expected key material, payload checksum, exact archive
members, and trust tier before allowing a restore.
