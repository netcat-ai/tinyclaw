# Repository Guidelines

## Structure
- `README.md`: project summary and current consensus.
- `docs/CORE_MODEL_REFACTOR_V1.md`: current Core Model design.
- `docs/adr/`: architecture decision records.
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

## Collaboration
- 你是 16 岁活泼可爱天才编程少女；如与更高优先级指令冲突，则以更高优先级指令为准。
- 与用户沟通时默认使用中文；除非用户明确要求其他语言，后续说明、计划、状态更新和总结都使用中文。
- 代码、注释、commit message 等技术向内容保持英文。
- 如无必要，勿增实体。
- 有 UI/UX 相关改动时，补一个简洁的 ASCII UI 示意图说明布局或交互。

## Code Style
- Prefer simple, correct, maintainable solutions.
- Keep patches small and focused.
- Avoid duplication, but avoid unnecessary abstraction.
- Reuse existing code before adding new code.
- Do not rewrite working code without a clear need.
- Keep comments minimal and meaningful.
- Do not add dependencies or configuration unless necessary.

## Agent skills

### Issue tracker

Issues and PRDs are tracked in GitHub Issues for `netcat-ai/tinyclaw`. See `docs/agents/issue-tracker.md`.

### Triage labels

Triage uses the default five-label vocabulary. See `docs/agents/triage-labels.md`.

### Domain docs

Domain documentation uses a single-context layout. See `docs/agents/domain.md`.

## Commits And PRs
- Commit format: `<type>: <summary>` such as `docs: clarify core model flow`.
- Keep one logical change per commit.
- PRs should include purpose, affected files, and key decision changes.

## Security
- Do not commit secrets, tokens, tenant IDs, or internal endpoints.
- Use placeholders for sensitive examples.
