---
name: cut-release
description: Use when cutting a new fleet-telemetry release binary for Teslemetry (dispatching release-binary.yml and getting the Teslemetry/servers pin PR opened).
---

# Cut a release

A release is only ever cut by `.github/workflows/release-binary.yml` on `workflow_dispatch`. Never tag or publish a GitHub Release by hand, and never add a `publish.yml`: both skip the gate and the `production` approval.

`gh-axi` resolves this checkout to upstream `teslamotors/fleet-telemetry` by default, so pass `--repo Teslemetry/tesla-fleet-telemetry` on every call below.

1. **Pick the commit.** Use an exact SHA on `main`, not a branch name:
   ```bash
   git fetch origin && git rev-parse origin/main
   gh-axi run list --repo Teslemetry/tesla-fleet-telemetry --workflow build.yml --commit <sha>
   ```
   The `build` run for that SHA must be green.
2. **Pick the version.** Find the latest tag and bump it (`vX.Y.Z`, with the leading `v`):
   ```bash
   gh-axi release list --repo Teslemetry/tesla-fleet-telemetry
   ```
3. **Dispatch.**
   ```bash
   gh-axi workflow run release-binary.yml --repo Teslemetry/tesla-fleet-telemetry --ref main \
     --field version=<vX.Y.Z> --field sha=<sha>
   ```
4. **Production approval.** The job waits on the `production` environment before any step runs. A repo admin approves it in the Actions UI. Do not approve it yourself unless you were explicitly given that authority.
5. **Watch the run.** It re-runs the full `build` gate against the SHA, builds `linux-amd64`, and publishes the Release with the binary, tarball and `.sha256` file.
   ```bash
   gh-axi run list --repo Teslemetry/tesla-fleet-telemetry --workflow release-binary.yml
   gh-axi run watch <run-id> --repo Teslemetry/tesla-fleet-telemetry
   ```
6. **Check the Teslemetry/servers pin PR.**
   - If the `SERVERS_REPO_TOKEN` secret is set, the workflow opens `fleet_telemetry: pin <X.Y.Z>` in `Teslemetry/servers` from branch `fleet-telemetry-pin-<X.Y.Z>`. Confirm it exists: `gh-axi pr list --repo Teslemetry/servers`.
   - If the secret is not set, the run summary says the PR was NOT opened and prints the two values. Open the PR by hand: in `roles/fleet_telemetry/defaults/main.yaml` set `fleet_telemetry_version` to the version **without** the `v`, and `fleet_telemetry_sha256` to the SHA-256 of `fleet-telemetry-linux-amd64` (the binary, not the tarball: the first line of the release's `.sha256` file). In the PR body, link the release and list what changed since the previous release.
7. **Stop there.** The host rollout is a separate step that goes through the servers repo's own deploy window. Cutting the binary does not deploy it.
