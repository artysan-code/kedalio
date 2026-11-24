import { assertEquals } from "@std/assert";
import { lintRoot, lintSkill } from "./skills.ts";

const ok = "---\nname: x\ndescription: Does a thing. Use when you do the thing.\n---\n\nBody";

Deno.test("the plugin's skills are linted too", async () => {
  const root = await Deno.makeTempDir();
  try {
    await Deno.mkdir(`${root}/plugin/skills/x`, { recursive: true });
    await Deno.mkdir(`${root}/plugin/skills/empty`, { recursive: true });
    await Deno.writeTextFile(`${root}/plugin/skills/x/SKILL.md`, ok.replace("name: x", "name: y"));
    assertEquals(await lintRoot(root), [
      "plugin/skills/empty: no SKILL.md",
      'plugin/skills/x: name "y" is not the folder "x"',
    ]);
  } finally {
    await Deno.remove(root, { recursive: true });
  }
});

Deno.test("a valid skill passes", () => assertEquals(lintSkill("x", ok), null));

Deno.test("the front matter is checked", () => {
  assertEquals(lintSkill("x", "Body"), "no front matter");
  assertEquals(lintSkill("y", ok), 'name "x" is not the folder "y"');
  assertEquals(lintSkill("x", "---\nname: x\n---\n"), "no description");
  assertEquals(lintSkill("x", "---\nname: x\ndescription: Does a thing.\n---\n")?.includes("Use when"), true);
  assertEquals(
    lintSkill("x", `---\nname: x\ndescription: Use when ${"a".repeat(1100)}\n---\n`)?.includes("at most"),
    true,
  );
});

Deno.test("the symlinks of .claude/skills follow .agents/skills", async () => {
  const root = await Deno.makeTempDir();
  try {
    await Deno.mkdir(`${root}/.agents/skills/x`, { recursive: true });
    await Deno.mkdir(`${root}/.claude/skills`, { recursive: true });
    await Deno.writeTextFile(`${root}/.agents/skills/x/SKILL.md`, ok);
    assertEquals((await lintRoot(root)).length, 1);
    await Deno.symlink("../../.agents/skills/x", `${root}/.claude/skills/x`);
    await Deno.symlink("../../.agents/skills/gone", `${root}/.claude/skills/gone`);
    assertEquals(await lintRoot(root), [".claude/skills/gone has no skill in .agents/skills"]);
  } finally {
    await Deno.remove(root, { recursive: true });
  }
});
