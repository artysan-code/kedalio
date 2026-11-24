# CI and releases on GitHub

The workflows are in `.github/workflows/`. The gate is `scripts/ci.sh`; a new check goes there, not in a workflow.
Actions are pinned by commit SHA (the version in a comment) and Dependabot (`.github/dependabot.yml`, weekly) moves
both, together with the Go modules.

## Workflows

| File          | Runs on                                              | Does                                                                                                          |
| ------------- | ---------------------------------------------------- | ------------------------------------------------------------------------------------------------------------- |
| `ci.yml`      | every push, every pull request                       | the full gate (`scripts/ci.sh`); on `dev`, after it passes, calls `image.yml` then `deploy.yml`               |
| `image.yml`   | called by `ci.yml` on `dev`; a push of a tag `v*`    | builds `core/image/Dockerfile` for linux/amd64 and linux/arm64, pushes to `ghcr.io/artysan-code/kedalio-core` |
| `deploy.yml`  | called by `ci.yml` after the image; manual, on `dev` | asks a deployment (Coolify) to redeploy; does nothing (and succeeds) without the two secrets                  |
| `release.yml` | a push of a tag `v*`                                 | tests the tag, builds the binaries, publishes a GitHub Release                                                |

- **ci**: one job on `ubuntu-latest`, Go from `go.mod`, Deno 2.9.7, shellcheck and gitleaks as sha256-checked release
  tarballs. One run per ref, a newer push cancels the older.
- **image**: tags `:dev` and `:sha-<short>` for `dev`; `:X.Y.Z`, `:X.Y` and `:latest` for a tag `vX.Y.Z` (a prerelease
  tag, `vX.Y.Z-rc.1`, gets only its own). Labels: source (the repository's URL), revision, version, license
  (AGPL-3.0-only), title. `kedalio-core -version` prints the same version. The first push creates the package
  private: make it public once (Packages › kedalio-core › Package settings › Change visibility), or Coolify needs a
  registry login.
- **release**: `kedalio-core` and `kedalio-restore` for linux, darwin and windows on amd64 and arm64
  (`kedalio-<os>-<arch>.tar.gz`, `.zip` for windows, each with `LICENSE` and `README.md`; the names carry no version),
  plus `compose.yaml` with its image pinned to the tag, `install.md` (`docs/install.md`), `get.sh` (the one-line
  installer) and `SHA256SUMS`.
  A tag fails when `.claude-plugin/marketplace.json` exists and a version in it differs from the tag.
  `kedalio-recall-eval` is a development tool and is not shipped. The notes are the tag's section of `CHANGELOG.md`
  (the heading starts with the version, `## 1.2.0 …` or `## [1.2.0] …`), else GitHub's generated notes. A tag with a
  `-` in it is a prerelease. To release: `git tag v1.2.0 && git push origin v1.2.0` on a green commit.

## Secrets

None are required. Without them a fork builds, tests and pushes its own image with the job's `GITHUB_TOKEN`. `deploy.yml`
uses two optional repository secrets (a self-hoster's redeploy hook); without them it does nothing and succeeds.
Secrets are read from the environment and are never printed.
