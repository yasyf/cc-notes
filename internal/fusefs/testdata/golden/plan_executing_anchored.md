---
id: "7777777777777777777777777777777777777777"
title: Rewrite the worker pool
status: executing
labels: [concurrency, pool]
commits: [7777aaaa7777aaaa7777aaaa7777aaaa7777aaaa]
dirs: [internal/pool]
branches: [main]
created: "2025-12-12T02:54:56Z"
updated: "2025-12-13T02:54:56Z"
---
# Rewrite the worker pool

## Context

Shutdown hangs under cancellation.

## Verification

go test -race ./internal/pool/

## Outcome

_Not recorded._
