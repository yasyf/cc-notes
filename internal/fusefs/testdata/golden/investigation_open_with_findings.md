---
id: "1111111111111111111111111111111111111111"
status: open
labels: [ci, concurrency]
paths: [internal/pool]
branches: [main]
findings:
  - id: aaaa1111aaaa1111aaaa1111aaaa1111
    text: The pool rewrite introduced the hang
    status: open
  - id: bbbb2222bbbb2222bbbb2222bbbb2222
    text: The hang predates the rewrite
    status: cleared
    why: The bisect reproduces four commits earlier
follow_ups: ["9999999999999999999999999999999999999999"]
---
# CI worker deadlock

Workers hang after the pool rewrite.

## Timeline

### 2025-12-12T02:54:56Z — Agent A <a@example.com>

Captured the first blocked worker stack.

- [goroutines.txt](../attachments/1111111/goroutines.txt)

## Verdict

### Root cause

_Not established._

### Resolution

_Not recorded._

### Fix commits

_None._
