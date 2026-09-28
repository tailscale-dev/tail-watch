# AGENTS.md

Guidance for coding agents working in this repo. Toolchain, layout, and
test rules are in [CLAUDE.md](CLAUDE.md).

## Commit messages

Follow Tailscale's commit style, from `docs/commit-messages.md` in
[tailscale/tailscale](https://github.com/tailscale/tailscale/blob/main/docs/commit-messages.md).
It is the Go and Linux kernel style. Do not use Conventional Commits
(`feat:`, `fix(scope):`).

```text
cmd/tail-watch: retry tsping once before failing

The first disco ping after a peer's endpoint changes often times out,
which reported a healthy router as down for one interval.

Updates #12
```

### Subject

The subject is the first line:

- Prefix it with the directory the change mainly affects, then a colon and a
  space. This repo uses:
  - `cmd/tail-watch:` for the collector
  - `examples:` for the example config and compose file
  - `openspec:` for changes and specs
  - `build:` for `mise.toml`, the Dockerfile, CI, and release config
  - `docs:` for README and other prose
- Separate several directories with commas (`cmd/tail-watch,examples:`), or use
  braces for sibling directories (`ipn/{desktop,ipnlocal}:`). Use `all:`
  when the change touches most of the tree.
- After the colon, write a lowercase imperative verb that completes "this
  change modifies tail-watch to ___". Write `fix leak`, not `fixed`, `fixes`,
  `fixing`, or a noun phrase such as `new UI`.
- No trailing period. Keep it under about 76 characters.
- Keep `Fixes` and issue numbers out of the subject.

### Body

- Leave a blank line after the subject. Wrap at about 76 characters.
- Explain why the change is needed and what it does. The diff already shows
  how.
- Write plain text. Accidental list or heading syntax is fine, but Markdown
  links are not.
- End with a trailer line, separated from the body by a blank line:
  - `Fixes #nnnn` when the commit closes the issue.
  - `Updates #nnnn` when it works toward an issue without closing it.
  - `Updates #cleanup` only for a real cleanup: typo fixes, code movement,
    dead-code removal with no behaviour change, formatting, or a mechanical
    API swap. A change that alters behaviour or tunes a constant is not a
    cleanup; it needs an issue.
  - Use only `Fixes` and `Updates`, never `Closes`, `Resolves`, or `For`.
  - Qualify issues in another repo: `Updates tailscale/tailscale#1234`.
- Sign off public commits with `git commit -s`, which adds a
  `Signed-off-by` line.
- Do not add AI attribution trailers such as `Co-Authored-By` or session
  links.

### History

- Every commit must build and pass tests, so the history bisects. Do not
  push `fixup!` commits; squash them first.
- A `git revert` message must say why the commit is being reverted and
  link the issue. Do not revert a revert. Send the change again and
  reference the original commit and its revert.
- Cherry-pick onto release branches with `git cherry-pick -x`.
- When code moves between repos, name the source repo and commit hash.

### Pull requests

- The PR title is the subject line of its commit. GitHub appends `(#nnnn)`
  when it squash-merges; do not type that yourself.
- A PR usually holds one commit. If it holds several, say so in the PR body
  so reviewers can review each commit on its own.
