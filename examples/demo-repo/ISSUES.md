# Seeded issues

Shape, parsed by a script: each issue is a `## <title>` heading, then a `Labels: a, b` line (comma-separated),
then a blank line, then the body up to the next `## ` heading. Nothing above the first `## ` is an issue.

## Fix the broken link in docs/guide.md

Labels: class:docs-links

The second link in `docs/guide.md` points to `setup.md`, which does not exist. Retarget the broken link to a
page that exists in this repository, for example `../README.md`. Do not remove it. Change only that file.

## Add a sentence to the README about what this repository is for

Labels: documentation

`README.md` says what the repository is, but not what it is for. Add one sentence that says it: a safe place
to watch the Agent Factory work on small issues.
