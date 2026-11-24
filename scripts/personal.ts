/**
 * personal.ts — nothing personal in the repository (AGENTS.md): no home path of a real user, no e-mail address that
 * could be someone's. Examples use `alice`, `example.com` and the like; whatever belongs to one installation is data
 * on its server.
 *
 *   deno run --allow-read --allow-run=git scripts/personal.ts            the added lines of the staged changes (pre-commit)
 *   deno run --allow-read --allow-run=git scripts/personal.ts --tree     every tracked text file (ci.sh)
 *
 * A line that must carry one on purpose says `personal-ok` on the same line.
 */

/** Names the repository uses as examples or as the container's own user. */
const HOME_OK = new Set([
  "a",
  "ann",
  "alice",
  "bob",
  "you",
  "tu",
  "user",
  "name",
  "me",
  "kedalio",
  "runner",
  "node",
  "alicea",
  "someone",
]);
/** Domains that are nobody's: reserved for examples, or ours, or a placeholder by shape. */
const DOMAIN_OK =
  /(^|\.)(example\.(com|org|net)|invalid|test|localhost|local|ingest\.sentry\.io|anthropic\.com|openai\.com)$/i;

/** Files that hold personal-looking strings on purpose: this check's own test. */
const SKIP_FILES = new Set(["scripts/personal_test.ts"]);

const HOME = /(?<![\w.$}~-])\/home\/([a-z][a-z0-9_-]*)/g;
// the last label is letters: `codex@1.2.3` is a pinned version, not an address
const EMAIL = /[A-Za-z0-9._%+-]+@([A-Za-z0-9-]+(?:\.[A-Za-z0-9-]+)*\.[A-Za-z]{2,})\b/g;

/** What is personal on one line, as short descriptions; empty when nothing is. */
export function personalIn(line: string): string[] {
  if (line.includes("personal-ok")) return [];
  const found: string[] = [];
  for (const m of line.matchAll(HOME)) if (!HOME_OK.has(m[1])) found.push(`home path /home/${m[1]}`);
  for (const m of line.matchAll(EMAIL)) if (!DOMAIN_OK.test(m[1])) found.push(`e-mail address @${m[1]}`);
  return found;
}

async function git(...args: string[]): Promise<string> {
  const out = await new Deno.Command("git", { args, stdout: "piped", stderr: "piped" }).output();
  if (!out.success) throw new Error(`git ${args.join(" ")}: ${new TextDecoder().decode(out.stderr).trim()}`);
  return new TextDecoder().decode(out.stdout);
}

/** `file:line: text` for the lines of a diff that are added. */
export function addedLines(diff: string): { file: string; text: string }[] {
  const out: { file: string; text: string }[] = [];
  let file = "";
  for (const l of diff.split("\n")) {
    if (l.startsWith("+++ b/")) file = l.slice(6);
    else if (l.startsWith("+") && !l.startsWith("+++")) out.push({ file, text: l.slice(1) });
  }
  return out;
}

if (import.meta.main) {
  let lines: { file: string; text: string }[];
  if (Deno.args.includes("--tree")) {
    lines = [];
    for (
      const f of (await git("ls-files", "-z", "--", ":!:*.lock", ":!:*lock.yaml", ":!:*.svg", ":!:*.png")).split("\0")
    ) {
      if (!f) continue;
      const text = await Deno.readTextFile(f).catch(() => "");
      if (text.includes("\0")) continue;
      for (const t of text.split("\n")) lines.push({ file: f, text: t });
    }
  } else {
    lines = addedLines(await git("diff", "--cached", "-U0", "--diff-filter=ACMR", "--no-color"));
  }
  let bad = 0;
  for (const { file, text } of lines) {
    if (SKIP_FILES.has(file)) continue;
    for (const what of personalIn(text)) {
      bad++;
      console.error(`${file}: ${what}: ${text.trim().slice(0, 100)}`);
    }
  }
  if (bad) {
    console.error(
      "\nNothing personal in the repository: use alice / example.com, or `personal-ok` on a line that must.",
    );
  }
  Deno.exit(bad ? 1 : 0);
}
