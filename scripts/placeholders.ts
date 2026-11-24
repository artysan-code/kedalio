/**
 * placeholders.ts — what a self-hoster reads must not carry a placeholder nobody filled in: `ghcr.io/<owner>/...` in
 * compose.yaml or the README is a command that fails (or pulls someone else's image). Looks at README.md, compose.yaml
 * and docs/ (tracked files), for the placeholders written as <owner>, <org>, <user>, <username>, <github-user>,
 * <your-...>, and REPLACE_ME. Syntax placeholders (<os>, <arch>, <version>, <uuid>) are fine.
 *
 *   deno run --allow-read --allow-run=git scripts/placeholders.ts
 *
 * A line that must carry one on purpose says `placeholder-ok` on the same line.
 */

const PLACEHOLDER = /<(?:owner|org|user|username|github-user|your-[\w-]+)>|REPLACE_ME/gi;

/** The unfilled placeholders on one line; empty when none, or when the line says `placeholder-ok`. */
export function placeholdersIn(line: string): string[] {
  if (line.includes("placeholder-ok")) return [];
  return [...line.matchAll(PLACEHOLDER)].map((m) => m[0]);
}

/** Whether a tracked file is one a self-hoster reads. */
export function inScope(file: string): boolean {
  return file === "README.md" || file === "compose.yaml" || file.startsWith("docs/");
}

if (import.meta.main) {
  const out = await new Deno.Command("git", { args: ["ls-files", "-z"], stdout: "piped", stderr: "inherit" }).output();
  if (!out.success) Deno.exit(1);
  let bad = 0;
  for (const f of new TextDecoder().decode(out.stdout).split("\0")) {
    if (!f || !inScope(f)) continue;
    const text = await Deno.readTextFile(f).catch(() => "");
    text.split("\n").forEach((line, i) => {
      for (const p of placeholdersIn(line)) {
        bad++;
        console.error(`${f}:${i + 1}: unfilled placeholder ${p}: ${line.trim().slice(0, 100)}`);
      }
    });
  }
  if (bad) console.error("\nFill it in (artysan-code, example.com, alice), or `placeholder-ok` on a line that must.");
  Deno.exit(bad ? 1 : 0);
}
