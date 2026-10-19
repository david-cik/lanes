# lanes

A terminal control panel for local AI coding agents, organized by the tickets they work on.

lanes shows the issues assigned to you (Linear today) and the agent sessions working each
one, inside tmux, with the selected agent live beside the board. From the board you can
start an agent on a ticket — yours, or an unassigned one from your team — in a folder or
its own git worktree, watch it, send it a message, approve its permission prompts, check
its branch and pull request, and stop it. Sessions you started elsewhere show up too:
put them on tickets yourself, let lanes suggest tickets from their transcripts, or ask
an agent in plain words ("find my old sessions for this ticket").

```
 ● board ───────────────────────────────────┬─ ○ agent · ✳ ABC-12 fix-login ──
▐lanes▌  me · 12 tickets  ⠹ 1  ⚠ 1 waiting  │
 Alpha  12                                  │
 ┏ ▸ UP FOR GRABS ───────────────────── 4   │  (the selected agent, live)
 ┏ IN PROGRESS ──────────────────────── 3   │
 ┃  ABC-12 Fix login redirect  ⚠            │
 ┃   ├─ ⠹ fix-login     working    4m  ◀    │
 ┃   └─ ⚠ review        needs you  · Bash…  │
 ┗  ABC-9 Rate limit the export endpoint    │
 ╭ UNLINKED ─────────────────────────── 1   │
 ╰   ○ scratch          idle       1h  ~/src│
⟨enter⟩ go to  ⟨s⟩ send  ⟨x⟩ stop  ⟨t⟩ ticket  ⟨?⟩ keys
```

## Requirements

