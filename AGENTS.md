# Agent notes

## GitHub access from a managed Codex worktree

`gh` and HTTPS GitHub access are already authenticated in this environment. Do not print, copy, or
attempt to recover credentials. Read-only commands such as `gh pr view`, `gh api`, `git status`, and
`git log` should work directly.

The recurring Git failure in Codex-managed worktrees is usually **not an authentication failure**.
The worktree's `.git` file points at shared metadata under the original checkout, for example:

```text
/Users/.../git/context-guru/.git/worktrees/...
```

That location is outside the worktree's writable sandbox. Commands that update Git metadata
(`git switch`, `git add`, `git commit`) therefore request escalation. If
`approvals_reviewer = "auto_review"` is configured while the gateway cannot serve the exact
`codex-auto-review` model, escalation fails with HTTP 403 before the command runs.

The user-level fix is to remove that setting from `~/.codex/config.toml`, or set:

```toml
approvals_reviewer = "user"
```

Then restart Codex. Do not modify user configuration unless the user explicitly asks.

### Safe fallback for creating or updating a PR

When the shared Git metadata remains unwritable, use a clean temporary clone. This stays within the
writable `/tmp` root and uses the existing GitHub authentication:

```sh
pr_tmp=$(mktemp -d /tmp/context-guru-pr.XXXXXX)
git clone https://github.com/rossoctl/context-guru.git "$pr_tmp"
git -C "$pr_tmp" switch -c feat/<topic>
```

Copy only the reviewed task files from the Codex worktree into the clone. Preserve paths and modes,
and exclude generated files such as `__pycache__`; do not blindly copy the whole dirty worktree.
Run the relevant tests and `git diff --check` in the clone, then commit with DCO sign-off:

```sh
git -C "$pr_tmp" add <explicit-paths>
git -C "$pr_tmp" commit --signoff -m '<message>'
git -C "$pr_tmp" push -u origin feat/<topic>
gh pr create --repo rossoctl/context-guru --base main --head feat/<topic> \
  --title '<title>' --body '<body>'
```

For later PR updates, reuse the same temporary clone, copy only the changed files, test, commit with
`--signoff`, and run `git -C "$pr_tmp" push`. If commits must be rewritten solely to add DCO
trailers, use `git rebase --signoff` and `git push --force-with-lease`, never an unrestricted force
push.

The local credential helper may print warnings that its configured binary is missing. A push that
ends with a successful `To https://github.com/rossoctl/context-guru.git` ref update still succeeded;
verify with `gh pr view` rather than treating the helper warning alone as failure.

This fallback does not broaden authorization: only create branches, push commits, open issues/PRs,
or post review replies when the user has requested those GitHub mutations.
