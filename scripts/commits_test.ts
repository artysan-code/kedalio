import { assertEquals } from "@std/assert";
import { lintMessage, lintSubject } from "./commits.ts";

Deno.test("Conventional Commit subjects pass", () => {
  for (
    const s of [
      "feat(server): health endpoint",
      "fix!: drop v0 frames",
      "docs(adr): 0002 the server",
      "Merge branch 'x'",
    ]
  ) {
    assertEquals(lintSubject(s), null, s);
  }
});

Deno.test("anything else is refused with the reason", () => {
  assertEquals(lintSubject("added stuff"), "the subject is not <type>(<scope>)!: <description>");
  assertEquals(lintSubject("feature: x")?.startsWith('unknown type "feature"'), true);
  assertEquals(lintSubject("feat(Server): x"), 'scope "Server" is not lowercase');
  assertEquals(lintSubject(`feat: ${"x".repeat(100)}`), "the subject is 106 characters, at most 100");
});

Deno.test("a message with an attribution line is refused, a plain one passes", () => {
  assertEquals(lintMessage("feat(x): y\n\nWhy this.\n\nSigned-off-by: A <a@example.com>"), null);
  assertEquals(lintMessage("feat(x): y\n\nBody mentioning co-authored work in prose."), null);
  for (
    const t of [
      "Co-Authored-By: Someone <noreply@example.com>",
      "co-authored-by: Codex <c@example.com>",
      "Generated-by: a tool",
      "Assisted-by: a tool",
      "🤖 Generated with [Claude Code](https://claude.com/claude-code)",
    ]
  ) {
    assertEquals(lintMessage(`feat(x): y\n\n${t}`)?.startsWith("attribution line"), true, t);
  }
});
