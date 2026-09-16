---
id: aaaa0000aaaa0000aaaa0000aaaa0000aaaa0000
title: Which cache backend should the session store use?
tags: ['header:Cache', 'scope:durable']
paths: [internal/session/store.go]
branches: [feature/sessions]
author: Agent A <a@example.com>
created: "2025-12-12T02:54:56Z"
updated: "2025-12-14T02:54:56Z"
verified_at: "2025-12-14T02:54:56Z"
verified_by: Agent A <a@example.com>
verified_commit: aaaa1111aaaa1111aaaa1111aaaa1111aaaa1111
witness:
  - kind: path
    value: internal/session/store.go
    oid: 1234567890abcdef1234567890abcdef12345678
---
Redis
Options: Redis | Memcached | In-process LRU
Notes: we already run Redis for queues