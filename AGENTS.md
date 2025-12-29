# AGENTS for sshpick

These notes describe how to work with the `sshpick` CLI so future agents can reason about the new behaviors.

## Override the SSH config path
- The tool defaults to `~/.ssh/config` but accepts `-config /path/to/config` (or `--config`) to point at any other file.
- Any relative path is respected, so you can launch `sshpick` against a separate workspace copy or temporary config.

## Surface comment-based notes
- Any `# comment` line that appears before, between, or inline with directives for a host is captured as a note for that host.
- Notes are stored with the host entry and only shown when notes mode is enabled, so adding a descriptive comment becomes a lightweight metadata source.

## Toggle note visibility
- The TUI hides notes by default; press `n` while browsing hosts to toggle the extra comment rows on and off.
- When notes are visible, each comment is rendered under its host row with an explicit `Note:` label so you can read the stored context.

Use these instructions whenever you need to modify how `sshpick` reads configs, surfaces notes, or exposes UI controls.
