---
id: "2222222222222222222222222222222222222222"
status: root_caused
labels: [concurrency]
findings:
  - id: cccc3333cccc3333cccc3333cccc3333
    text: A sender remains blocked after cancellation
    status: confirmed
    why: The goroutine dump shows the blocked send
---
# Canceled collection leaks a sender

Worker shutdown sometimes leaves the suite hung.

## Timeline

### 2025-12-12T02:54:56Z — Agent A <a@example.com>

Reproduced with a canceled context.

### 2025-12-13T02:54:56Z — Agent B <b@example.com>

The sender outlives the collector return.

## Verdict

### Root cause

An unbuffered result send survives the canceled collector.

### Resolution

_Not recorded._

### Fix commits

_None._
