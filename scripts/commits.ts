/**
 * commits.ts — commit subjects are Conventional Commits (ported from Agents Multi's release.ts --lint).
 *
 *   deno run --allow-read --allow-run=git scripts/commits.ts              every commit since the last v* tag (CI, pre-push)
 *   deno run --allow-read scripts/commits.ts --subject "<subject>"         one subject (the commit-msg hook)
 *   deno run --allow-read scripts/commits.ts --message-file <path>         one whole message: subject and no attribution (commit-msg)
 */

/** The commit types a subject may start with. */
export const TYPES = [
  "feat",
  "fix",
  "perf",
  "refactor",
  "security",
  "docs",
  "test",
  "build",
  "ci",
  "chore",
  "style",
  "revert",
];

const HEADER = /^(\w+)(?:\(([^)]+)\))?(!)?: (.+)$/;
/** Subjects git writes itself, accepted as they are. */
const GENERATED = /^(Merge |Revert "|fixup! |squash! |amend! )/;

/** Why a subject is not a valid Conventional Commit, or null when it is. */
export function lintSubject(subject: string): string | null {
  if (GENERATED.test(subject)) return null;
  const m = HEADER.exec(subject);
  if (!m) return "the subject is not <type>(<scope>)!: <description>";
  if (!TYPES.includes(m[1])) return `unknown type "${m[1]}" (${TYPES.join(", ")})`;
  if (m[2] !== undefined && !/^[a-z0-9][a-z0-9/._-]*$/.test(m[2])) return `scope "${m[2]}" is not lowercase`;
  if (subject.length > 100) return `the subject is ${subject.length} characters, at most 100`;
  return null;
}

/** Lines that credit a tool or an assistant; the owner's commits carry no attribution (AGENTS.md, «Commits»). */
const ATTRIBUTION = /^(co-authored-by|generated[- ]by|assisted[- ]by|created[- ]with)\s*:|generated with \[?claude|🤖/i;

/** Why a whole message is refused beyond its subject (an attribution line), or null when it is fine. */
export function lintMessage(message: string): string | null {
  const bad = message.split("\n").find((l) => ATTRIBUTION.test(l.trim()));
  return bad ? `attribution line "${bad.trim().slice(0, 60)}": commits carry no attribution trailers` : null;
}

async function git(...args: string[]): Promise<string> {
  const out = await new Deno.Command("git", { args, stdout: "piped", stderr: "piped" }).output();
  if (!out.success) throw new Error(`git ${args.join(" ")}: ${new TextDecoder().decode(out.stderr).trim()}`);
  return new TextDecoder().decode(out.stdout);
}

async function subjectsSinceLastTag(): Promise<{ hash: string; subject: string }[]> {
  // an empty repository (no commit yet) has nothing to check
  if (!(await git("rev-parse", "--verify", "--quiet", "HEAD").catch(() => ""))) return [];
  const tag = (await git("describe", "--tags", "--abbrev=0", "--match", "v*").catch(() => "")).trim();
  const raw = await git("log", "--no-merges", "--format=%H%x1f%s%x1e", ...(tag ? [`${tag}..HEAD`] : ["HEAD"]));
  return raw.split("\x1e").map((r) => r.trim()).filter(Boolean).map((r) => {
    const [hash, subject] = r.split("\x1f");
    return { hash, subject };
  });
}

if (import.meta.main) {
  const mf = Deno.args.indexOf("--message-file");
  if (mf >= 0) {
    const message = (await Deno.readTextFile(Deno.args[mf + 1])).split("\n").filter((l) => !l.startsWith("#")).join(
      "\n",
    );
    const why = lintSubject(message.split("\n")[0]) ?? lintMessage(message);
    if (why) console.error(why);
    Deno.exit(why ? 1 : 0);
  }
  const at = Deno.args.indexOf("--subject");
  const commits = at >= 0 ? [{ hash: "", subject: Deno.args[at + 1] ?? "" }] : await subjectsSinceLastTag();
  let bad = 0;
  for (const c of commits) {
    const why = lintSubject(c.subject);
    if (!why) continue;
    bad++;
    console.error(`${c.hash ? `${c.hash.slice(0, 7)} ` : ""}${c.subject}\n  ${why}`);
  }
  if (bad) console.error("\nWrite subjects as <type>(<scope>)!: <description>, e.g. feat(server): health endpoint");
  Deno.exit(bad ? 1 : 0);
}
