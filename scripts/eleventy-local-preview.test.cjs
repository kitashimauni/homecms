"use strict";

const assert = require("node:assert/strict");
const fs = require("node:fs");
const childProcess = require("node:child_process");
const http = require("node:http");
const net = require("node:net");
const os = require("node:os");
const path = require("node:path");
const test = require("node:test");

const {
  createBuildState,
  createLoopbackServer,
  configureProjectDirectories,
  findOutputFile,
  listen,
  parseArguments,
} = require("./eleventy-local-preview.cjs");

const integrationFixture = path.join(__dirname, "fixtures", "eleventy-programmatic");

function hasRealEleventy() {
  try {
    require.resolve("@11ty/eleventy", { paths: [integrationFixture] });
    return true;
  } catch (_) {
    return false;
  }
}

function createEleventyFixture() {
  const project = fs.mkdtempSync(path.join(integrationFixture, ".tmp-"));
  const input = path.join(project, "src");
  const output = path.join(project, "public");
  fs.cpSync(path.join(integrationFixture, "site"), project, { recursive: true });
  return { project, input, output, article: path.join(input, "posts", "one.md") };
}

function removeEleventyFixture(fixture) {
  fs.rmSync(fixture.project, { recursive: true, force: true });
}

function waitForExit(child) {
  if (child.exitCode !== null) return Promise.resolve();
  return new Promise((resolve) => child.once("exit", resolve));
}

function waitForHTTP(port, pathname) {
  return new Promise((resolve, reject) => {
    const deadline = Date.now() + 15000;
    const attempt = () => {
      const request = http.get({ host: "127.0.0.1", port, path: pathname }, (response) => {
        const chunks = [];
        response.on("data", (chunk) => chunks.push(chunk));
        response.on("end", () => {
          if (response.statusCode === 200) {
            resolve({ statusCode: response.statusCode, body: Buffer.concat(chunks).toString("utf8") });
            return;
          }
          retry();
        });
      });
      request.on("error", retry);
      request.setTimeout(500, () => request.destroy());
    };
    const retry = () => {
      if (Date.now() >= deadline) {
        reject(new Error(`Timed out waiting for Eleventy preview at ${pathname}`));
        return;
      }
      setTimeout(attempt, 100);
    };
    attempt();
  });
}

function requestHTTP(port, pathname, method = "GET") {
  return new Promise((resolve, reject) => {
    const request = http.request({ host: "127.0.0.1", port, path: pathname, method }, (response) => {
      const chunks = [];
      response.on("data", (chunk) => chunks.push(chunk));
      response.on("end", () => resolve({
        statusCode: response.statusCode,
        body: Buffer.concat(chunks).toString("utf8"),
      }));
    });
    request.on("error", reject);
    request.end();
  });
}

function waitForHTTPStatus(port, pathname, statusCode) {
  return new Promise((resolve, reject) => {
    const deadline = Date.now() + 15000;
    const attempt = async () => {
      try {
        const response = await requestHTTP(port, pathname);
        if (response.statusCode === statusCode) {
          resolve(response);
          return;
        }
      } catch (_) {
        // The loopback listener may not have started yet.
      }
      if (Date.now() >= deadline) {
        reject(new Error(`Timed out waiting for HTTP ${statusCode} at ${pathname}`));
        return;
      }
      setTimeout(attempt, 100);
    };
    attempt();
  });
}

function availablePort() {
  return new Promise((resolve, reject) => {
    const server = net.createServer();
    server.once("error", reject);
    server.listen(0, "127.0.0.1", () => {
      const port = server.address().port;
      server.close((error) => error ? reject(error) : resolve(port));
    });
  });
}

function connectReloadSocket(port) {
  return new Promise((resolve, reject) => {
    const socket = net.createConnection({ host: "127.0.0.1", port });
    const key = Buffer.from("homecms-eleventy-test").toString("base64");
    let response = "";
    const onData = (chunk) => {
      response += chunk.toString("utf8");
      if (response.includes("101 Switching Protocols")) {
        socket.off("data", onData);
        resolve(socket);
      }
    };
    socket.on("connect", () => {
      socket.write([
        "GET /__homecms_live_reload HTTP/1.1",
        "Host: 127.0.0.1",
        "Upgrade: websocket",
        "Connection: Upgrade",
        `Sec-WebSocket-Key: ${key}`,
        "Sec-WebSocket-Version: 13",
        "\r\n",
      ].join("\r\n"));
    });
    socket.on("data", onData);
    socket.once("error", reject);
    socket.once("close", () => reject(new Error("Eleventy LiveReload socket closed before handshake")));
  });
}

