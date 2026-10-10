import assert from "node:assert/strict";
import { EventEmitter } from "node:events";
import { mock, test } from "node:test";

test("npm launcher hides native child consoles by default", async (t) => {
  const spawnCalls = [];
  const syncCalls = [];
  mock.module("node:child_process", {
    namedExports: {
      spawn: (...args) => {
        spawnCalls.push(args);
        const child = new EventEmitter();
        queueMicrotask(() => child.emit("exit", 0, null));
        return child;
      },
      spawnSync: (...args) => {
        syncCalls.push(args);
        return { status: 0, stdout: "output", stderr: "" };
      },
    },
  });
  t.after(() => mock.restoreAll());

  const { runChild } = await import("../lib/child.js?windows-hide-child");
  const { productionRunner } = await import("../lib/update.js?windows-hide-update");

  await runChild("memory", ["status"]);
  const runner = productionRunner();
  assert.equal(runner.output("npm", ["--version"]), "output");

  assert.equal(spawnCalls[0][2].windowsHide, true);
  assert.equal(syncCalls[0][2].windowsHide, true);
});

test("explicit child options can still override the default", async () => {
  const spawnCalls = [];
  mock.module("node:child_process", {
    namedExports: {
      spawn: (...args) => {
        spawnCalls.push(args);
        const child = new EventEmitter();
        queueMicrotask(() => child.emit("exit", 0, null));
        return child;
      },
      spawnSync: () => ({ status: 0, stdout: "", stderr: "" }),
    },
  });

  const { runChild } = await import("../lib/child.js?windows-hide-override");
  await runChild("memory", ["status"], { windowsHide: false });
  assert.equal(spawnCalls[0][2].windowsHide, false);
  mock.restoreAll();
});
