---
weight: 1420
title: "Agent-to-agent communication"
description: "Publish/subscribe, request/reply, and hierarchical coordination."
icon: "forum"
toc: true
draft: false
---

Patterns for coordinating running agents through graith's messaging primitives.

## Publish/subscribe broadcast

One agent publishes findings; multiple agents react:

```bash
# Scanner agent
gr msg pub --topic vulnerabilities "SQL injection in user.go:89"

# Fixer agents (each subscribing)
gr msg sub --topic vulnerabilities --follow --ack
```

## Request/reply

Structured request with a designated reply channel:

```bash
# Requester
gr msg send worker-1 "analyze auth.go for race conditions" --reply-to analysis-results
gr msg sub --topic analysis-results --wait

# Worker
gr msg inbox --all --ack
# ... does analysis ...
gr msg pub --topic analysis-results "No race conditions found. Thread-safe."
```

## Hierarchical coordination

Parent orchestrates children:

```bash
# Parent creates workers and sends tasks
gr new worker-1 --repo ~/Code/api --background
gr new worker-2 --repo ~/Code/api --background
gr msg send worker-1 "fix the auth tests"
gr msg send worker-2 "fix the API tests"

# Workers report back
gr msg send --parent "auth tests fixed, all passing"

# Parent reads results
gr msg inbox --all --ack
```
