# CI and releases on GitHub

The workflows are in `.github/workflows/`. The gate is `scripts/ci.sh`; a new check goes there, not in a workflow.
Actions are pinned by commit SHA (the version in a comment) and Dependabot (`.github/dependabot.yml`, weekly) moves
both, together with the Go modules.

## Workflows

| File          | Runs on                                                      | Does                                                                                                          |
| ------------- | ------------------------------------------------------------ | ------------------------------------------------------------------------------------------------------------- |
| `ci.yml`      | every push, every pull request                               | the full gate (`scripts/ci.sh`); on `development`, after it passes, calls `image.yml` then `deploy.yml`       |
| `image.yml`   | called by `ci.yml` on `development`; a push of a tag `v*`    | builds `core/image/Dockerfile` for linux/amd64 and linux/arm64, pushes to `ghcr.io/artysan-code/kedalio-core` |
| `deploy.yml`  | called by `ci.yml` after the image; manual, on `development` | asks a deployment (Coolify) to redeploy; does nothing (and succeeds) without the two secrets                  |
| `release.yml` | a push of a tag `v*`                                         | tests the tag, builds the binaries, publishes a GitHub Release                                                |

- **ci**: one job on `ubuntu-latest`, Go from `go.mod`, Deno 2.9.7, shellcheck and gitleaks as sha256-checked release
  tarballs. One run per ref, a newer push cancels the older.
- **image**: tags `:development` and `:sha-<short>` for `development`, built as version `<last release>-<commits since>-g<sha>`
  (`git describe`, the `v` stripped, e.g. `1.0.0-2-g27fd5851`, the sha as git abbreviates it; `0.0.0-<sha7>` when no
  release tag is reachable); `:X.Y.Z`, `:X.Y` and `:latest` for a tag `vX.Y.Z` (a prerelease tag, `vX.Y.Z-rc.1`, gets
  only its own). A tag without its non-empty `CHANGELOG.md` section fails `image.yml` before anything is pushed. Labels: source (the repository's URL), revision, version, license
  (AGPL-3.0-or-later), title. `kedalio-core -version` prints the same version. The first push creates the package
  private: make it public once (Packages › kedalio-core › Package settings › Change visibility), or Coolify needs a
  registry login.
- **release**: `kedalio-core` and `kedalio-restore` for linux, darwin and windows on amd64 and arm64
  (`kedalio-<os>-<arch>.tar.gz`, `.zip` for windows, each with `LICENSE` and `README.md`; the names carry no version),
  plus `compose.yaml` with its image pinned to the tag, `install.md` (`docs/install.md`), `get.sh` (the one-line
  installer) and `SHA256SUMS`.
  A tag fails when `.claude-plugin/marketplace.json` exists and a version in it differs from the tag.
  Every asset listed in `SHA256SUMS` is attested (SLSA build provenance, `actions/attest-build-provenance` on `SHA256SUMS`), and so is the image
  by digest (pushed to ghcr.io): `gh attestation verify <file> --repo artysan-code/kedalio`, or
  `gh attestation verify oci://ghcr.io/artysan-code/kedalio-core:<tag> --repo artysan-code/kedalio`.
  The image workflow validates `server.json` on every pushed image and, for a release, publishes it to the MCP
  registry as `io.github.artysan-code/kedalio` (version and image tag from the tag; `mcp-publisher` checked with cosign,
  signed in with the job's OIDC token).
  `kedalio-recall-eval` is a development tool and is not shipped. The notes are the tag's section of `CHANGELOG.md`
  (the heading starts with the version, `## 1.2.0 …` or `## [1.2.0] …`). A tag without one fails the release; only a
  prerelease (a `-` in the tag) falls back to GitHub's generated notes. To release: `deno run --allow-read --allow-write
  scripts/release.ts 1.2.0` (turns `## Unreleased` into `## 1.2.0 (date)`, sets the plugin versions), commit that to
  `development`, then `git tag v1.2.0 && git push origin v1.2.0` on the green commit.

## Secrets

None are required. Without them a fork builds, tests and pushes its own image with the job's `GITHUB_TOKEN`. `deploy.yml`
uses two optional secrets, `COOLIFY_WEBHOOK` and `COOLIFY_TOKEN` (a self-hoster's redeploy hook), kept in the
`development` environment, whose deployment branches are `development` only. A fork gets an empty `development` environment and
`deploy.yml` does nothing and succeeds.
Secrets are read from the environment and are never printed.

## Branches, environments and releases

- `release` is the default branch and always points at the latest release. `development` is development: a green push to it builds
  `:development` and redeploys the maintainer's instance. CI runs the gate on every branch, `release` included, but only `development` builds
  an image or deploys.
- Environments: `development` (used by `deploy.yml`; deployment branches `development` only; the Coolify secrets above) and
  `release` (used by the job of `release.yml` that publishes the GitHub Release; deployment refs: tags `v*` only).
  `image.yml` uses none.
- Release: run `scripts/release.ts X.Y.Z` and commit it, then tag the green `development` commit `vX.Y.Z` and push the tag. When `release.yml` and the tag's image are green,
  fast-forward `release` to it: `git push origin vX.Y.Z^{commit}:refs/heads/release`. No workflow pushes `release`.
