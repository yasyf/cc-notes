---
id: "9999999999999999999999999999999999999999"
title: Replace the pool with an errgroup
status: abandoned
labels: []
created: "2025-12-12T02:54:56Z"
updated: "2025-12-15T02:54:56Z"
---
# Replace the pool with an errgroup

## Approach

Swap the hand-rolled pool for errgroup.

## Outcome

errgroup cannot express the per-step lease.
