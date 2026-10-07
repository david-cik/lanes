# lanes

A terminal control panel for local AI coding agents, organized by the tickets they work on.

lanes shows the issues assigned to you (Linear today) and the agent sessions working each
one. From the board you can start an agent on a ticket in its own git worktree, watch it
live, send it a message, approve its permission prompts, check its branch and pull
request, and stop it.

```
⚠ 1 waiting (a) · lanes · me · 12 tickets · 4 agents · updated 14:02:11
Alpha                                       │ claude ▸ abc-12/fix-login
  ── In Progress ──                         │ (the selected agent, live)
    ABC-12 Fix login redirect               │
      ● claude ABC-12 fix-login working 4m ◀│
      ⚠ claude ABC-12 review waiting · Bash:│
  ── In Review ──                           │
    ABC-9 Rate limit the export endpoint    │
Unlinked                                    │
  ○ claude scratch idle 1h · ~/src (ro)     │
enter show · d details · a approve · n new …│
```

## Requirements

- macOS or Linux
- [tmux](https://github.com/tmux/tmux) (lanes runs inside it)
- [Claude Code](https://docs.claude.com/en/docs/claude-code) for agents
- [GitHub CLI](https://cli.github.com) (`gh`, signed in) for pull request details — optional
- Go (latest) to install

## Install

```sh
go install github.com/david-cik/lanes@latest
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

Tickets assigned to you, grouped by team and workflow state; completed and canceled ones
are hidden. Under each ticket are the agents working on it. An agent belongs to a ticket
when lanes started it for that ticket, when you linked it (`l`), or when its git branch,
session name, or folder contains the ticket key. Everything else is under **Unlinked**.
`(ro)` marks sessions lanes didn't start: you can see and link them, and adopt them (`A`),
but not control them.

Status: `●` working, `○` idle, `⚠` waiting for you, `◌` unknown.

## Keys

| Key | Does |
|---|---|
| `j` `k` `g` `G` | move |
| `enter` | on an agent: show it in the right pane · on a ticket: show its agent, or resume one of its ended sessions, or start a new one · on an ended session: resume it |
| `n` | start an agent on the selected ticket |
| `A` | adopt a session lanes didn't start: fork it into a lanes pane (the original keeps running); on an ended session, resume it |
| `a` | answer the selected (or oldest) waiting permission request |
| `s` | send text to the selected agent |
| `x` | stop the selected agent (its worktree and branch are kept) |
| `l` | link / unlink a session to a ticket (saved) |
| `L` | suggest links: read unlinked Claude sessions' transcripts and propose a ticket for each |
| `d` | details: status, git, pull request, recent activity (or ticket details) |
| `o` | open the agent's pull request, or the ticket, in the browser |
| `u` | show another person's tickets |
| `r` | refresh |
| `q` | quit (agents keep running in their own tmux sessions) |

Use your usual tmux keys (prefix + arrow, or the mouse) to move between the board and
the agent pane.

### Starting an agent (`n`)

lanes picks the repository from the ticket's linked pull requests or a repository named
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

`L` reads the transcripts Claude keeps for each unlinked session and proposes the open
ticket it mentions most — counting what you typed above commands, and those above the
agent's own text, and recent mentions above old ones. Clear matches come pre-checked;
mixed ones show the alternatives. Nothing is linked until you press enter, and `l`
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

## Configuration

`~/.config/lanes/config.toml` (or `$XDG_CONFIG_HOME/lanes/config.toml`). Every key is
optional.

```toml
assignee      = "me"          # whose tickets: "me", a user id, name, or email
linear_poll   = "60s"         # how often tickets refresh (min 1s)
external_poll = "5s"          # how often agent sessions refresh (min 1s)
repo_roots    = ["~/src", "~/git"]   # where to look for repositories
default_agent = "claude"
branch_template   = "{key_lower}/{slug}"           # must contain {key} or {key_lower}
worktree_template = "{repo}/.worktrees/{branch}"
notify_os = false             # also send desktop notifications
auto_link = false             # link new Claude sessions whose transcript clearly points at one open ticket

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

[teams."Platform"]            # order of a team's workflow states on the board
state_order = ["Todo", "In Progress", "In Review", "Done"]
```

State lives in `~/.local/state/lanes/` (or `$XDG_STATE_HOME/lanes/`): launched agents,
manual links, and the panel's socket.

## What lanes never does

- Store or evaluate permission rules — Claude does, through its own settings.
- Press keys in an agent's terminal for you.
- Edit your Claude settings, except `install-hooks` / `uninstall-hooks` after you agree.
- Delete worktrees or branches.
- Write to your issue tracker or GitHub.

## Troubleshooting

- **"live status and approvals are off"** — the panel couldn't open its socket: another
  lanes panel is running, or the state path is too long for a unix socket. Agents still
  work; they just don't report.
- **Pull request says "gh is not signed in"** — run `gh auth login`.
- **"lanes is already running in tmux pane …"** — only one panel per tmux server.
- **The launch screen warns about `go run`** — install lanes with `go install`.

## Development

```sh
go test ./...                          # unit tests
go test -tags integration ./...        # also drives a real tmux server
```

## License

Apache-2.0
