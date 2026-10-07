# lanes

A terminal control panel for local AI coding agents, organized by the tickets they work on.

lanes shows the issues assigned to you (Linear today) and the agent sessions working each
one, inside tmux, with the selected agent live beside the board. From the board you can
start an agent on a ticket — yours, or an unassigned one from your team — in a folder or
its own git worktree, watch it, send it a message, approve its permission prompts, check
its branch and pull request, and stop it. Sessions you started elsewhere show up too:
put them on tickets yourself, let lanes suggest tickets from their transcripts, or ask
Claude in plain words ("find my old sessions for this ticket").

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
- [Claude Code](https://docs.claude.com/en/docs/claude-code) for agents
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
(`┏ ┃ ┗`; order them with `state_order`); completed and canceled ones are hidden. Under each ticket are the agents working on it. An agent belongs to a ticket
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

Status: a spinner while working, `○` idle, `⚠` waiting for you ("needs you"), `·` ended,
`◌` unknown. `◀` marks the agent shown on the right.

## Keys

| Key | Does |
|---|---|
| `↑` `↓` (or `j` `k`) | move — the right pane shows the selected agent (or the selected ticket's agent) as you go |
| `Home` `End` (or `g` `G`) | top / bottom |
| `PgDn` `PgUp` (or `ctrl+d` `ctrl+u`) | half a page |
| `/` | filter tickets and agents as you type; `enter` keeps the filter, `esc` clears it |
| `enter` | on an agent: go to it · on a ticket: go to its agent, or resume one of its ended sessions, or start a new one · on an ended session: resume it |
| `l` `→` | go to the agent pane |
| `n` | start an agent on the selected ticket |
| `A` | adopt a session lanes didn't start: fork it into a lanes pane (the original keeps running, off the board); on an ended session, resume it |
| `a` | answer the selected (or oldest) waiting permission request |
| `s` | send text to the selected agent |
| `x` | stop the selected agent (its worktree and branch are kept) |
| `t` | move a session to another ticket, or take it off its ticket (saved; matching by branch, `T` and `auto_link` won't put it back; an ended session that's taken off disappears from the board) |
| `T` | suggest tickets: read unlinked Claude sessions' transcripts and propose a ticket for each |
| `:` | ask Claude in plain words — "attach my CSP session to ABC-12", or on a ticket "find my old sessions for this ticket". It knows the ticket you're on, proposes one or more links (you confirm), asks back if unsure (you answer), or just answers |
| `d` | details: status, git, pull request, recent activity (or ticket details) |
| `o` | open the agent's pull request, or the ticket, in the browser |
| `u` | show another person's tickets |
| `r` | refresh |
| `?` | list every key (the footer shows only the keys for the selected row) |
| `c` | on the start screen of an up-for-grabs ticket: claim it in Linear, or not |
| `q` | quit (agents keep running in their own tmux sessions) |

**Mouse:** click a row to select it (the agent shows on the right), click it again to go
to it, scroll to move, click a key in the footer to press it, click a choice in a menu.

**Move between the board and the agent pane with your tmux prefix and `h` / `l`**, as in
herdr (`Ctrl-b h` / `Ctrl-b l` by default; the arrows work too), with the prefix pressed
twice (`Ctrl-b Ctrl-b`), **or by clicking either one**. A thin title over each pane marks
the one you're in: `● board` / `○ agent · <its title>`. `prefix z` zooms the agent pane.

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

The start screen shows where the agent will run: `y`/`enter` starts it, `f` picks another
folder (your launch folder, any repo as it is, or a path you type), and `w` switches to a
fresh git worktree for the ticket.

**Launch folder:** set `launch_dir` (e.g. `"~/git"`) to start new agents in one folder you
choose, as it is — no worktree or branch is created.

**Worktree per ticket** (the default without `launch_dir`): lanes picks the repository from the ticket's linked pull requests or a repository named
in its description (otherwise it asks), fetches, and creates a worktree on a new branch
from `origin`'s default branch — or reuses an existing branch or worktree. It never
touches your local default branch. The agent starts with a prompt built from the ticket.
Only one agent can run in a worktree; a second agent on the same ticket gets a warning.

Claude asks whether to trust each new folder. lanes notices the prompt and tells you to
answer it in the agent's pane; it never presses keys for you.

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

Claude's own prompt stays on screen the whole time, so you can always answer there too.

## Sessions lanes didn't start

`T` reads the transcripts Claude keeps for each unlinked session and proposes the open
ticket it mentions most — counting what you typed above commands, and those above the
agent's own text, and recent mentions above old ones. Clear matches come pre-checked;
mixed ones show the alternatives. Nothing is linked until you press enter, and `t`
changes or removes any link. With `auto_link = true`, new sessions with one clearly
dominant ticket are linked automatically.

Linked sessions that have ended (closed terminal, killed process) stay on the board under
their ticket for 14 days, marked "ended". `enter` or `A` resumes one in a lanes pane with
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
branch_template   = "{key_lower}/{slug}"           # must contain {key} or {key_lower}
worktree_template = "{repo}/.worktrees/{branch}"
notify_os = false             # also send desktop notifications
auto_link = false             # link new Claude sessions whose transcript clearly points at one open ticket
focus_key = ""                # optional single key that jumps board ⇄ agent, e.g. "C-]", "F12" ("" = prefix + h/l)

# The first prompt an agent gets. Placeholders: {key} {key_lower} {slug} {title} {url}
# {description} {preamble} {repo} {branch}
prompt_template = """
{preamble}

Ticket {key}: {title}
{url}

{description}
"""
preamble = ""                 # standing instructions, e.g. "Run the tests before finishing."

[agents.claude]
args = []                     # extra arguments, e.g. ["--model", "sonnet"]

[teams."Platform"]            # order of a team's workflow states on the board; the first
state_order = ["Backlog", "Todo", "In Progress", "In Review"]   # started one is where a claimed ticket goes
```

State lives in `~/.local/state/lanes/` (or `$XDG_STATE_HOME/lanes/`): launched agents,
manual links, and the panel's socket. Edit links with `t`, `T`, `:` or `lanes link`, not by
hand.

## What lanes never does

- Store or evaluate permission rules — Claude does, through its own settings.
- Press keys in an agent's terminal for you.
- Edit your Claude settings, except `install-hooks` / `uninstall-hooks` after you agree.
- Delete worktrees or branches.
- Write to your issue tracker, except claiming an "up for grabs" ticket you start an
  agent on (which the start screen shows, and `c` turns off); never to GitHub.

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
