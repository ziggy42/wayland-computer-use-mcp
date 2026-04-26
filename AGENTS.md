# Core Values

- **Correctness, Performance & Security**: Absolute top priorities. When in doubt, consult the user on which to prioritize.
- **Simplicity**: Secondary priority; must not be achieved at the expense of correctness, performance or security.

# General Rules

- **Dependencies**: Do not include any external dependencies without explicit user approval.
- **Code Clarity**: Write self-explanatory code. Only add comments to explain genuinely non-obvious logic.
- **Scope**: Avoid creative additions unless explicitly requested.
- **Error Handling**: 
  - Never use `panic` unless explicitly instructed.
  - Do not ignore errors. Handle them or propagate them.
- **Naming Conventions**: Use self-explanatory variable names (e.g., use `name` instead of `n`).
- **Line Length**: Lines should be up to 80 chars long (consider tabs as 2 spaces).

# Git & Commits

- **Commit Messages**: Do not use conventional commit prefixes (e.g., `chore:`, `feat:`, `fix:`, `docs:`). Use plain, descriptive language.