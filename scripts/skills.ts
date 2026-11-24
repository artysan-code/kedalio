/**
 * skills.ts — the repository's own skills (ADR 0025): `.agents/skills/<name>/SKILL.md` is the folder; Claude Code
 * reads `.claude/skills/`, so each skill has a tracked symlink there. The check keeps the two in step and the front
 * matter honest (name = folder, a description that says when to use it, within the standard's limit).
 *
 *   deno run --allow-read scripts/skills.ts     (ci.sh)
 */

export const MAX_DESCRIPTION = 1024;

/** Why a SKILL.md is not valid for folder `name`, or null. */
export function lintSkill(name: string, text: string): string | null {
  const m = /^---\n([\s\S]*?)\n---\n/.exec(text);
  if (!m) return "no front matter";
  const field = (k: string) => new RegExp(`^${k}:\\s*(.+)$`, "m").exec(m[1])?.[1].trim();
  if (field("name") !== name) return `name "${field("name")}" is not the folder "${name}"`;
  const d = field("description");
  if (!d) return "no description";
  if (d.length > MAX_DESCRIPTION) return `the description is ${d.length} characters, at most ${MAX_DESCRIPTION}`;
  if (!/\buse (when|for|before|after)\b/i.test(d)) return 'the description does not say when ("Use when …")';
  return null;
}

async function* entries(dir: string): AsyncGenerator<Deno.DirEntry> {
  try {
    yield* Deno.readDir(dir);
  } catch (e) {
    if (!(e instanceof Deno.errors.NotFound)) throw e;
  }
}

/** Problems of a repository root: each skill valid, each linked from `.claude/skills`, no link without a skill. */
export async function lintRoot(root: string): Promise<string[]> {
  const bad: string[] = [];
  const names: string[] = [];
  for await (const e of entries(`${root}/.agents/skills`)) {
    if (!e.isDirectory) continue;
    names.push(e.name);
    const text = await Deno.readTextFile(`${root}/.agents/skills/${e.name}/SKILL.md`).catch(() => null);
    const why = text === null ? "no SKILL.md" : lintSkill(e.name, text);
    if (why) bad.push(`.agents/skills/${e.name}: ${why}`);
    const link = await Deno.readLink(`${root}/.claude/skills/${e.name}`).catch(() => null);
    if (link !== `../../.agents/skills/${e.name}`) {
      bad.push(`.claude/skills/${e.name} is not a symlink to ../../.agents/skills/${e.name}`);
    }
  }
  for await (const e of entries(`${root}/.claude/skills`)) {
    if (!names.includes(e.name)) bad.push(`.claude/skills/${e.name} has no skill in .agents/skills`);
  }
  // The public plugin's skills ship to other people's Claude: same front matter, no symlink (the plugin loads the folder).
  for await (const e of entries(`${root}/plugin/skills`)) {
    if (!e.isDirectory) continue;
    const text = await Deno.readTextFile(`${root}/plugin/skills/${e.name}/SKILL.md`).catch(() => null);
    const why = text === null ? "no SKILL.md" : lintSkill(e.name, text);
    if (why) bad.push(`plugin/skills/${e.name}: ${why}`);
  }
  return bad.sort();
}

if (import.meta.main) {
  const bad = await lintRoot(new URL("..", import.meta.url).pathname.replace(/\/$/, ""));
  for (const b of bad) console.error(b);
  Deno.exit(bad.length ? 1 : 0);
}