test("registers Eleventy hooks on the Programmatic API UserConfig", () => {
  const handlers = {};
  configureProjectDirectories(
    { on(name, callback) { handlers[name] = callback; } },
    { json: false },
    () => {},
  );
  assert.equal(typeof handlers["eleventy.after"], "function");
});

test("serves output index paths without allowing traversal", () => {
  const output = fs.mkdtempSync(path.join(os.tmpdir(), "homecms-eleventy-output-"));
  fs.mkdirSync(path.join(output, "posts"));
  fs.writeFileSync(path.join(output, "posts", "index.html"), "ok");
  try {
    assert.equal(findOutputFile(output, "/posts/").filePath, path.join(output, "posts", "index.html"));
    assert.equal(findOutputFile(output, "/posts").redirect, "/posts/");
    assert.equal(findOutputFile(output, "/../secret").error, 400);
  } finally {
    fs.rmSync(output, { recursive: true, force: true });
  }
});

test("binds the preview server to loopback", async () => {
  const output = fs.mkdtempSync(path.join(os.tmpdir(), "homecms-eleventy-output-"));
  const server = createLoopbackServer(output);
  try {
    await listen(server, 0, "127.0.0.1");
    assert.equal(server.address().address, "127.0.0.1");
  } finally {
    await new Promise((resolve) => {
      server.closeAll();
      server.close(() => resolve());
    });
    fs.rmSync(output, { recursive: true, force: true });
  }
});

test("requires an input directory", () => {
  assert.throws(
    () => parseArguments(["--serve", "--output", "/tmp/output", "--port", "14123"]),
    /--input is required/,
  );
});

test("uses the project-root overlay in JSON mode", () => {
  const project = fs.mkdtempSync(path.join(os.tmpdir(), "homecms-eleventy-fixture-"));
  const packageDir = path.join(project, "node_modules", "@11ty", "eleventy");
  const input = path.join(project, "src");
  const output = path.join(project, "public");
  fs.mkdirSync(packageDir, { recursive: true });
  fs.mkdirSync(path.join(input, "posts"), { recursive: true });
  fs.mkdirSync(path.join(project, "_data"), { recursive: true });
  fs.mkdirSync(path.join(project, "_includes"), { recursive: true });
  fs.mkdirSync(path.join(project, "_layouts"), { recursive: true });
  fs.writeFileSync(path.join(project, "package.json"), "{}");
  fs.writeFileSync(path.join(packageDir, "package.json"), '{"main":"index.js"}');
  fs.writeFileSync(path.join(packageDir, "index.js"), `
    const path = require("node:path");
    module.exports = class FakeEleventy {
      constructor(input, output, options) {
        const root = process.cwd();
        const directories = {
          input: path.resolve(root, input),
          data: path.join(root, "_data"),
          includes: path.join(root, "_includes"),
          layouts: path.join(root, "_layouts"),
          output,
        };
        options.config({ on() {} });
        this.directories = directories;
      }
      async toJSON() {
        return [{
          inputPath: path.join(this.directories.input, "posts/one.md"),
          outputPath: this.directories.output,
          data: {
            directories: {
              data: this.directories.data,
              includes: this.directories.includes,
              layouts: this.directories.layouts,
            },
          },
        }];
      }
    };
  `);

  try {
    const script = path.resolve(__dirname, "eleventy-local-preview.cjs");
    const stdout = childProcess.execFileSync(process.execPath, [
      script,
      "--json",
      "--input", "src",
      "--output", output,
    ], { cwd: project, encoding: "utf8" });
    const [entry] = JSON.parse(stdout);
    assert.equal(entry.inputPath, path.join(input, "posts/one.md"));
    assert.equal(entry.data.directories.data, path.join(project, "_data"));
    assert.equal(entry.data.directories.includes, path.join(project, "_includes"));
    assert.equal(entry.data.directories.layouts, path.join(project, "_layouts"));
    assert.equal(entry.outputPath, output);
  } finally {
    fs.rmSync(project, { recursive: true, force: true });
  }
});

