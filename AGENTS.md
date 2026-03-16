# Repository Guidelines

## Structure
- `README.md`: project summary and current consensus.
- `docs/ARCHITECTURE_V0.md`: runtime architecture and event flow.
- `docs/AGENT_SANDBOX_INTEGRATION_V0.md`: agent-sandbox integration design.
- `docs/NEXT_STEPS.md`: execution roadmap.
- `docs/CONVERSATION_LOG.md`: decision history.

Prefer updating an existing document over creating overlapping files.

## Workflow
- `rg --files`: list files quickly.
- `rg -n "TODO|FIXME" *.md`: find open doc edits.
- `git diff -- *.md`: review markdown-only changes.
- `git status --short`: confirm the final change set.
- Use concise technical writing and stable terminology.
- Keep headings clear and task-oriented.
- Keep examples explicit and executable.
- Reuse existing repository vocabulary instead of inventing new terms.
- Verify links, commands, and key names across files.
- Keep changes aligned with `README.md`.
- When architecture behavior changes, update both design and execution docs together.

## Code Style
- Prefer simple, correct, maintainable solutions.
- Keep patches small and focused.
- Avoid duplication, but avoid unnecessary abstraction.
- Reuse existing code before adding new code.
- Do not rewrite working code without a clear need.
- Keep comments minimal and meaningful.
- Do not add dependencies or configuration unless necessary.

## Commits And PRs
- Commit format: `<type>: <summary>` such as `docs: clarify sandbox claim flow`.
- Keep one logical change per commit.
- PRs should include purpose, affected files, and key decision changes.

## Security
- Do not commit secrets, tokens, tenant IDs, or internal endpoints.
- Use placeholders for sensitive examples.
