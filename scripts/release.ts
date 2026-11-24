/**
 * release.ts — prepares a release: turns CHANGELOG.md's «## Unreleased» into «## X.Y.Z (today)», opens a fresh empty
 * Unreleased above it and sets the version of every plugin in .claude-plugin/marketplace.json (and of a plugin.json,
 * where one exists). It only edits files: committing, tagging and pushing the tag are the owner's, and it prints them.
 *
 *   deno run --allow-read --allow-write scripts/release.ts X.Y.Z
 *
 * X.Y.Z is SemVer: X breaks users, Y adds, Z fixes. It must be greater than the last release in the changelog.
 */

const VERSION = /^(\d+)\.(\d+)\.(\d+)$/;
const RELEASE_HEADING = /^## \[?(\d+\.\d+\.\d+)/m;

/** -1, 0 or 1 as `a` is lower than, equal to or greater than `b`; both are X.Y.Z. */
export function compareVersions(a: string, b: string): number {
  const x = a.split(".").map(Number);
  const y = b.split(".").map(Number);
  for (let i = 0; i < 3; i++) if (x[i] !== y[i]) return x[i] < y[i] ? -1 : 1;
  return 0;
}

/** The newest release heading's version (the first one, the changelog is newest first); undefined when none. */
export function lastRelease(changelog: string): string | undefined {
  return RELEASE_HEADING.exec(changelog)?.[1];
}

/** The changelog with Unreleased turned into the release `version` dated `date`, and a new empty Unreleased on top. */
export function prepareChangelog(changelog: string, version: string, date: string): string {
  if (!VERSION.test(version)) throw new Error(`${version} is not a version: write X.Y.Z, without a v`);
  const last = lastRelease(changelog);
  if (last !== undefined && compareVersions(version, last) <= 0) {
    throw new Error(`${version} is not greater than the last release, ${last}`);
  }
  const heading = /^## Unreleased[ \t]*\n/m.exec(changelog);
  if (!heading) throw new Error("CHANGELOG.md has no «## Unreleased» section");
  const rest = changelog.slice(heading.index + heading[0].length);
  const next = rest.search(/^## /m);
  const body = next === -1 ? rest : rest.slice(0, next);
  if (body.trim() === "") throw new Error("the Unreleased section is empty: nothing to release");
  return changelog.slice(0, heading.index + heading[0].length) + `\n## ${version} (${date})\n` + rest;
}

/** The JSON text with every "version" value set to `version`, the formatting untouched. */
export function setPluginVersions(json: string, version: string): string {
  const field = /("version"\s*:\s*")[^"]*(")/g;
  if (!field.test(json)) throw new Error("the file has no version to set");
  field.lastIndex = 0;
  return json.replace(field, `$1${version}$2`);
}

async function exists(path: string): Promise<boolean> {
  try {
    await Deno.stat(path);
    return true;
  } catch (e) {
    if (e instanceof Deno.errors.NotFound) return false;
    throw e;
  }
}

if (import.meta.main) {
  const version = Deno.args[0] ?? "";
  try {
    const root = new URL("..", import.meta.url).pathname;
    const today = new Date().toISOString().slice(0, 10);
    // all computed before the first write, so a refusal leaves every file as it was
    const edits = new Map<string, string>();
    edits.set(`${root}CHANGELOG.md`, prepareChangelog(await Deno.readTextFile(`${root}CHANGELOG.md`), version, today));
    const plugins = [
      ".claude-plugin/marketplace.json",
      ".claude-plugin/plugin.json",
      "plugin/.claude-plugin/plugin.json",
    ];
    for (const file of plugins) {
      if (await exists(root + file)) {
        edits.set(root + file, setPluginVersions(await Deno.readTextFile(root + file), version));
      }
    }
    for (const [file, text] of edits) await Deno.writeTextFile(file, text);
    console.log(`Prepared ${version} (${today}): CHANGELOG.md and the plugin versions are edited, nothing is committed.

Review the diff, then:
  git commit -am "chore(release): ${version}"
  # once it is on dev and CI is green, the owner tags it:
  git tag v${version} && git push origin v${version}`);
  } catch (e) {
    console.error(`release: ${e instanceof Error ? e.message : e}`);
    Deno.exit(1);
  }
}
