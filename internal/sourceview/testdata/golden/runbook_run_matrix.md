---
id: d4e5f6a7b8c9d0e1f2a3b4c5d6e7f8a9b0c1d2e3
title: Run status matrix
status: active
labels: [ci]
created: "2025-12-12T02:54:56Z"
updated: "2025-12-13T02:54:56Z"
---
Exercises every run and step-result status.

## Steps

<!-- cc-notes:step aaaaaaa -->
1. Build

```sh
go build ./...
```

<!-- cc-notes:step bbbbbbb -->
2. Deploy

## Runs

- run1suc succeeded — Agent A <a@example.com>, 2025-12-12T02:54:56Z → 2025-12-13T02:54:56Z, 1 done / 1 skipped / 0 failed (task ffff000)
- run2fai failed — Agent B <b@example.com>, 2025-12-12T02:54:56Z → 2025-12-13T02:54:56Z, 0 done / 0 skipped / 1 failed
- run3run running — Agent C <c@example.com>, 2025-12-14T02:54:56Z → in progress, 0 done / 0 skipped / 0 failed
- run4aba abandoned — Agent D <d@example.com>, 2025-12-12T02:54:56Z → 2025-12-13T02:54:56Z, 0 done / 0 skipped / 0 failed