test("re-notifies a missed watcher event after invalidation", async () => {
  const input = fs.mkdtempSync(path.join(os.tmpdir(), "homecms-eleventy-recovery-"));
  const article = path.join(input, "posts", "one.md");
  fs.mkdirSync(path.dirname(article), { recursive: true });
  fs.writeFileSync(article, "# one\n");
  const state = createBuildState(input);
  try {
    const before = fs.statSync(article).mtimeMs;
    assert.equal(state.invalidate("posts/one.md"), 1);
    assert.equal(state.diagnostics().invalidation_generation, 1);
    await new Promise(resolve => setTimeout(resolve, 900));
    assert.ok(fs.statSync(article).mtimeMs > before, "watcher recovery did not touch the article");
  } finally {
    state.activeBuildGeneration = state.invalidationGeneration;
    state.update([]);
    state.stopRecovery();
    fs.rmSync(input, { recursive: true, force: true });
  }
});

test("re-notifies the parent directory when an invalidated article was deleted", async () => {
  const input = fs.mkdtempSync(path.join(os.tmpdir(), "homecms-eleventy-delete-recovery-"));
  const article = path.join(input, "posts", "one.md");
  const articleDirectory = path.dirname(article);
  fs.mkdirSync(articleDirectory, { recursive: true });
  fs.writeFileSync(article, "# one\n");
  const state = createBuildState(input);
  try {
    const before = fs.statSync(articleDirectory).mtimeMs;
    assert.equal(state.invalidate("posts/one.md"), 1);
    fs.rmSync(article);
    await new Promise(resolve => setTimeout(resolve, 900));
    assert.ok(fs.statSync(articleDirectory).mtimeMs > before, "watcher recovery did not touch the parent directory");
  } finally {
    state.activeBuildGeneration = state.invalidationGeneration;
    state.update([]);
    state.stopRecovery();
    fs.rmSync(input, { recursive: true, force: true });
  }
});

test("re-notifies the input root when invalidation has no specific path", async () => {
  const input = fs.mkdtempSync(path.join(os.tmpdir(), "homecms-eleventy-root-recovery-"));
  const state = createBuildState(input);
  try {
    const before = fs.statSync(input).mtimeMs;
    assert.equal(state.invalidate(), 1);
    await new Promise(resolve => setTimeout(resolve, 900));
    assert.ok(fs.statSync(input).mtimeMs > before, "watcher recovery did not touch the input root");
  } finally {
    state.activeBuildGeneration = state.invalidationGeneration;
    state.update([]);
    state.stopRecovery();
    fs.rmSync(input, { recursive: true, force: true });
  }
});

test("serves stale metadata immediately after a resource invalidation", async () => {
  const input = fs.mkdtempSync(path.join(os.tmpdir(), "homecms-eleventy-resource-barrier-"));
  const output = fs.mkdtempSync(path.join(os.tmpdir(), "homecms-eleventy-resource-output-"));
  const state = createBuildState(input);
  state.ready = true;
  state.building = false;
  state.entries.set("posts/one.md", {
    inputPath: path.join(input, "posts", "one.md"),
    outputPath: path.join(output, "posts", "one", "index.html"),
    url: "/posts/one/",
  });
  const server = createLoopbackServer(output, state);
  try {
    await listen(server, 0, "127.0.0.1");
    const port = server.address().port;
    const before = await requestHTTP(port, "/__homecms_metadata?path=posts%2Fone.md");
    assert.equal(before.statusCode, 200);

    const invalidated = await requestHTTP(port, "/__homecms_invalidate", "POST");
    assert.equal(invalidated.statusCode, 202);
    const duringMutation = await requestHTTP(port, "/__homecms_metadata?path=posts%2Fone.md");
    assert.equal(duringMutation.statusCode, 200);
    const stale = JSON.parse(duringMutation.body);
    assert.equal(stale.status, "stale");
    assert.equal(stale.fresh, false);
    assert.equal(stale.invalidation_generation, 1);
    assert.equal(stale.active_build_generation, 0);
    assert.equal(stale.last_build_completed_at, 0);
    assert.equal(typeof stale.invalidation_at, "number");
    assert.equal(stale.inputPath, path.join(input, "posts", "one.md"));
    assert.equal(stale.outputPath, path.join(output, "posts", "one", "index.html"));
    assert.equal(stale.url, "/posts/one/");
  } finally {
    state.stopRecovery();
    server.closeAll();
    await new Promise(resolve => server.close(resolve));
    fs.rmSync(input, { recursive: true, force: true });
    fs.rmSync(output, { recursive: true, force: true });
  }
});

