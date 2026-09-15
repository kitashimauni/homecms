#!/usr/bin/env node

"use strict";

const crypto = require("node:crypto");
const fs = require("node:fs");
const http = require("node:http");
const path = require("node:path");
const { createRequire } = require("node:module");

const RELOAD_SCRIPT_PATH = "/__homecms_reload.js";
const RELOAD_SOCKET_PATH = "/__homecms_live_reload";
const READY_PATH = "/__homecms_ready";
const METADATA_PATH = "/__homecms_metadata";
const INVALIDATE_PATH = "/__homecms_invalidate";
const LEGACY_RELOAD_SCRIPT_PATH = "/__hugo_cms_reload.js";
const LEGACY_RELOAD_SOCKET_PATH = "/__hugo_cms_live_reload";
const LEGACY_READY_PATH = "/__hugo_cms_ready";
const LEGACY_METADATA_PATH = "/__hugo_cms_metadata";
const LEGACY_INVALIDATE_PATH = "/__hugo_cms_invalidate";
const WATCH_RECOVERY_DELAY_MS = 750;
const WATCH_RECOVERY_INTERVAL_MS = 1000;
const RELOAD_SCRIPT = `(() => {
  const protocol = location.protocol === "https:" ? "wss:" : "ws:";
  const socket = new WebSocket(protocol + "//" + location.host + "${RELOAD_SOCKET_PATH}");
  socket.addEventListener("message", (event) => {
    try {
      const message = JSON.parse(event.data);
      if (message.type === "eleventy.reload") {
        if (window.parent && window.parent !== window) {
          window.parent.postMessage({
            type: "homecms-local-preview-reload",
            invalidation_generation: message.invalidation_generation,
            active_build_generation: message.active_build_generation,
          }, "*");
        }
        location.reload();
      }
    } catch (_) {
      // Ignore malformed development notifications.
    }
  });
})();`;

function parseArguments(argv) {
  const options = { json: false };
  for (let index = 0; index < argv.length; index += 1) {
    const argument = argv[index];
    if (argument === "--json") {
      options.json = true;
      continue;
    }
    if (argument === "--serve") {
      options.serve = true;
      continue;
    }
    if (argument.startsWith("--") && index + 1 < argv.length) {
      const key = argument.slice(2).replace(/-([a-z])/g, (_, letter) => letter.toUpperCase());
      options[key] = argv[++index];
      continue;
    }
    throw new Error(`Unknown Eleventy local preview argument: ${argument}`);
  }
  if (!options.input) {
    throw new Error("--input is required");
  }
  if (!options.json && (!options.output || !options.port || !options.host)) {
    throw new Error("--output, --port and --host are required for local preview serving");
  }
  return options;
}

