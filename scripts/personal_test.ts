import { assertEquals } from "@std/assert";
import { addedLines, personalIn } from "./personal.ts";

Deno.test("example home paths and addresses pass", () => {
  for (
    const l of [
      'cwd: "/home/ann/site"',
      "WORKDIR /home/kedalio",
      "git config user.email ci@kedalio.invalid",
      "write to alice@example.com",
      "a placeholder /home/<name>/x",
      "SENTRY=https://abc@o1.ingest.sentry.io/1",
    ]
  ) assertEquals(personalIn(l), [], l);
});

Deno.test("a real home path or address is found", () => {
  assertEquals(personalIn("cd /home/bruno/work"), ["home path /home/bruno"]);
  assertEquals(personalIn("mail bruno@gmail.com now"), ["e-mail address @gmail.com"]);
  assertEquals(personalIn("both /home/bruno/x and a@b.it").length, 2);
});

Deno.test("personal-ok on the line allows it", () => {
  assertEquals(personalIn("/home/bruno/x // personal-ok"), []);
});

Deno.test("only added lines of a diff are looked at", () => {
  const diff = "+++ b/a.ts\n@@ -1 +1,2 @@\n-/home/old/x\n+/home/new/x\n+fine\n+++ b/b.md\n+z";
  assertEquals(addedLines(diff), [
    { file: "a.ts", text: "/home/new/x" },
    { file: "a.ts", text: "fine" },
    { file: "b.md", text: "z" },
  ]);
});

Deno.test("a pinned version and an API path are not personal", () => {
  assertEquals(personalIn("install codex@1.2.3 from /api/engines/home/pieces"), []);
});
