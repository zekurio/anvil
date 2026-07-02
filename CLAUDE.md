@AGENTS.md

## Claude Code mechanics for workflows and subagents

AGENTS.md owns the shared repo rules and model-selection policy. This file only documents Claude Code-specific execution details.

- gpt-5.5 is only reachable through the Codex CLI, invoked directly via Bash (my `~/.codex/config.toml` defaults to gpt-5.5). `codex exec "<prompt>"` for implementation, `codex exec -s read-only "<prompt>"` for investigation/analysis that must not touch the tree, `codex review` for reviews. Prompts must be fully self-contained: repo path, relevant AGENTS.md rules, exact files, and verification commands — Codex shares no context with your session. Iterate on a previous run with `codex resume <session-id>`. For long tasks, run via Bash with `run_in_background` instead of blocking. Do not use the codex plugin commands, skills, or agents.
- Claude models (sonnet-5, opus-4.8, fable-5) run via the Agent/Workflow model parameter.

Using gpt-5.5 inside workflows and subagents (the model parameter only takes Claude models, so use a wrapper):

- Spawn a thin Claude wrapper agent with `model: 'sonnet', effort: 'low'` whose prompt instructs it to write a self-contained codex prompt, run `codex exec` via Bash, and return Codex's final message verbatim.
