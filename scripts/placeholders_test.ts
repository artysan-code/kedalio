import { assertEquals } from "@std/assert";
import { inScope, placeholdersIn } from "./placeholders.ts";

Deno.test("flags an unfilled owner placeholder", () => {
  assertEquals(placeholdersIn("image: ghcr.io/<owner>/kedalio-core:1.0.0"), ["<owner>"]);
});

Deno.test("flags the usual shapes, case-insensitively", () => {
  assertEquals(placeholdersIn("clone github.com/<YOUR-ORG>/kedalio as <username>"), ["<YOUR-ORG>", "<username>"]);
  assertEquals(placeholdersIn("token: REPLACE_ME"), ["REPLACE_ME"]);
});

Deno.test("leaves the placeholders that are part of the syntax", () => {
  assertEquals(placeholdersIn("kedalio-<os>-<arch>.tar.gz, v<X.Y.Z>, uuid=<uuid>"), []);
});

Deno.test("a line can keep one on purpose", () => {
  assertEquals(placeholdersIn("<owner> placeholder-ok"), []);
});

Deno.test("covers README.md, compose.yaml and docs/ only", () => {
  assertEquals(["README.md", "compose.yaml", "docs/ci.md", "docs/adr/0001-x.md"].map(inScope), [
    true,
    true,
    true,
    true,
  ]);
  assertEquals(["core/x.go", "scripts/placeholders.ts", "CHANGELOG.md"].map(inScope), [false, false, false]);
});
