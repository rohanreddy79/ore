# ADR 0010: The learning engine is built on the pipeline primitive

**Status:** Proposed · **Date:** 2026-10-18

## Context
GitHub Skills (Actions-driven) survived; Learning Lab (bespoke bot) was killed; free anonymous sandboxes died of abuse; auto-graded courses out-complete peer-graded ones; CS50's guardrailed, human-endorsed tutor is the best-documented AI tutoring design (research/04).

## Decision
A lesson is a template repository plus a pipeline that asserts the learner's state and advances the lesson; it runs on the learner's own repository or an ephemeral fork, auto-graded, four steps or fewer, inside quota. Sandboxes are per-user, authenticated, time-boxed and egress-restricted. The tutor guides rather than answers and its Q&A replies are provisional until a human endorses them. Credentials are Open Badges 3.0. Lessons ship in ten languages first and are bundled into the self-hosted binary.

## Consequences
The pipeline IR gains lesson-specific task types (assert, advance, hint); lesson runs are ledger entities; the curriculum lives in `lessons/` under CC BY-SA 4.0 and is translated through Weblate.
