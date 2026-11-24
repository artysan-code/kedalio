#!/usr/bin/env bash
# deploy.sh — ask Coolify to redeploy the commit whose image was just built (the «deploy» job of ci.yml and of
# deploy.yml run this one body). Env: DEPLOY_URL, COOLIFY_TOKEN and the runner's GITHUB_*; prints «state=…» lines to
# $GITHUB_OUTPUT.
set -euo pipefail

if [[ -z "$DEPLOY_URL" || -z "$COOLIFY_TOKEN" ]]; then
  echo "no Coolify deploy secrets: nothing deployed"; echo "state=no-secrets" >>"$GITHUB_OUTPUT"; exit 0
fi
head=$(git ls-remote origin refs/heads/dev | cut -f1)
if [[ "$head" != "$GITHUB_SHA" ]]; then
  echo "dev moved on to ${head:0:7}: its own run deploys it"; echo "state=superseded" >>"$GITHUB_OUTPUT"; exit 0
fi
# Coolify pulls :dev, so :dev must be the image of THIS commit's inputs (the tag :inputs-<key> of images.yml's «plan»,
# pushed in the same build as :dev): the same digest. A revision label would be wrong when the inputs did not change
# and nothing was rebuilt. A mismatch fails loudly instead of deploying another commit's image (2026-11-23).
# Coolify's image is a variable of its own (core.yaml), so a per-commit tag is not cheap there; this check is.
registry_org="${REGISTRY_ORG:-kedalio}"
core_key=$(git rev-parse HEAD:core HEAD:protocol HEAD:go.mod HEAD:go.sum | sha256sum | cut -c1-32)
token=$(curl -fsS "$GITHUB_SERVER_URL/v2/token?scope=repository:$registry_org/kedalio-core:pull" |
  sed -n 's/.*"token" *: *"\([^"]*\)".*/\1/p') || token=""
manifest_digest() {
  curl -fsSI -H "Authorization: Bearer $token" \
    -H 'Accept: application/vnd.oci.image.index.v1+json, application/vnd.oci.image.manifest.v1+json, application/vnd.docker.distribution.manifest.v2+json, application/vnd.docker.distribution.manifest.list.v2+json' \
    "$GITHUB_SERVER_URL/v2/$registry_org/kedalio-core/manifests/$1" | tr -d '\r' | sed -n 's/^[Dd]ocker-[Cc]ontent-[Dd]igest: *//p'
}
dev_digest=$(manifest_digest dev) || dev_digest=""
inputs_digest=$(manifest_digest "inputs-$core_key") || inputs_digest=""
if [[ -z "$dev_digest" || "$dev_digest" != "$inputs_digest" ]]; then
  echo "::error::kedalio-core:dev (${dev_digest:-unreadable}) is not the image of ${GITHUB_SHA:0:7}'s inputs (${inputs_digest:-unreadable}): not deploying"
  echo "state=image-mismatch" >>"$GITHUB_OUTPUT"; exit 1
fi
curl -fsS -X POST -H "Authorization: Bearer $COOLIFY_TOKEN" "$DEPLOY_URL" >/dev/null
echo "deploy of ${GITHUB_SHA:0:7} requested"
echo "state=requested" >>"$GITHUB_OUTPUT"
