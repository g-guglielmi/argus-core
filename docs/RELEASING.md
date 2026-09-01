# Releasing Argus (core)

A release is a git tag `vX.Y.Z` that CI (`.github/workflows/build.yml`) turns into a versioned
image (`ghcr.io/<owner>/argus:vX.Y.Z`), a refreshed `:latest`/`:testing`, and a GitHub Release built
from the matching `CHANGELOG.md` section.

## Checklist

1. Land the feature commits on `main` and confirm CI is **green** on the tip.
2. Add the `## [X.Y.Z]` section to `CHANGELOG.md` (this is the Release body — the `release` job
   fails if it can't find a matching section).
3. Mark any completed `ROADMAP.md` items **in the same commit** as the CHANGELOG cut. Commit both.
4. Push `main`.
5. Tag and push the tag:
   ```bash
   git tag vX.Y.Z
   git push origin vX.Y.Z
   ```
6. **Stop.** Do not push anything else to `main` until the **tag build** has left the queue and
   started running (watch it: `gh run watch <id>` or the Actions tab). See the gotcha below.
7. Confirm the tag build is green (both `docker` and `release` jobs) and the Release published:
   `gh release view vX.Y.Z`.

## The concurrency gotcha (why step 3 & step 6 matter)

`build.yml` serializes image builds with a `build-image` concurrency group and
`cancel-in-progress: false`, so the main-push build and the tag build run in order and the tag build
is the last writer of `:testing` (cleanly stamped `vX.Y.Z`).

But GitHub keeps only **one** *pending* run per concurrency group: if you push another commit to
`main` while the tag build is still queued behind the main-push build, that new run **evicts the
still-pending tag build**, which shows up as `cancelled`. The result is a tag with **no versioned
image and no GitHub Release** — the failure mode seen at v0.4.29.

Two ways to avoid it, both in the checklist above:
- Fold the ROADMAP `[x]` marks into the CHANGELOG-cut commit (step 3) so there's no follow-up push.
- If you must push again, wait until the tag build is *running*, not queued (step 6).

**Recovery** if the tag build was cancelled this way: just re-run it — the tag already points at the
right commit and nothing else is contending.
```bash
gh run rerun <cancelled-tag-run-id>
```
