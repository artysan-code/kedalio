# The nightly upkeep's container: Claude Code at a fixed version, jq, and the runner. Coolify builds it on the host from
# deploy/coolify/core.yaml (service `upkeep`); it idles, and a Coolify scheduled task runs /upkeep/run.sh in it at
# night. It reaches the core only on the compose network and signs in to Claude with CLAUDE_CODE_OAUTH_TOKEN (made once
# with `claude setup-token`, kept in Coolify as a secret).
FROM node:22-bookworm-slim@sha256:5ec23b7c622b00547753bc6ffd78085dd868a68aeb41c8e0a1418f033e13d96d

RUN apt-get update \
  && apt-get install -y --no-install-recommends jq ca-certificates \
  && rm -rf /var/lib/apt/lists/* \
  && npm install -g @anthropic-ai/claude-code@2.1.322 \
  && npm cache clean --force

COPY run.sh nightly.md mcp.json /upkeep/

USER node
WORKDIR /home/node
ENV DISABLE_AUTOUPDATER=1
CMD ["sleep", "infinity"]
