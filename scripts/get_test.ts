import { assert, assertEquals, assertStringIncludes } from "@std/assert";

// get.sh against a fake release served on localhost: the archive holds two dummy scripts, never a real binary.
const script = new URL("../get.sh", import.meta.url).pathname;
const decoder = new TextDecoder();

async function run(cmd: string, args: string[], cwd?: string): Promise<void> {
  const out = await new Deno.Command(cmd, { args, cwd, stderr: "piped" }).output();
  if (!out.success) throw new Error(decoder.decode(out.stderr));
}

async function sha256(file: string): Promise<string> {
  const digest = await crypto.subtle.digest("SHA-256", await Deno.readFile(file));
  return [...new Uint8Array(digest)].map((b) => b.toString(16).padStart(2, "0")).join("");
}

async function exists(path: string): Promise<boolean> {
  try {
    await Deno.stat(path);
    return true;
  } catch {
    return false;
  }
}

const compose =
  "services:\n  core:\n    environment:\n      # - KEDALIO_CORE_PUBLIC_URL=${KEDALIO_CORE_PUBLIC_URL:-}\n";
const linux = { KEDALIO_TEST_UNAME_S: "Linux", KEDALIO_TEST_UNAME_M: "x86_64" };

interface Fixture {
  home: string;
  bin: string;
  release: string;
  /** Runs get.sh with a clean environment: no claude on PATH unless `path` adds a folder that has one. */
  get(args?: string[], env?: Record<string, string>, path?: string): Promise<{ code: number; out: string }>;
  close(): Promise<void>;
}

async function fixture(): Promise<Fixture> {
  const root = await Deno.makeTempDir({ prefix: "kedalio-get-" });
  const home = `${root}/home`;
  const release = `${root}/release`;
  const stage = `${root}/stage/kedalio-linux-amd64`;
  await Deno.mkdir(home);
  await Deno.mkdir(stage, { recursive: true });
  await Deno.mkdir(release);
  for (const name of ["kedalio-core", "kedalio-restore"]) {
    await Deno.writeTextFile(`${stage}/${name}`, `#!/bin/sh\necho "${name} 9.9.9"\n`);
    await Deno.chmod(`${stage}/${name}`, 0o755);
  }
  await run("tar", ["-czf", `${release}/kedalio-linux-amd64.tar.gz`, "kedalio-linux-amd64"], `${root}/stage`);
  await Deno.writeTextFile(`${release}/compose.yaml`, compose);
  const sums = [];
  for (const name of ["kedalio-linux-amd64.tar.gz", "compose.yaml"]) {
    sums.push(`${await sha256(`${release}/${name}`)}  ${name}`);
  }
  await Deno.writeTextFile(`${release}/SHA256SUMS`, sums.join("\n") + "\n");

  const server = Deno.serve({ hostname: "127.0.0.1", port: 0, onListen() {} }, async (req) => {
    const name = new URL(req.url).pathname.replace(/^\/+/, "");
    if (!/^[\w.-]+$/.test(name)) return new Response("not found", { status: 404 });
    try {
      return new Response(await Deno.readFile(`${release}/${name}`));
    } catch {
      return new Response("not found", { status: 404 });
    }
  });
  const base = `http://127.0.0.1:${server.addr.port}`;
  const bin = `${root}/bin`;

  return {
    home,
    bin,
    release,
    async get(args = [], env = {}, path = "") {
      const out = await new Deno.Command("sh", {
        args: [script, ...args],
        clearEnv: true,
        env: {
          PATH: `${path ? path + ":" : ""}/usr/bin:/bin`,
          HOME: home,
          TMPDIR: root,
          KEDALIO_BASE_URL: base,
          KEDALIO_BIN_DIR: bin,
          KEDALIO_DIR: `${home}/kedalio`,
          ...env,
        },
        stdin: "null",
        stdout: "piped",
        stderr: "piped",
      }).output();
      return { code: out.code, out: decoder.decode(out.stdout) + decoder.decode(out.stderr) };
    },
    async close() {
      await server.shutdown();
      await Deno.remove(root, { recursive: true });
    },
  };
}

