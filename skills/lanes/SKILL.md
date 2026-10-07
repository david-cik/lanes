---
name: lanes
description: Find Claude Code sessions and attach them to (or take them off) tickets on the lanes board. Use when the user asks to link, attach, move, or unlink a session/agent to a ticket (e.g. "attach my CSP session to ABC-12", "which session was working on the export bug?", "take the Friday-updates session off ABC-7").
---

# lanes: attach sessions to tickets

lanes is a terminal board of tickets and the Claude Code sessions working them. Its CLI
reads the transcripts Claude keeps, so you can find a session by what it was about.

1. Find candidates:
   `lanes sessions --json [words…]` — running and recent (`--days N`, default 7) sessions
   with `id`, `title`, `cwd`, `first_prompt`, `recent_prompts`, `mentions` (ticket keys the
   session talks about), and its current `ticket` (`"-"` = deliberately on no ticket).
   Words filter by substring; run it without words if a filtered search finds nothing.
2. Pick the session the user means from title, prompts, folder, and mentions. If two or
   more fit about equally, show them (id prefix + title) and ask — don't guess.
3. Act:
   - `lanes link <session-id or prefix> <TICKET>` — put it on a ticket.
   - `lanes unlink <session-id or prefix>` — take it off its ticket (stays off; lanes won't
     re-attach it by matching).
4. Tell the user what you linked (id prefix, title, ticket). A running lanes panel shows the
   change within a few seconds.

Never edit lanes' state files directly; use these commands.
