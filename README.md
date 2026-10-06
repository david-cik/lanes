# lanes

A terminal control panel for local AI coding agents, organized by the tickets they work on.
lanes shows the issues assigned to you (Linear today) and the agent sessions working each one.

Status: early development (read-only board).

```sh
go install github.com/david-cik/lanes@latest
```

Auth: lanes signs in to Linear's MCP server in your browser on first run.
Set `LINEAR_API_KEY` to use a personal API key instead.

Licensed under Apache-2.0.
