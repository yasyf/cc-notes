---
id: a1b2c3d4e5f6a7b8c9d0e1f2a3b4c5d6e7f8a9b0
title: Deploy the service
status: active
labels: [deploy, ops]
created: "2025-12-12T02:54:56Z"
updated: "2025-12-13T02:54:56Z"
---
Roll a new build to production.

## Steps

<!-- cc-notes:step 1111111 -->
1. Pull the latest image

```sh
docker pull myapp:latest
```

<!-- cc-notes:step 2222222 -->
2. Restart the service

## Runs

- 3333333 succeeded — Agent A <a@example.com>, 2025-12-12T02:54:56Z → 2025-12-13T02:54:56Z, 1 done / 1 skipped / 0 failed (task ffff000)
- 4444444 running — Agent B <b@example.com>, 2025-12-14T02:54:56Z → in progress, 0 done / 0 skipped / 0 failed