- macOS or Linux
- [tmux](https://github.com/tmux/tmux) 3.3 or newer (lanes runs inside it)
- An agent CLI: [Claude Code](https://docs.claude.com/en/docs/claude-code) and
  [GitHub Copilot CLI](https://github.com/github/copilot-cli) (1.0 or newer), and
  [Codex](https://github.com/openai/codex) are built in; any other CLI can be added in the
  config (see
  [Other agent tools](#other-agent-tools))
- [GitHub CLI](https://cli.github.com) (`gh`, signed in) for pull request details — optional
- Go (latest) to install

## Install

```sh
go install github.com/david-cik/lanes@latest     # or a release, e.g. @v0.1.0
```

Use the installed binary rather than `go run`: launched agents call back into lanes
through hooks, and a `go run` build disappears when it exits.

## First run

```sh
lanes
```

lanes starts (or attaches to) a tmux session called `lanes`. The first time, it opens
your browser to sign in to Linear; the sign-in is kept in your OS keychain. To use a
personal API key instead, set `LINEAR_API_KEY`. `lanes auth logout` forgets the sign-in.

`lanes --no-tmux` shows a read-only board without tmux.

## The board

Tickets assigned to you, grouped by team, each workflow state a lane down the left edge
(`┏ ┃ ┗`; order them with `state_order`); completed and canceled ones are hidden. Within
a lane, tickets are grouped by project (`◆ <project> · count`, `◇ no project` last;
`group_by_project = false` turns it off). Under each ticket are the agents working on it. An agent belongs to a ticket
when lanes started it for that ticket, when you put it there (`t`), or when its git branch,
session name, or folder contains the ticket key. Everything else is under **Unlinked**
(the dashed lane).
First in each team is **Up for grabs**: the team's unassigned tickets in a not-started
(Todo-type) state, closed until you press `enter` on it. Starting an agent on one offers
to claim it in Linear — assign it to you and move it to the team's first started state
(the first started state in your `state_order`, else "In Progress") — on by default,
`c` on the start screen leaves it as it is.

`external` marks sessions lanes didn't start: you can see them, put them on tickets, and
adopt them (`A`), but not control them. Once you adopt one, its original (which keeps
running) is no longer shown.

Status: a spinner while working, `✓` ready (finished a turn you haven't looked at yet),
`○` idle, `⚠` waiting for you ("needs you"), `·` ended, `◌` unknown. `◀` marks the agent shown on the right.

## Keys

These are the defaults. To choose your own, run `lanes keys`: it asks about each action in
turn ("Key to start an agent on a ticket?"); press the key you want, `enter` to keep the
current one, or `backspace` for its default, and save at the end. `lanes keys --reset` puts
them all back. Moving around (`j k g G [ ] l`, arrows, `enter`, `esc`) stays as it is. The
footer and `?` always show your keys; restart lanes to pick up changes.

| Key | Does |
|---|---|
| `↑` `↓` (or `j` `k`) | move — the right pane shows the selected agent (or the selected ticket's agent) as you go |
| `Home` `End` (or `g` `G`) | top / bottom |
| `PgDn` `PgUp` (or `ctrl+d` `ctrl+u`) | half a page |
| `[` `]` | previous / next team |
| `/` | filter tickets and agents as you type; `enter` keeps the filter, `esc` clears it |
| `enter` | on an agent: go to it · on a ticket: go to its agent, or resume one of its ended sessions, or start a new one · on an ended session: resume it · on a team: fold it to one line (with its working / waiting counts) or unfold it · on Up for grabs: open or close it |
| `l` `→` | go to the agent pane |
| `N` | create a ticket, assigned to you in Todo, in the team (and project) of the row you're on. Claude drafts it — from a few words you type, or, on a session's row, from that conversation (and the session goes on the new ticket) — and you review it: `y` create, `e` edit in `$EDITOR`, `esc` drop. If an open ticket already covers it, Claude says so (and, for a session, `l` puts it there instead). If Claude can't be reached, you type the title yourself |
| `n` | start an agent on the selected ticket |
| `A` | adopt a session lanes didn't start: fork it into a lanes pane (the original keeps running, off the board); on an ended session, resume it |
| `C` | the project's coordinator (on a project, or a ticket in one): show it, or start one — see [below](#a-projects-coordinator-c) |
| `a` | answer the selected (or oldest) waiting permission request |
| `s` | send text to the selected agent |
| `x` | stop the selected agent (its worktree and branch are kept) |
| `t` | move a session to another ticket, or take it off its ticket (saved; matching by branch, `T` and `auto_link` won't put it back; an ended session that's taken off disappears from the board) |
| `T` | suggest tickets: read unlinked Claude sessions' transcripts and propose a ticket for each |
| `:` | ask Claude in plain words — "attach my CSP session to ABC-12", or on a ticket "find my old sessions for this ticket". It knows the ticket you're on, proposes one or more links (you confirm), asks back if unsure (you answer), or just answers |
| `d` | details: status, git, pull request, recent activity (or ticket details) |
| `i` | the ticket — the selected one, or the selected agent's — on the whole panel: state, project, pull requests, agents, full description (scroll with `↑` `↓`); `o` / `p` / `n` from there |
| `o` | open the ticket in the browser (on an agent: its ticket; an agent on no ticket opens its pull request) |
| `p` | the ticket's pull requests (on a ticket or one of its agents): opens the one, or lists them with title and state to pick from |
| `u` | show another person's tickets |
| `r` | refresh |
| `?` | list every key (the footer shows only the keys for the selected row) |
| `c` | on the start screen of an up-for-grabs ticket: claim it in Linear, or not |
| `q` | quit (agents keep running in their own tmux sessions) |

**Mouse:** click a row to select it (its agent shows on the right). Clicking in the board
keeps you in the board: to go to the agent, click its pane. Clicking the selected row
again opens a ticket's card, folds a team or Up for grabs, or offers to resume an ended
session. Scroll to move, click a key in the footer to press it, click a choice in a menu.

**Move between the board and the agent pane with your tmux prefix and `h` / `l`**, as in
herdr (`Ctrl-b h` / `Ctrl-b l` by default; the arrows work too), with the prefix pressed
twice (`Ctrl-b Ctrl-b`), **or by clicking either one**. The pane you're in is easy to
tell: its title over the pane is a solid bar (` ● board ` / ` ● agent · <its title> `),
the divider lights up on its side, and the other pane's plain text turns grey
(`dim_inactive = false` keeps it as is). `prefix z` zooms the agent pane.

While it runs, lanes turns the mouse on for its tmux session and borrows those keys
there only: elsewhere they keep tmux's defaults (the prefix pressed twice still sends the
prefix), keys you've bound yourself are left alone, and everything is put back when lanes
quits. So inside lanes the prefix pressed twice doesn't reach the agent (unless you've
bound it yourself) — for Claude, `Ctrl-b` would move the session to the background and
leave a copy of it on the board.

Prefer one key? Set `focus_key` (a tmux key name such as `"C-]"` or `"F12"`): it jumps
between board and agent inside the lanes session only, passes through everywhere else,
and is removed when lanes quits. A binding you already have for that key is left alone.

### Starting an agent (`n`)

The start screen shows where the agent will run and with which tool (`default_agent`;
`t` picks another installed one): `y`/`enter` starts it, `f` picks another
folder (your launch folder, any repo as it is, or a path you type), and `w` switches to a
fresh git worktree for the ticket.

**Launch folder:** set `launch_dir` (e.g. `"~/git"`) to start new agents in one folder you
choose, as it is — no worktree or branch is created.

**Worktree per ticket** (the default without `launch_dir`): lanes picks the repository from the ticket's linked pull requests or a repository named
in its description (otherwise it asks), fetches, and creates a worktree on a new branch
from `origin`'s default branch — or reuses an existing branch or worktree. It never
touches your local default branch. The agent starts with a prompt built from the ticket.
Only one agent can run in a worktree; a second agent on the same ticket gets a warning.

**Tickets in the same project.** An agent's first prompt lists the other open tickets in its
ticket's project that lanes sees (yours, and the unassigned ones up for grabs) — and which
already have an agent — and tells it to stay on its own
ticket, saying so rather than doing work that belongs to another. And if two agents on
different tickets change the same file anyway (compared within a repo, so the same file in
two worktrees counts), lanes warns once: the later one's row reads `⚑ same files as
ABC-12`, and you get a notification. `s` tells it what to do.

Claude asks whether to trust each new folder. lanes notices the prompt and tells you to
answer it in the agent's pane; it never presses keys for you.

### Notifications

When an agent lanes started finishes a turn, its row turns `✓ ready`, the header counts
`✓ n ready`, the terminal bell rings, and tmux shows "<ticket> is ready for you" — unless
that agent is the one on screen. Looking at it (showing or going to it) clears the mark.
Permission requests ring and notify the same way. Set `notify_os = true` for desktop
notifications too. lanes sends them through your terminal when it knows how — the escape
code iTerm2, WezTerm and Ghostty (OSC 9), kitty (OSC 99) or foot (OSC 777) understand,
passed through tmux — so they carry the terminal's icon and a click brings you back to it. Other terminals get the system's notifier (`osascript`,
`notify-send`). `notify_via = "terminal"` or `"system"` overrides the guess.

### Approving (`a`)

When an agent asks for permission, its row shows `⚠` and the command, the terminal bell
rings, and tmux shows a message. In the approval dialog:

| Key | Does |
|---|---|
| `y` | allow once |
| `s` | allow and remember for this session |
| `A` | allow and always allow in this repository (Claude saves the rule in the repo's `.claude/settings.local.json`) |
| `e` | edit the rule before saving it |
| `tab` | pick another suggested rule |
| `n` | deny |
| `m` | deny with a message to the agent |
| `p` | answer in the agent's own pane instead |

Claude's own prompt stays on screen the whole time, so you can always answer there too. A
project's coordinator can answer them as well, if `coordinator.approve` lets it (see
[A project's coordinator](#a-projects-coordinator-c)).

## Sessions lanes didn't start

Sessions you started yourself show up for tools whose sessions lanes can list and whose
transcripts it can read (Claude today; each tool's adapter says what it can do).

`T` reads the transcript kept for each unlinked session and proposes the open
ticket it mentions most — counting what you typed above commands, and those above the
agent's own text, and recent mentions above old ones. Clear matches come pre-checked;
mixed ones show the alternatives. Nothing is linked until you press enter, and `t`
changes or removes any link. With `auto_link = true`, new sessions that are clearly about
one ticket are linked automatically — it has to outweigh the runner-up twice over and make
up at least half of all ticket mentions, so a session that went over many tickets (a
review, a planning pass) is left alone. Sessions run by tools and plugins (Agent SDK,
`claude -p`) never show up.

Linked sessions that have ended (closed terminal, killed process) stay on the board under
their ticket for 3 days (`ended_days`), marked "ended". `enter` or `A` resumes one in a lanes pane with
its conversation intact, so lanes controls it from then on.

By default, sessions you start yourself show up read-only with polled status. To give
them live status, activity, and approvals too:

```sh
lanes install-hooks      # asks first, keeps a backup of ~/.claude/settings.json
lanes uninstall-hooks    # removes exactly what install-hooks added
```

### From any Claude session

```sh
lanes sessions [--days N] [words…] # running and recent (7 days) sessions: title, folder, prompts, ticket, mentions
lanes link <session-id> <TICKET>   # put it on a ticket (id prefix is enough)
lanes unlink <session-id>          # take it off its ticket
```

Copy `skills/lanes` into `~/.claude/skills/` and you can just tell Claude "attach my
session about the export bug to ABC-12". A running panel shows the change within seconds.
The skill also covers the commands below, so "what did the ABC-12 agent say?" or "wait
until ABC-12's agent is done" work from any Claude session too.

### A project's coordinator (`C`)

On a project heading, or a ticket in a project, `C` starts that project's coordinator: a
Claude session on no ticket that keeps the project's agents moving and talks to them for
you. It opens with a summary of where the project stands and asks what you want done; from
there you can say things like "start agents on the two DNS tickets and keep them from
overlapping". It's listed under **Coordinators** at the bottom of the board, and there's
one per project (`C` again shows it). It works through the lanes MCP tools (below). You
can always talk to a ticket's agent directly as before.

You can run a project entirely from its coordinator: it starts agents, answers their
permission prompts and folder-trust prompts, and stops them. What needs your yes, and
where, is set in the config:

| `[coordinator]` | `"ask"` (default) | `"auto"` | the other |
|---|---|---|---|
| `start` | Claude asks you in the coordinator's pane before each start | it starts agents on its own | `"panel"`: you confirm each start in the lanes panel |
| `approve` | it may deny; each allow asks you in its pane, showing the request | it allows and denies on its own | `"off"`: it can't answer prompts; you do, in the panel |

Stopping an agent always asks you in the coordinator's pane. The coordinator runs in
Claude's `manual` permission mode, so an `auto` or bypass default in your Claude settings
can't skip those questions. It answers prompts of, and stops, only its own project's agents. An agent a coordinator starts doesn't
take over the right pane; it starts in the launch folder if one is set, else in a new
worktree in the ticket's repo (the coordinator names the repo when lanes can't tell).

### Letting one agent coordinate others

A running panel also takes requests from other programs, so one agent can look after a
project's tickets: see where each stands, start agents, talk to them, and wait for them.
Output is JSON. An agent is named by the `id` that `lanes agents` lists, a prefix of it,
or its ticket key when one agent is on that ticket.

```sh
lanes agents [--project NAME]         # tickets (state, project) with their agents: status, ready, waiting, recent activity
lanes start [--repo R] <TICKET> [instructions…]
                                      # start an agent on a ticket (see below); the instructions go after its first prompt
lanes send <agent> <text…>            # type text into an agent lanes started, and press Enter (refused while it shows a prompt)
lanes read <agent>                    # what the agent wrote since its last prompt
lanes approve <agent> <request…>      # allow what it's waiting on: a permission request (once), or "folder-trust prompt"
lanes deny <agent> <request> [why…]   # deny a permission request, telling it why
lanes stop <agent>                    # stop an agent lanes started (its worktree is kept)
lanes wait [--after SEQ] [--timeout D] [agent|TICKET…]
                                      # block until an agent starts, finishes a turn (ready), needs you, or ends
```

`approve` and `deny` work only for a project's coordinator, and take the request repeated exactly as `lanes agents` (`waiting`) or
`lanes wait` (`needs_you`) shows it, e.g. `"Bash: npm test"`, so the caller's own permission
prompt shows what it's answering, and an answer can't land on a newer request. They're off
entirely with `approve = "off"`.

The same requests are MCP tools (`agents`, `start`, `send`, `read`, `wait`, `approve`,
`deny`, `stop`) from
`lanes mcp`, a server on stdin/stdout. Coordinators get it automatically; to give it to
any other Claude session, `claude mcp add lanes -- lanes mcp`. An agent's own events are
left out of its waits.

From a project's coordinator, `lanes start` starts the agent (subject to
`coordinator.start`) and answers with its id and a `seq`. From anything else it never
starts anything by itself: the panel shows the usual start screen, marked as asked for by
another agent with its instructions, and you say yes or no (instructions not used within
two minutes are dropped).
`lanes wait` answers with the events and a `seq`; pass it back as `--after` and nothing
that happened in between is missed (`"missed": true` says the panel had already
dropped some, after a long gap; a `seq` from before the panel restarted gets everything
since). It gives up after 90 seconds by default (under a
tool's two-minute command limit), answering `"timeout": true`.

## Configuration

`~/.config/lanes/config.toml` (or `$XDG_CONFIG_HOME/lanes/config.toml`). Every key is
optional.

```toml
assignee      = "me"          # whose tickets: "me", a user id, name, or email
linear_poll   = "30s"         # how often tickets refresh (min 1s)
external_poll = "5s"          # how often agent sessions refresh (min 1s)
repo_roots    = ["~/src", "~/git"]   # where to look for repositories
launch_dir    = ""            # start new agents in this folder as it is (e.g. "~/git"); "" = a worktree per ticket
default_agent = "claude"
ask_agent = ""                # the tool that answers : and drafts N tickets ("" = default_agent if it can)
branch_template   = "{key_lower}/{slug}"           # must contain {key} or {key_lower}
worktree_template = "{repo}/.worktrees/{branch}"
notify_os = false             # also send desktop notifications
notify_via = "auto"           # how: "auto" (the terminal's own, if known), "terminal", "system"
dim_inactive = true           # fade the pane (board or agent) you're not in
auto_link = false             # link new Claude sessions whose transcript clearly points at one open ticket
ended_days = 3                # how long ended sessions stay under their ticket (0 = not at all)
group_by_project = true       # within a lane, group tickets by project
focus_key = ""                # optional single key that jumps board ⇄ agent, e.g. "C-]", "F12" ("" = prefix + h/l)

# The first prompt an agent gets. Placeholders: {key} {key_lower} {slug} {title} {url}
# {description} {preamble} {repo} {branch} {siblings} (the project's other open tickets;
# added at the end if the template leaves it out)
prompt_template = """
{preamble}

Ticket {key}: {title}
{url}

{description}

{siblings}
"""
preamble = ""                 # standing instructions, e.g. "Run the tests before finishing."

[agents.claude]
args = []                     # extra arguments, e.g. ["--model", "sonnet"]

[keys]                        # board keys you changed (lanes keys writes this; --reset removes it)
start = "n"                   # one character each; names as in `lanes keys`, e.g. send, stop, approve, quit

[coordinator]                 # what a project's coordinator (C) may do; see "A project's coordinator"
agent = ""                    # the tool it runs in ("" = default_agent if it can)
start = "ask"                 # "ask" (in its pane), "auto", or "panel"
approve = "ask"               # answering agents' prompts: "ask" (each allow, in its pane), "auto", or "off"

[teams."Platform"]            # order of a team's workflow states on the board; the first
state_order = ["Backlog", "Todo", "In Progress", "In Review"]   # started one is where a claimed ticket goes
```

State lives in `~/.local/state/lanes/` (or `$XDG_STATE_HOME/lanes/`): launched agents,
manual links, and the panel's socket. Edit links with `t`, `T`, `:` or `lanes link`, not by
hand.

## Other agent tools

lanes starts, watches, and stops agents through a small adapter per tool. Each feature
checks what the tool can do, and `default_agent`, `ask_agent`, and `coordinator.agent`
pick which tool does what.

| | Claude Code | Copilot CLI | Codex | any other (config) |
|---|---|---|---|---|
| start, send, stop | ✓ | ✓ | ✓ | ✓ |
| live status, ready, needs you | ✓ hooks | ✓ hooks (a plugin lanes loads per session) | ✓ hooks (passed per session) | from its pane |
| answer its prompts from lanes (`a`) | ✓ | — answer in its pane | ✓ | — |
| folder-trust prompt spotted | ✓ | ✓ | ✓ | — |
| sessions you started yourself on the board | ✓ | — (`lanes sessions` lists them) | — (`lanes sessions` lists them) | — |
| transcripts: `T`, auto_link, `read`, `N` from a session | ✓ | ✓ | ✓ | — |
| `:` and `N` (`ask_agent`) | ✓ | ✓ | ✓ (`codex exec`, read-only, not saved) | — |
| coordinator (`coordinator.agent`) | ✓ | ✓ | ✓ | — |
| resume an ended session | ✓ | — | ✓ (`codex fork`) | — |

Copilot asks before tool use through a hook that runs ahead of its own rules, so lanes
leaves those prompts to its pane rather than hold up every call.

Codex runs only hooks you've trusted in its `/hooks` screen, so lanes starts Codex with
`--dangerously-bypass-hook-trust` alongside its own hooks: in those sessions, any hooks
you've configured in Codex but never trusted run too. While lanes holds a Codex
permission request, Codex shows "Working" rather than its own prompt: answer with `a` on
the board (`p` there hands it back to Codex's prompt). Codex sessions lanes starts this way
don't show in `codex agents`.

Any other CLI works from the config, with no code: lanes can start it on a ticket, show it,
send it text, and stop it, and reads its status from its pane: changing = working, still =
idle, showing a `waiting` text = needs you (with a ready notification when it goes quiet,
as for Claude).

```toml
[agents.aider]
command = ["aider"]           # how to run it; lanes adds the first prompt last
prompt_flag = "--message"     # put this before the prompt ("" = the prompt alone)
stop = "/exit"                # typed to make it exit (else stop_keys, e.g. ["C-c", "C-c"])
waiting = ["Allow edits?"]    # text its pane shows when it needs you
```

Tools that aren't installed are left out. With more than one installed, the start screen's
`t` picks which one an agent runs in.

## What lanes never does

- Store or evaluate permission rules — Claude does, through its own settings.
- Press keys in an agent's terminal on its own: only text you send (`s`), or an agent you
  run sends (`lanes send`, or `lanes approve` answering a folder-trust prompt).
- Edit your Claude settings, except `install-hooks` / `uninstall-hooks` after you agree.
- Delete worktrees or branches.
- Write to your issue tracker, except creating a ticket you ask for (`N`) and claiming an
  "up for grabs" ticket you start an agent on (which the start screen shows, and `c` turns
  off); never to GitHub.

## Troubleshooting

- **"live status and approvals are off"** — the panel couldn't open its socket: another
  lanes panel is running, or the state path is too long for a unix socket. Agents still
  work; they just don't report.
- **Pull request says "gh is not signed in"** — run `gh auth login`.
- **"lanes is already running in tmux pane …"** — only one panel per tmux server.
- **The launch screen warns about `go run`** — install lanes with `go install`.
- **A ticket shows the same session twice** — usually `Ctrl-b` reached Claude and moved the
  session to the background, leaving a forked copy. `lanes unlink <id>` takes the copy off
  the ticket (lanes' own `Ctrl-b Ctrl-b` no longer lets this happen).

## Development

```sh
go test ./...                          # unit tests
go test -tags integration ./...        # also drives a real tmux server
```

## License

Apache-2.0
