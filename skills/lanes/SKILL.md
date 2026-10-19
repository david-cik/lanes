---
name: lanes
description: Work with the lanes board of tickets and the Claude Code sessions working them. Use when the user asks to link, attach, move, or unlink a session/agent to a ticket (e.g. "attach my CSP session to ABC-12", "which session was working on the export bug?", "take the Friday-updates session off ABC-7"), or to check on, read, message, wait for, or start the agents on their tickets (e.g. "what did the ABC-12 agent say?", "tell the DNS agent ABC-14 owns the certs", "wait until ABC-12's agent is done", "start an agent on ABC-15", "where does the Apollo project stand?").
---

# lanes

lanes is a terminal board of tickets and the Claude Code sessions working them. Its CLI
reads the transcripts Claude keeps, so you can find a session by what it was about, and it
can talk to the running lanes panel to watch and steer the agents on the board.

## Attach sessions to tickets

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

## Watch and steer the agents on the board

These need the lanes panel running (otherwise they fail with "no lanes panel is running";
tell the user). They print JSON. Name an agent by its `id` from `lanes agents`, a prefix of
it, or its ticket key when one agent is on that ticket. If the session has the lanes MCP
tools (`agents`, `start`, `send`, `read`, `wait`), use those instead; they're the same.

- `lanes agents [--project NAME]` — tickets (key, title, state, project) with the agents on
  each: `status` (working, idle, waiting, ended), `ready` (finished a turn nobody looked
  at), `waiting` (what it needs from the user), `recent` activity, and `lanes` (true = lanes
  started it, so `send` works). Agents on no ticket are under `unlinked`. Also gives `seq`.
- `lanes read <agent>` — what the agent wrote since its last prompt.
- `lanes send <agent> <text…>` — type text into an agent lanes started and press Enter, as
  if the user typed it. Refused while the agent shows a prompt (permission, question,
  folder trust): only the user can answer those, so tell them.
- `lanes wait [--after SEQ] [--timeout D] [agent|TICKET…]` — block until an agent starts,
  finishes a turn (`ready`), needs the user (`needs_you`), or ends. Default timeout 90s
  (`"timeout": true` and no events). Pass back the `seq` from the previous answer as
  `--after` so nothing is missed; keep each call under your command time limit.
- `lanes start <TICKET> [instructions…]` — ask the user to start an agent on a ticket. It
  answers at once; the user decides in the panel, and `lanes wait` reports `started` with
  the new agent's id if they say yes. Instructions are added to the agent's first prompt.

Only `send` and `start` affect anything. Before using `send`, make sure the user wants that
agent told that thing. Quote what you send, and what agents reply, when you report back.

To have a project looked after continuously, the user can press `C` on the project in the
panel to start its coordinator, a session built for this.

Never edit lanes' state files directly; use these commands.