/** A folder with a `claude` that only records its arguments. */
async function fakeClaude(): Promise<{ dir: string; log: string }> {
  const dir = await Deno.makeTempDir({ prefix: "kedalio-fake-claude-" });
  const log = `${dir}/args.log`;
  await Deno.writeTextFile(`${dir}/claude`, `#!/bin/sh\necho "$*" >>"${log}"\n`);
  await Deno.chmod(`${dir}/claude`, 0o755);
  return { dir, log };
}

Deno.test("get.sh installs both binaries, prints the version and warns about PATH", async () => {
  const f = await fixture();
  try {
    const r = await f.get(["--no-plugin"], linux);
    assertEquals(r.code, 0, r.out);
    assertStringIncludes(r.out, "kedalio-core 9.9.9");
    assertStringIncludes(r.out, "is not on your PATH");
    assertEquals((await Deno.stat(`${f.bin}/kedalio-core`)).mode! & 0o111, 0o111);
    assert(await exists(`${f.bin}/kedalio-restore`));
  } finally {
    await f.close();
  }
});

Deno.test("get.sh refuses a tampered SHA256SUMS and installs nothing", async () => {
  const f = await fixture();
  try {
    const sums = await Deno.readTextFile(`${f.release}/SHA256SUMS`);
    await Deno.writeTextFile(`${f.release}/SHA256SUMS`, sums.replace(/^./, (c) => (c === "0" ? "1" : "0")));
    const r = await f.get(["--no-plugin"], linux);
    assert(r.code !== 0);
    assertStringIncludes(r.out, "does not match");
    assert(!await exists(`${f.bin}/kedalio-core`));
  } finally {
    await f.close();
  }
});

Deno.test("get.sh refuses when SHA256SUMS has no line for the archive", async () => {
  const f = await fixture();
  try {
    await Deno.writeTextFile(`${f.release}/SHA256SUMS`, "abc  other.tar.gz\n");
    const r = await f.get([], linux);
    assert(r.code !== 0);
    assertStringIncludes(r.out, "no line for kedalio-linux-amd64.tar.gz");
  } finally {
    await f.close();
  }
});

Deno.test("get.sh says clearly that an unknown architecture has no binary", async () => {
  const f = await fixture();
  try {
    const r = await f.get([], { KEDALIO_TEST_UNAME_S: "Linux", KEDALIO_TEST_UNAME_M: "riscv64" });
    assert(r.code !== 0);
    assertStringIncludes(r.out, "no released binary for riscv64");
    assertStringIncludes(r.out, "Build from source");
  } finally {
    await f.close();
  }
});

Deno.test("get.sh explains that an unpublished release is not there yet", async () => {
  const f = await fixture();
  try {
    // the fake release has only the amd64 archive
    const r = await f.get(["--no-plugin"], { KEDALIO_TEST_UNAME_S: "Linux", KEDALIO_TEST_UNAME_M: "arm64" });
    assert(r.code !== 0);
    assertStringIncludes(r.out, "not published yet");
  } finally {
    await f.close();
  }
});

Deno.test("get.sh --server writes a private .env and the URL in compose.yaml, and refuses a second run", async () => {
  const f = await fixture();
  try {
    const url = "https://kedalio.example.com";
    const r = await f.get(["--server", url]);
    assertEquals(r.code, 0, r.out);
    const dir = `${f.home}/kedalio`;
    assertEquals((await Deno.stat(`${dir}/.env`)).mode! & 0o777, 0o600);
    const env = await Deno.readTextFile(`${dir}/.env`);
    assertStringIncludes(env, `KEDALIO_CORE_PUBLIC_URL=${url}\n`);
    const token = /^KEDALIO_CORE_TOKEN=([0-9a-f]{64})$/m.exec(env)?.[1];
    assert(token, "the token is 64 hex characters");
    assert(!r.out.includes(token), "the token is never printed");
    const yaml = await Deno.readTextFile(`${dir}/compose.yaml`);
    assertStringIncludes(yaml, "      - KEDALIO_CORE_PUBLIC_URL=");
    assert(!yaml.includes("# - KEDALIO_CORE_PUBLIC_URL="));
    assertStringIncludes(r.out, "first-run.txt");

    const again = await f.get(["--server", url]);
    assert(again.code !== 0);
    assertStringIncludes(again.out, "upgrade.md");
    assertEquals(await Deno.readTextFile(`${dir}/.env`), env);
  } finally {
    await f.close();
  }
});