test("does not mark an older build fresh after a second invalidation", async () => {
  const input = fs.mkdtempSync(path.join(os.tmpdir(), "homecms-eleventy-generation-race-"));
  const output = fs.mkdtempSync(path.join(os.tmpdir(), "homecms-eleventy-generation-output-"));
  const state = createBuildState(input);
  const result = {
    inputPath: path.join(input, "posts", "one.md"),
    outputPath: path.join(output, "posts", "one", "index.html"),
    url: "/posts/one/",
  };
  state.ready = true;
  state.building = false;
  state.invalidationGeneration = 10;
  state.activeBuildGeneration = 10;
  state.entries.set("posts/one.md", result);
  const server = createLoopbackServer(output, state);
  try {
    await listen(server, 0, "127.0.0.1");
    const port = server.address().port;

    assert.equal(state.invalidate("posts/one.md"), 11);
    state.begin();
    assert.equal(state.diagnostics().active_build_generation, 11);
    assert.equal(state.invalidate("posts/one.md"), 12);
    state.update([result]);

    assert.equal(state.ready, false);
    assert.equal(state.diagnostics().active_build_generation, 11);
    assert.equal(state.diagnostics().invalidation_generation, 12);
    const stale = await requestHTTP(port, "/__homecms_metadata?path=posts%2Fone.md");
    assert.equal(stale.statusCode, 200);
    assert.equal(JSON.parse(stale.body).status, "stale");
    assert.equal(JSON.parse(stale.body).fresh, false);

    state.begin();
    state.update([result]);
    assert.equal(state.ready, true);
    assert.equal(state.diagnostics().active_build_generation, 12);
    const fresh = await requestHTTP(port, "/__homecms_metadata?path=posts%2Fone.md");
    assert.equal(fresh.statusCode, 200);
    assert.equal(JSON.parse(fresh.body).status, "resolved");
    assert.equal(JSON.parse(fresh.body).fresh, true);
  } finally {
    state.stopRecovery();
    server.closeAll();
    await new Promise(resolve => server.close(resolve));
    fs.rmSync(input, { recursive: true, force: true });
    fs.rmSync(output, { recursive: true, force: true });
  }
});

test("resolves a real Eleventy 3.x project in JSON mode", { skip: !hasRealEleventy() }, () => {
  const fixture = createEleventyFixture();
  try {
    const script = path.resolve(__dirname, "eleventy-local-preview.cjs");
    const stdout = childProcess.execFileSync(process.execPath, [
      script,
      "--json",
      "--input", "src",
      "--output", fixture.output,
    ], { cwd: fixture.project, encoding: "utf8" });
    const entries = JSON.parse(stdout);
    const entry = entries.find((candidate) => path.resolve(fixture.project, candidate.inputPath) === fixture.article);
    assert.ok(entry, `Eleventy did not return metadata for ${fixture.article}`);
    assert.equal(entry.url, "/custom/one/");
  } finally {
    removeEleventyFixture(fixture);
  }
});