function normalizeMetadataPath(value) {
  return value.replaceAll("\\", "/").replace(/^\.\//, "").replace(/^\/+/, "");
}

function createBuildState(input) {
  const state = {
    inputRoot: path.resolve(process.cwd(), input),
    ready: false,
    building: true,
    entries: new Map(),
    invalidationGeneration: 0,
    activeBuildGeneration: 0,
    invalidationAt: 0,
    invalidationPath: "",
    lastBuildCompletedAt: 0,
    hasCompletedBuild: false,
    recoveryTimer: null,
  };
  const clearRecoveryTimer = () => {
    if (state.recoveryTimer !== null) {
      clearTimeout(state.recoveryTimer);
      state.recoveryTimer = null;
    }
  };
  const scheduleRecovery = () => {
    if (state.recoveryTimer !== null) return;
    const recover = () => {
      state.recoveryTimer = null;
      if (state.ready || state.activeBuildGeneration >= state.invalidationGeneration || !state.invalidationPath) return;
      try {
        const inputRoot = path.resolve(state.inputRoot);
        let recoveryPath = path.resolve(inputRoot, state.invalidationPath);
        const relativePath = path.relative(inputRoot, recoveryPath);
        if (relativePath.startsWith(".." + path.sep) || path.isAbsolute(relativePath)) return;
        // A deleted article no longer has an mtime to touch. Walk up to the
        // nearest existing directory so the watcher still receives a
        // notification after a coalesced delete event.
        while (!fs.existsSync(recoveryPath) && recoveryPath !== inputRoot) {
          recoveryPath = path.dirname(recoveryPath);
        }
        fs.utimesSync(recoveryPath, new Date(), new Date());
      } catch (_) {
        // The update may still be writing the file. Keep retrying until a
        // build observes the invalidation or the process is stopped.
      }
      state.recoveryTimer = setTimeout(recover, WATCH_RECOVERY_INTERVAL_MS);
      state.recoveryTimer.unref?.();
    };
    state.recoveryTimer = setTimeout(recover, WATCH_RECOVERY_DELAY_MS);
    state.recoveryTimer.unref?.();
  };
  state.begin = () => {
    state.activeBuildGeneration = state.invalidationGeneration;
    state.ready = false;
    state.building = true;
  };
  state.update = (results, { incremental = false } = {}) => {
    const entries = incremental && state.hasCompletedBuild
      ? new Map(state.entries)
      : new Map();
    const updatedPaths = new Set();
    for (const result of Array.isArray(results) ? results : []) {
      const inputPath = typeof result?.inputPath === "string" ? result.inputPath : "";
      if (!inputPath) continue;
      const absoluteInputPath = path.resolve(process.cwd(), inputPath);
      const relativeInputPath = path.relative(state.inputRoot, absoluteInputPath);
      if (!relativeInputPath || relativeInputPath.startsWith(".." + path.sep) || path.isAbsolute(relativeInputPath)) {
        continue;
      }
      const metadataPath = normalizeMetadataPath(relativeInputPath);
      updatedPaths.add(metadataPath);
      const url = typeof result?.url === "string"
        ? result.url
        : typeof result?.data?.page?.url === "string" ? result.data.page.url : "";
      if (!url) {
        entries.delete(metadataPath);
        continue;
      }
      entries.set(metadataPath, {
        inputPath,
        outputPath: result.outputPath || "",
        url,
      });
    }
    if (incremental && state.invalidationPath && state.invalidationPath !== ".") {
      const invalidatedPath = path.resolve(state.inputRoot, state.invalidationPath);
      const invalidatedMetadataPath = normalizeMetadataPath(state.invalidationPath);
      if (!fs.existsSync(invalidatedPath) || (entries.has(invalidatedMetadataPath) && !updatedPaths.has(invalidatedMetadataPath))) {
        entries.delete(invalidatedMetadataPath);
      }
    }
    state.entries = entries;
    state.ready = state.activeBuildGeneration >= state.invalidationGeneration;
    state.building = !state.ready;
    state.lastBuildCompletedAt = Date.now();
    state.hasCompletedBuild = true;
    if (state.ready) {
      state.invalidationAt = 0;
      state.invalidationPath = "";
      clearRecoveryTimer();
    }
  };
  state.invalidate = (articlePath = "") => {
    state.invalidationGeneration += 1;
    state.ready = false;
    state.building = true;
    state.invalidationAt = Date.now();
    const absoluteArticlePath = path.resolve(state.inputRoot, articlePath);
    const relativeArticlePath = path.relative(state.inputRoot, absoluteArticlePath);
    state.invalidationPath = articlePath && relativeArticlePath && !relativeArticlePath.startsWith(".." + path.sep) && !path.isAbsolute(relativeArticlePath)
      ? normalizeMetadataPath(relativeArticlePath)
      : ".";
    clearRecoveryTimer();
    scheduleRecovery();
    return state.invalidationGeneration;
  };
  state.diagnostics = () => ({
    invalidation_generation: state.invalidationGeneration,
    active_build_generation: state.activeBuildGeneration,
    last_build_completed_at: state.lastBuildCompletedAt,
    invalidation_at: state.invalidationAt,
  });
  state.get = (articlePath) => {
    const absoluteArticlePath = path.resolve(state.inputRoot, articlePath);
    const relativeArticlePath = path.relative(state.inputRoot, absoluteArticlePath);
    if (!relativeArticlePath || relativeArticlePath.startsWith(".." + path.sep) || path.isAbsolute(relativeArticlePath)) {
      return null;
    }
    return state.entries.get(normalizeMetadataPath(relativeArticlePath)) || null;
  };
  state.stopRecovery = clearRecoveryTimer;
  return state;
}

function configureProjectDirectories(eleventyConfig, options, notify, buildState) {
  if (!options.json) {
    eleventyConfig.on("eleventy.before", () => buildState?.begin());
    eleventyConfig.on("eleventy.after", (event) => {
      buildState?.update(event?.results, { incremental: event?.incremental === true });
      notify(event);
    });
  }
}

function enableIncrementalWatch(eleventy) {
  if (typeof eleventy?.setIncrementalBuild !== "function") return false;
  eleventy.setIncrementalBuild(true);
  return true;
}

function getEleventyClass() {
  const projectRequire = createRequire(path.join(process.cwd(), "package.json"));
  const loaded = projectRequire("@11ty/eleventy");
  return loaded.Eleventy || loaded.default || loaded;
}

function contentType(filePath) {
  const extension = path.extname(filePath).toLowerCase();
  const types = {
    ".css": "text/css; charset=utf-8",
    ".gif": "image/gif",
    ".html": "text/html; charset=utf-8",
    ".ico": "image/x-icon",
    ".jpeg": "image/jpeg",
    ".jpg": "image/jpeg",
    ".js": "text/javascript; charset=utf-8",
    ".json": "application/json; charset=utf-8",
    ".png": "image/png",
    ".svg": "image/svg+xml",
    ".txt": "text/plain; charset=utf-8",
    ".webp": "image/webp",
    ".woff": "font/woff",
    ".woff2": "font/woff2",
  };
  return types[extension] || "application/octet-stream";
}

function findOutputFile(outputRoot, pathname) {
  let decodedPath;
  try {
    decodedPath = decodeURIComponent(pathname);
  } catch (_) {
    return { error: 400 };
  }
  const root = path.resolve(outputRoot);
  const relativePath = decodedPath.replace(/^[/\\]+/, "");
  const candidate = path.resolve(root, relativePath);
  if (candidate !== root && !candidate.startsWith(`${root}${path.sep}`)) {
    return { error: 400 };
  }

  try {
    const info = fs.statSync(candidate);
    if (info.isFile()) return { filePath: candidate, redirect: null };
    if (info.isDirectory()) {
      if (!pathname.endsWith("/")) return { redirect: `${pathname}/` };
      const indexPath = path.join(candidate, "index.html");
      if (fs.existsSync(indexPath)) return { filePath: indexPath, redirect: null };
    }
  } catch (_) {
    // Continue with Eleventy’s extension and trailing-slash conventions.
  }

  const htmlPath = `${candidate}.html`;
  if (fs.existsSync(htmlPath)) return { filePath: htmlPath, redirect: null };
  const indexPath = path.join(candidate, "index.html");
  if (fs.existsSync(indexPath)) {
    return pathname.endsWith("/")
      ? { filePath: indexPath, redirect: null }
      : { redirect: `${pathname}/` };
  }
  return { error: 404 };
}

function injectReloadScript(body) {
  const tag = `<script src="${RELOAD_SCRIPT_PATH}"></script>`;
  if (body.includes("</head>")) return body.replace("</head>", `${tag}</head>`);
  if (body.includes("</body>")) return body.replace("</body>", `${tag}</body>`);
  return `${body}${tag}`;
}

function websocketFrame(message) {
  const payload = Buffer.from(JSON.stringify(message));
  if (payload.length < 126) {
    return Buffer.concat([Buffer.from([0x81, payload.length]), payload]);
  }
  if (payload.length < 65536) {
    const header = Buffer.alloc(4);
    header[0] = 0x81;
    header[1] = 126;
    header.writeUInt16BE(payload.length, 2);
    return Buffer.concat([header, payload]);
  }
  throw new Error("Eleventy local preview reload message is too large");
}

function sendJSON(response, status, body) {
  const payload = Buffer.from(JSON.stringify(body));
  response.writeHead(status, {
    "Content-Length": payload.length,
    "Content-Type": "application/json; charset=utf-8",
    "Cache-Control": "no-store",
  });
  response.end(payload);
}

function createLoopbackServer(outputRoot, buildState = { ready: true, building: false, get: () => null }) {
  const clients = new Set();
  const server = http.createServer((request, response) => {
    const requestURL = new URL(request.url || "/", "http://127.0.0.1/");
    if (requestURL.pathname === READY_PATH || requestURL.pathname === LEGACY_READY_PATH) {
      sendJSON(response, buildState.ready ? 200 : 503, {
        status: buildState.ready ? "ready" : "building",
        building: buildState.building,
      });
      return;
    }
    if (requestURL.pathname === INVALIDATE_PATH || requestURL.pathname === LEGACY_INVALIDATE_PATH) {
      if (request.method !== "POST") {
        sendJSON(response, 405, { status: "method_not_allowed" });
        return;
      }
      const generation = buildState.invalidate?.(requestURL.searchParams.get("path") || "") || 0;
      sendJSON(response, 202, { status: "invalidated", generation, ...buildState.diagnostics?.() });
      return;
    }
    if (requestURL.pathname === METADATA_PATH || requestURL.pathname === LEGACY_METADATA_PATH) {
      const articlePath = requestURL.searchParams.get("path");
      const metadata = articlePath ? buildState.get(articlePath) : null;
      if (!buildState.ready) {
        if (metadata) {
          sendJSON(response, 200, {
            status: "stale",
            fresh: false,
            ...buildState.diagnostics?.(),
            ...metadata,
          });
          return;
        }
        sendJSON(response, 503, { status: "building", ...buildState.diagnostics?.() });
        return;
      }
      if (!metadata) {
        sendJSON(response, 404, { status: "not_found", ...buildState.diagnostics?.() });
        return;
      }
      sendJSON(response, 200, { status: "resolved", fresh: true, ...buildState.diagnostics?.(), ...metadata });
      return;
    }
    if (requestURL.pathname === RELOAD_SCRIPT_PATH || requestURL.pathname === LEGACY_RELOAD_SCRIPT_PATH) {
      const body = Buffer.from(RELOAD_SCRIPT);
      response.writeHead(200, { "Content-Type": "text/javascript; charset=utf-8", "Content-Length": body.length });
      if (request.method !== "HEAD") response.end(body);
      else response.end();
      return;
    }

    const match = findOutputFile(outputRoot, requestURL.pathname);
    if (match.error) {
      response.writeHead(match.error, { "Content-Type": "text/plain; charset=utf-8" });
      response.end(match.error === 404 ? "Not found" : "Invalid path");
      return;
    }
    if (match.redirect) {
      response.writeHead(301, { Location: match.redirect });
      response.end();
      return;
    }

    let body = fs.readFileSync(match.filePath);
    const type = contentType(match.filePath);
    if (type.startsWith("text/html")) {
      body = Buffer.from(injectReloadScript(body.toString("utf8")));
    }
    response.writeHead(200, {
      "Cache-Control": "no-store",
      "Content-Length": body.length,
      "Content-Type": type,
    });
    if (request.method !== "HEAD") response.end(body);
    else response.end();
  });

  server.on("upgrade", (request, socket) => {
    const requestURL = new URL(request.url || "/", "http://127.0.0.1/");
    const key = request.headers["sec-websocket-key"];
    if (![RELOAD_SOCKET_PATH, LEGACY_RELOAD_SOCKET_PATH].includes(requestURL.pathname) || typeof key !== "string") {
      socket.destroy();
      return;
    }
    const accept = crypto.createHash("sha1").update(`${key}258EAFA5-E914-47DA-95CA-C5AB0DC85B11`).digest("base64");
    clients.add(socket);
    socket.write([
      "HTTP/1.1 101 Switching Protocols",
      "Upgrade: websocket",
      "Connection: Upgrade",
      `Sec-WebSocket-Accept: ${accept}`,
      "\r\n",
    ].join("\r\n"));
    socket.on("close", () => clients.delete(socket));
    socket.on("error", () => clients.delete(socket));
  });

  server.broadcast = (message) => {
    const frame = websocketFrame(message);
    for (const client of clients) {
      if (!client.destroyed) client.write(frame);
    }
  };

  server.closeAll = () => {
    for (const client of clients) client.destroy();
    clients.clear();
  };
  return server;
}

function listen(server, port, host) {
  return new Promise((resolve, reject) => {
    const onError = (error) => {
      server.off("listening", onListening);
      reject(error);
    };
    const onListening = () => {
      server.off("error", onError);
      resolve();
    };
    server.once("error", onError);
    server.once("listening", onListening);
    server.listen({ host, port: Number(port) });
  });
}

async function closeServer(server) {
  if (!server) return;
  server.closeAll();
  if (!server.listening) return;
  await new Promise((resolve) => server.close(() => resolve()));
}

async function main(argv = process.argv.slice(2)) {
  const options = parseArguments(argv);
  const Eleventy = getEleventyClass();
  const buildState = options.json ? null : createBuildState(options.input);
  const server = options.json ? null : createLoopbackServer(options.output, buildState);
  let stopping = false;
  const notify = () => server?.broadcast({ type: "eleventy.reload", ...buildState?.diagnostics?.() });
  if (server) await listen(server, options.port, options.host);
  const eleventy = new Eleventy(options.input, options.output, {
    source: "script",
    runMode: options.json ? "build" : "serve",
    quietMode: options.json,
    config: (eleventyConfig) => configureProjectDirectories(eleventyConfig, options, notify, buildState),
  });

  if (options.json) {
    const result = await eleventy.toJSON();
    process.stdout.write(JSON.stringify(result));
    return;
  }

  try {
    await eleventy.init();
    enableIncrementalWatch(eleventy);
    await eleventy.watch();
  } catch (error) {
    buildState?.stopRecovery?.();
    await closeServer(server);
    throw error;
  }

  const shutdown = async () => {
    if (stopping) return;
    stopping = true;
    await eleventy.stopWatch();
    buildState?.stopRecovery?.();
    await closeServer(server);
    process.exit(0);
  };
  process.on("SIGINT", shutdown);
  process.on("SIGTERM", shutdown);
}

if (require.main === module) {
  main().catch((error) => {
    console.error(error?.stack || error);
    process.exitCode = 1;
  });
}

module.exports = {
  createBuildState,
  configureProjectDirectories,
  createLoopbackServer,
  enableIncrementalWatch,
  findOutputFile,
  listen,
  parseArguments,
};
