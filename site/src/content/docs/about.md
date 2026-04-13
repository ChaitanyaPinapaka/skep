---
title: Why "skep"?
description: The metaphor behind the name, and why this tool is built the way it is.
---

A **skep** is a dome of woven straw — the oldest design for a beehive.
A colony of bees lives inside. Each bee works alone: some build comb,
some tend the brood, some fly miles out to collect nectar. They share
one structure and one purpose, but no bee runs the hive. The hive
emerges from the bees.

This tool is a skep for your codebase. Each repo is a worker with its
own routine and its own memory. When a task spans repos, the workers
coordinate through the hive — not by being told what to do, but by
leaving signals the others can sense.

:::note[A note on the metaphor]
Traditional straw skeps had a dark side: harvesting honey usually meant
destroying the skep and killing the colony. Modern beekeeping moved to
Langstroth hives specifically so the bees could keep living.

**This skep is a Langstroth in straw clothing.** Nothing gets torn
open to get the work done. Workers keep running between tasks. The
comb keeps growing. Named for the structure, not the harvest.
:::

## Roadmap

For the current release's supported backends, shipped MCP tools, and
known limitations, see the [Changelog](/changelog/). The roadmap in
short: expand wired LLM backends beyond Claude Code, document the
crash-recovery and session-resume flows end-to-end, and publish
reproducible benchmark scripts.