test("builds a clean overlay fixture without modifying production output", { skip: !hasRealEleventy() }, async () => {
  const production = createEleventyFixture();
  const previewProject = fs.mkdtempSync(path.join(integrationFixture, ".tmp-overlay-"));
  fs.cpSync(production.project, previewProject, { recursive: true });
  const preview = {
    project: previewProject,
    input: path.join(previewProject, "src"),
    output: path.join(previewProject, "public"),
    article: path.join(previewProject, "src", "posts", "one.md"),
  };
  try {
    assert.equal(fs.existsSync(path.join(production.project, "public")), false);
    const productionArticle = fs.readFileSync(production.article, "utf8");
    const script = path.resolve(__dirname, "eleventy-local-preview.cjs");
    const stdout = childProcess.execFileSync(process.execPath, [
      script,
      "--json",
      "--input", "src",
      "--output", preview.output,
    ], { cwd: preview.project, encoding: "utf8" });
    const entries = JSON.parse(stdout);
    const entry = entries.find((candidate) => path.resolve(preview.project, candidate.inputPath) === preview.article);
    assert.ok(entry, `Eleventy did not return metadata for ${preview.article}`);
    assert.equal(entry.url, "/custom/one/");
    const port = await availablePort();
    const child = childProcess.spawn(process.execPath, [
      script,
      "--serve",
      "--input", "src",
      "--output", preview.output,
      "--port", String(port),
      "--host", "127.0.0.1",
    ], { cwd: preview.project, stdio: ["ignore", "pipe", "pipe"] });
    try {
      const page = await waitForHTTP(port, "/");
      assert.match(page.body, /\/custom\/one\//);
      assert.equal(fs.readFileSync(path.join(preview.output, "images", "source.txt"), "utf8"), "passthrough\n");
      assert.equal(fs.readFileSync(path.join(preview.output, "img", "generated.txt"), "utf8"), "generated by fixture\n");
    } finally {
      child.kill();
      await waitForExit(child);
    }
    assert.equal(fs.existsSync(path.join(production.project, "public")), false);
    assert.equal(fs.readFileSync(production.article, "utf8"), productionArticle);
  } finally {
    removeEleventyFixture(production);
    fs.rmSync(previewProject, { recursive: true, force: true });
  }
});

test("starts real Eleventy serve and broadcasts LiveReload", { skip: !hasRealEleventy() }, async () => {
  const fixture = createEleventyFixture();
  let child;
  let reloadSocket;
  try {
    const port = await availablePort();
    const script = path.resolve(__dirname, "eleventy-local-preview.cjs");
    child = childProcess.spawn(process.execPath, [
      script,
      "--serve",
      "--input", "src",
      "--output", fixture.output,
      "--port", String(port),
      "--host", "127.0.0.1",
    ], {
      cwd: fixture.project,
      env: { ...process.env, ELEVENTY_FIXTURE_SLOW_BUILD: "1" },
      stdio: ["ignore", "pipe", "pipe"],
    });
    const building = await waitForHTTPStatus(port, "/__homecms_ready", 503);
    assert.equal(JSON.parse(building.body).status, "building");
    const page = await waitForHTTP(port, "/custom/one/");
    assert.match(page.body, /__homecms_reload\.js/);
    const reloadScript = await requestHTTP(port, "/__homecms_reload.js");
    assert.equal(reloadScript.statusCode, 200);
    assert.match(reloadScript.body, /homecms-local-preview-reload/);
    assert.match(reloadScript.body, /postMessage/);
    const metadata = await requestHTTP(port, "/__homecms_metadata?path=posts%2Fone.md");
    assert.equal(metadata.statusCode, 200);
    assert.equal(JSON.parse(metadata.body).url, "/custom/one/");
    reloadSocket = await connectReloadSocket(port);
    let reloadReceived = false;
    const reload = new Promise((resolve, reject) => {
      const deadline = setTimeout(() => reject(new Error("Timed out waiting for Eleventy LiveReload")), 15000);
      reloadSocket.on("data", (chunk) => {
        if (chunk.toString("utf8").includes('"type":"eleventy.reload"')) {
          assert.match(chunk.toString("utf8"), /"active_build_generation":/);
          clearTimeout(deadline);
          reloadReceived = true;
          resolve();
        }
      });
      reloadSocket.once("error", reject);
    });
    const invalidated = await requestHTTP(port, "/__homecms_invalidate?path=posts%2Fone.md", "POST");
    assert.equal(invalidated.statusCode, 202);
    const invalidatedMetadata = await requestHTTP(port, "/__homecms_metadata?path=posts%2Fone.md");
    assert.equal(invalidatedMetadata.statusCode, 200);
    assert.equal(JSON.parse(invalidatedMetadata.body).status, "stale");
    assert.equal(JSON.parse(invalidatedMetadata.body).fresh, false);
    const updatedArticle = ["---", "title: Changed", "permalink: /custom/changed/", "---", "", "# {{ title }}", ""].join("\n");
    fs.writeFileSync(fixture.article, updatedArticle);
    await reload;
    const updatedMetadata = await waitForHTTPStatus(port, "/__homecms_metadata?path=posts%2Fone.md", 200);
    assert.equal(updatedMetadata.statusCode, 200);
    assert.equal(JSON.parse(updatedMetadata.body).url, "/custom/changed/");
  } finally {
    reloadSocket?.destroy();
    if (child) {
      child.kill();
      await waitForExit(child);
    }
    removeEleventyFixture(fixture);
  }
});
