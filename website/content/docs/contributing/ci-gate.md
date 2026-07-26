---
weight: 38
title: CI gate evaluator
description: Validate graith-ci-gate evidence bundles
icon: shield
toc: true
---

# CI gate evaluator

`graith-ci-gate` is the P4 repository-side contract for the future GitHub App
gate. It consumes the [CI policy manifest]({{< relref "/docs/contributing/ci-policy.md" >}}),
a digest-bound run plan, result records, trusted GitHub run metadata, and
artifact provenance, then emits the synthetic `graith-ci-gate` check payload.
The command is offline: it never installs an App, changes rulesets, enables a
merge queue, calls GitHub, or publishes a check.

Current required checks remain authoritative until the live App fixture and the
later dual-run evidence pass.

## Evaluate a bundle

Run the offline evaluator from the repository root:

```bash
go run ./cmd/cigate \
  -input /tmp/graith-ci-gate-bundle.json \
  -trusted-policy /trusted-base/internal/cipolicy/manifest.json \
  -policy-digest <trusted-policy-sha256> \
  -trusted-config /secure/graith-ci-gate/config.json \
  -config-digest <trusted-config-sha256> \
  -evaluator-version vX.Y.Z \
  -release-digest <reviewed-release-sha256> \
  -evaluator-digest <reviewed-source-sha256> \
  -replay-store /tmp/graith-ci-gate-replay.json \
  evaluate
```

The evaluator-owned trust anchors are separate from the bundle:

- `-trusted-policy`: the P1 `cipolicy.Manifest` read from evaluator-owned
  trusted-base storage, not from a PR checkout;
- `-policy-digest`: the expected trusted policy digest. A swapped or
  self-consistent forged manifest fails closed before evidence validation;
- `-trusted-config`: the GitHub App, hosted runtime, release digest,
  attestation key, live-proof fixture repository, rotation, retention,
  revocation, and operator ownership contract loaded from evaluator-owned
  storage;
- `-config-digest`: the expected raw digest of that trusted config file. A
  swapped App/runtime/key/owner config fails closed before validation; and
- `-evaluator-version`, `-release-digest`, and `-evaluator-digest`: the
  deployed evaluator identity and reviewed digest-pinned release/source
  anchors.

The input bundle is strict JSON. It must include:

- `delivery`: the verified webhook delivery id, event name, signature status,
  and body digest;
- `event`: trusted GitHub event metadata, intended SHA, head SHA, base SHA,
  repository identity, trust tier, and policy digest. This context must be
  derived only from the signed webhook body named by `delivery.body_digest`;
- `plan`: the P2 `cipolicy.RunPlan` for the same policy digest and trusted
  event identity;
- `results`: one P2 `cipolicy.ResultRecord` for each required plan
  coordinate; and
- `evidence`: trusted run and artifact provenance for each result coordinate.

The bundle may echo `config`, `policy`, or `evaluator` for retention, but those
fields are not trust roots. If present, they must match the evaluator-owned
anchors exactly, and policy echoes must validate against their own digest.

`-replay-store` is required for `evaluate`; omitting it is a fail-closed
configuration error. The file-backed replay store is for the offline CLI and
must be retained with evaluator state. Deleting it resets local replay memory;
corruption fails closed.

Bundle JSON cannot supply trusted validation time. The optional CLI
`-test-clock` flag is only enabled when `GRAITH_CIGATE_ALLOW_TEST_CLOCK=1` is
set for hermetic tests; hosted deployments must use the runtime clock. The
deprecated `-now` alias has the same gate and remains for older local fixtures.

On success, the command prints a completed `graith-ci-gate` check with
`conclusion: success` plus retained evidence identity. On `evaluate` rejection,
including strict input decode and trusted config or policy anchor-load
rejection, it still prints a completed failed check payload when possible and
exits non-zero.

## What local evaluation proves

The offline evaluator fails closed for:

- untrusted event source or unverified webhook signature;
- missing, duplicate, or replayed delivery ids;
- replayed evidence bundles, even with a fresh delivery id;
- bundle config, policy, or evaluator echoes that do not match the trusted
  anchors;
- wrong intended, head, or base SHA;
- stale policy epoch or plan digest mismatch;
- wrong workflow path or workflow blob SHA for a coordinate;
- wrong producer run id or run attempt;
- missing, duplicate, extra, partial, zero-job, or non-green results;
- artifact digest or artifact provenance mismatch;
- future or plan-expired run/artifact provenance timestamps;
- fork, same-repository-agent, and trusted-base trust-tier confusion; and
- PR-controlled unconditional success without trusted provenance.

These checks build on the existing P2 plan/result/fan-in contracts rather than
forking their schemas.

## Required App contract

The App contract must name `graith-ci-gate`, bind the check name to
`graith-ci-gate`, and use only these repository permissions:

- `metadata: read`
- `contents: read`
- `actions: read`
- `pull_requests: read`
- `checks: write`
- `statuses: write` only if commit statuses are actually used

The App events are `pull_request` and `merge_group`. `merge_group` is validated
as a live GitHub event, while the current P2 policy schema represents its
required jobs through the existing `pull_request`/`trusted-base` event identity
rather than adding a new `cipolicy` event.

## External decision gate

Repository code has no default hosted runtime or signing backend. A gate
configuration with a missing, local, pending, or placeholder runtime or
attestation key service is invalid. Owners must explicitly provide:

- the hosted runtime and digest-pinned release mechanism;
- the attestation/signing key service, key id, and trust model;
- rotation, retention, incident-revocation, and operator ownership;
- the GitHub App owner/install target; and
- the disposable live fixture repository and any account, cost, or credential
  inputs.

Do not install the App, create a hosted service, enable merge queue, edit
rulesets or branch protection, or publish a required check without explicit
authorization for that external state.

## Validate live proof

The retained live-proof schema is validated separately:

```bash
go run ./cmd/cigate \
  -input /tmp/graith-ci-gate-live-proof.json \
  -trusted-config /secure/graith-ci-gate/config.json \
  -config-digest <trusted-config-sha256> \
  live-proof
```

Local emulation cannot satisfy the live-proof claim. The validator only accepts
a bundle declared as `source: github-live-fixture`; operators must retain the
GitHub evidence behind that declaration. The proof must also match the
evaluator-owned App/runtime/key id/ownership config and the configured
disposable fixture repository. A valid bundle must bind the required check to
the installed App id, show no bypass actors, show merge queue enabled, retain
evidence URIs, and include all required cases:

- App source restriction;
- fork permissions;
- same-repository-agent permissions;
- `merge_group` triggering;
- check freshness;
- PR YAML rewrite;
- replay;
- stale head SHA;
- missing evidence;
- zero-job failure; and
- artifact/run provenance.

P4 remains incomplete until those live cases pass with the externally approved
runtime, key service, App installation, fixture repository, and retained
evidence. P10 cutover remains blocked until that evidence is retained in the
dual-run bundle.
