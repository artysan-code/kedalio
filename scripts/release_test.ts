import { assertEquals, assertThrows } from "@std/assert";
import { compareVersions, lastRelease, localDate, prepareChangelog, setPluginVersions } from "./release.ts";

const changelog = `# Changelog

## Unreleased

### Fixed

- A thing.

## 1.0.0 (2026-11-24)

The first release.

### Added

- Everything.
`;

Deno.test("compares versions numerically, not as text", () => {
  assertEquals(compareVersions("1.10.0", "1.9.0"), 1);
  assertEquals(compareVersions("1.0.0", "1.0.0"), 0);
  assertEquals(compareVersions("0.9.9", "1.0.0"), -1);
});

Deno.test("finds the last release in the changelog, skipping Unreleased", () => {
  assertEquals(lastRelease(changelog), "1.0.0");
  assertEquals(lastRelease("# Changelog\n\n## Unreleased\n"), undefined);
});

Deno.test("turns Unreleased into the release and opens a fresh Unreleased above", () => {
  const out = prepareChangelog(changelog, "1.0.1", "2026-12-01");
  assertEquals(out, changelog.replace("## Unreleased\n", "## Unreleased\n\n## 1.0.1 (2026-12-01)\n"));
});

Deno.test("refuses a version that is not greater than the last release", () => {
  assertThrows(() => prepareChangelog(changelog, "1.0.0", "2026-12-01"), Error, "not greater");
  assertThrows(() => prepareChangelog(changelog, "0.9.0", "2026-12-01"), Error, "not greater");
});

Deno.test("refuses a version that is not X.Y.Z", () => {
  assertThrows(() => prepareChangelog(changelog, "v1.1.0", "2026-12-01"), Error, "X.Y.Z");
  assertThrows(() => prepareChangelog(changelog, "1.1", "2026-12-01"), Error, "X.Y.Z");
});

Deno.test("refuses an empty Unreleased", () => {
  const empty = changelog.replace(/## Unreleased\n[\s\S]*?(?=## 1\.0\.0)/, "## Unreleased\n\n");
  assertThrows(() => prepareChangelog(empty, "1.1.0", "2026-12-01"), Error, "empty");
});

Deno.test("refuses an Unreleased that has only empty subheadings", () => {
  const bare = changelog.replace("- A thing.\n", "");
  assertThrows(() => prepareChangelog(bare, "1.1.0", "2026-12-01"), Error, "empty");
});

Deno.test("dates a release with the local calendar day, not UTC's", () => {
  assertEquals(localDate(new Date(2026, 11, 1, 0, 30)), "2026-12-01");
  assertEquals(localDate(new Date(2026, 0, 9, 23, 59)), "2026-01-09");
});

Deno.test("refuses a changelog with no Unreleased section", () => {
  const none = "# Changelog\n\n## 1.0.0 (2026-11-24)\n\n- x\n";
  assertThrows(() => prepareChangelog(none, "1.1.0", "2026-12-01"), Error, "Unreleased");
});

Deno.test("sets every version and keeps the rest of the file as it is", () => {
  const json =
    `{\n  "name": "k",\n  "plugins": [\n    { "name": "a", "version": "1.0.0" },\n    {\n      "name": "b",\n      "version": "1.0.0"\n    }\n  ]\n}\n`;
  assertEquals(setPluginVersions(json, "1.1.0"), json.replaceAll("1.0.0", "1.1.0"));
});

Deno.test("refuses a plugin file with no version", () => {
  assertThrows(() => setPluginVersions(`{"name":"x"}`, "1.1.0"), Error, "no version");
});