Deno.test("get.sh --uninstall removes the binaries and keeps the data folder", async () => {
  const f = await fixture();
  try {
    assertEquals((await f.get(["--no-plugin"], linux)).code, 0);
    const data = `${f.home}/.local/share/kedalio`;
    await Deno.mkdir(data, { recursive: true });
    await Deno.writeTextFile(`${data}/core.db`, "x");
    const r = await f.get(["--uninstall"], linux);
    assertEquals(r.code, 0, r.out);
    assert(!await exists(`${f.bin}/kedalio-core`));
    assert(!await exists(`${f.bin}/kedalio-restore`));
    assert(await exists(`${data}/core.db`));
    assertStringIncludes(r.out, data);
  } finally {
    await f.close();
  }
});

Deno.test("get.sh runs the plugin commands with the answers from the environment, through a fake claude", async () => {
  const f = await fixture();
  const claude = await fakeClaude();
  try {
    const r = await f.get([], { ...linux, KEDALIO_PLUGIN: "yes", KEDALIO_LANGUAGE: "Italian" }, claude.dir);
    assertEquals(r.code, 0, r.out);
    assertEquals((await Deno.readTextFile(claude.log)).trim().split("\n"), [
      "plugin marketplace add artysan-code/kedalio",
      `plugin install kedalio-local@kedalio --config core_path=${f.bin}/kedalio-core --config language=Italian`,
    ]);
  } finally {
    await Deno.remove(claude.dir, { recursive: true });
    await f.close();
  }
});

Deno.test("get.sh with KEDALIO_PLUGIN=no prints the plugin commands and does not call claude", async () => {
  const f = await fixture();
  const claude = await fakeClaude();
  try {
    const r = await f.get([], { ...linux, KEDALIO_PLUGIN: "no" }, claude.dir);
    assertEquals(r.code, 0, r.out);
    assert(!await exists(claude.log));
    assertStringIncludes(r.out, "claude plugin install kedalio-local@kedalio");
  } finally {
    await Deno.remove(claude.dir, { recursive: true });
    await f.close();
  }
});

Deno.test("get.sh --server refuses a compose.yaml that does not match SHA256SUMS", async () => {
  const f = await fixture();
  try {
    await Deno.writeTextFile(`${f.release}/compose.yaml`, compose + "      - EVIL=1\n");
    const r = await f.get(["--server", "https://kedalio.example.com"]);
    assert(r.code !== 0);
    assertStringIncludes(r.out, "does not match");
    assert(!await exists(`${f.home}/kedalio/compose.yaml`));
    assert(!await exists(`${f.home}/kedalio/.env`));
  } finally {
    await f.close();
  }
});

Deno.test("get.sh --server treats a dangling .env symlink as existing", async () => {
  const f = await fixture();
  try {
    const dir = `${f.home}/kedalio`;
    await Deno.mkdir(dir);
    await Deno.symlink(`${f.home}/nowhere`, `${dir}/.env`);
    const r = await f.get(["--server", "https://kedalio.example.com"]);
    assert(r.code !== 0);
    assertStringIncludes(r.out, "upgrade.md");
    assert(!await exists(`${f.home}/nowhere`));
    assert(!await exists(`${dir}/compose.yaml`));
  } finally {
    await f.close();
  }
});

Deno.test("a truncated get.sh piped to sh installs nothing and fails", async () => {
  const f = await fixture();
  try {
    const text = await Deno.readTextFile(script);
    const half = text.slice(0, Math.floor(text.length / 2));
    const child = new Deno.Command("sh", {
      args: [],
      clearEnv: true,
      env: { PATH: "/usr/bin:/bin", HOME: f.home, TMPDIR: f.home, KEDALIO_BIN_DIR: f.bin, ...linux },
      stdin: "piped",
      stdout: "piped",
      stderr: "piped",
    }).spawn();
    const writer = child.stdin.getWriter();
    await writer.write(new TextEncoder().encode(half));
    await writer.close();
    const out = await child.output();
    assert(!out.success);
    assert(!await exists(`${f.bin}/kedalio-core`));
  } finally {
    await f.close();
  }
});
