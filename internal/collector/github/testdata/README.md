# GitHub collector fixtures

Response bodies replayed by `httptest` servers in the collector tests. Headers
(`Link`, rate-limit headers) are not stored: the tests set them, so each test
says exactly which header it depends on.

## Recorded

`pulls_page1.json` and `pulls_page2.json` are real responses from the public
repository `alloteyphil/wasitus`, recorded on 26 Nov 2026 with the personal
account:

```
gh api 'repos/alloteyphil/wasitus/pulls?state=closed&sort=updated&direction=desc&per_page=3'
gh api 'repos/alloteyphil/wasitus/pulls?state=closed&sort=updated&direction=desc&per_page=3&page=2'
```

`per_page=3` splits the six merged pull requests over two pages, so the
pagination test replays real pages. GitHub's `Link` header for page 1 pointed
at `/repositories/<id>/pulls?...&page=2`, not at `/repos/<owner>/<name>/...`;
the tests copy that shape.

Scrubbed before committing, with `jq`:

```
jq '[.[] | del(.body)
     | .head.repo = (.head.repo | {id, name, full_name, private, html_url})
     | .base.repo = (.base.repo | {id, name, full_name, private, html_url})]'
```

- `body` (free text) is removed.
- The `head.repo` and `base.repo` objects are cut down to a few public fields
  to keep the files small; the collector does not read them.

Nothing else was changed. The files hold no tokens, emails or private data:
only public pull request metadata.

## Hand-written

The repository has no deployments or releases yet, and every closed pull
request in it was merged. These files are written by hand from the response
shapes in GitHub's REST API documentation (API version 2022-11-28), with fake
IDs, the fake `lab-bot` user and demo-lab names:

| File | What it covers |
|---|---|
| `pulls_mixed.json` | a pull request closed without merging, one merged before the window, a `docs:` title and a null `user` and `merge_commit_sha` |
| `deployments.json` | two deployments inside the test window and one older one that stops pagination |
| `deployment_9002_statuses.json` | statuses newest first (the collector reads the first) |
| `releases.json` | a draft (skipped), a published release and an older pre-release |

The deployment with ID 9001 has no statuses; the tests serve `[]` for it.

## Refreshing

Re-run the `gh api` commands above with the personal account, apply the same
`jq` scrub, and review the diff before committing. Tests pin their window to
the recorded merge times (26 Nov 2026), so a refresh that adds pull requests
needs the expectations in `github_test.go` updated too.
