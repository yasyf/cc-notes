---
id: "6666666666666666666666666666666666666666"
title: Buffer the result channel
status: draft
labels: [concurrency]
created: "2025-12-12T02:54:56Z"
updated: "2025-12-12T02:54:56Z"
---
# Buffer the result channel

## Context

The collector returns while a worker is still sending.

## Approach

1. Buffer the result channel.
2. Wait for every worker before returning.

## Outcome

_Not recorded._
