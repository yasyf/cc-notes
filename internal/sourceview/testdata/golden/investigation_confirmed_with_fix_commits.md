---
id: "3333333333333333333333333333333333333333"
status: confirmed
labels: [ci]
findings:
  - id: dddd4444dddd4444dddd4444dddd4444
    text: The send blocks after collector cancellation
    status: confirmed
    why: A focused race test reproduces the blocked sender
---
# Buffered results prevent shutdown deadlock

The collector can return while a worker is sending.

## Timeline

### 2025-12-12T02:54:56Z — Agent A <a@example.com>

Added a focused reproducer.

### 2025-12-14T02:54:56Z — Agent V <v@example.com>

Twenty race runs completed without recurrence.

## Verdict

### Root cause

The unbuffered result channel leaves a worker blocked on send.

### Resolution

Buffer the result channel and wait for workers before returning.

### Fix commits

- 3333aaaa3333aaaa3333aaaa3333aaaa3333aaaa
- 4444bbbb4444bbbb4444bbbb4444bbbb4444bbbb
