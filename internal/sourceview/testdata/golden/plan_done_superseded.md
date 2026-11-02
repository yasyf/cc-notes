---
id: "8888888888888888888888888888888888888888"
title: First cut at the shutdown fix
status: done
labels: [concurrency]
paths: [internal/pool/pool.go]
superseded_by: ["7777777777777777777777777777777777777777"]
created: "2025-12-12T02:54:56Z"
updated: "2025-12-15T02:54:56Z"
---
# First cut at the shutdown fix

## Approach

Drain the channel on cancellation.

## Outcome

Landed, but the drain races the second shutdown path.
