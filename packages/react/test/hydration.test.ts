import assert from "node:assert/strict";
import test from "node:test";
import { act, createElement, useState } from "react";
import { renderToString } from "react-dom/server";
import { JSDOM } from "jsdom";
import {
  BROWSER_PROTOCOL_VERSION,
  bootstrap,
} from "../dist/browser.js";
import { ClientOnly } from "../dist/client-only.js";

function Counter({ initial }: { initial: number }) {
  const [count, setCount] = useState(initial);
  return createElement(
    "button",
    { type: "button", onClick: () => setCount(count + 1) },
    `Count: ${count}`,
  );
}

function ClientBoundaryPage({ label }: { label: string }) {
  return createElement(
    ClientOnly,
    { fallback: createElement("p", null, `${label} fallback`) },
    createElement("p", null, `${label} active`),
  );
}

function installDOM(dom: JSDOM) {
  const previous = {
    window: globalThis.window,
    document: globalThis.document,
    HTMLElement: globalThis.HTMLElement,
  };
  Object.assign(globalThis, {
    window: dom.window,
    document: dom.window.document,
    HTMLElement: dom.window.HTMLElement,
    IS_REACT_ACT_ENVIRONMENT: true,
  });
  return () => Object.assign(globalThis, previous);
}

function documentFor(routeId: string, props: object, markup: string) {
  const payload = JSON.stringify({
    apiVersion: BROWSER_PROTOCOL_VERSION,
    buildId: "build-test",
    routeId,
    props,
  }).replaceAll("<", "\\u003c");
  return new JSDOM(
    `<!doctype html><body><div id="__gobeyond">${markup}</div>` +
      `<script id="__GOBEYOND_DATA__" type="application/json">${payload}</script></body>`,
    { url: "https://example.gobeyond.dev/" },
  );
}

test("bootstrap hydrates Go-equivalent markup and attaches interaction", async () => {
  const props = { initial: 2 };
  const dom = documentFor("counter", props, renderToString(createElement(Counter, props)));
  const restore = installDOM(dom);
  const recoverable: unknown[] = [];

  try {
    let result: ReturnType<typeof bootstrap> | undefined;
    await act(async () => {
      result = bootstrap({
        routes: { counter: Counter },
        document: dom.window.document,
        onRecoverableError: (error) => recoverable.push(error),
      });
    });

    const button = dom.window.document.querySelector("button");
    assert.equal(button?.textContent, "Count: 2");
    await act(async () => button?.dispatchEvent(new dom.window.MouseEvent("click", { bubbles: true })));
    assert.equal(button?.textContent, "Count: 3");
    assert.deepEqual(recoverable, []);
    await act(async () => result?.root.unmount());
  } finally {
    restore();
    dom.window.close();
  }
});

test("ClientOnly hydrates its fallback before activating browser content", async () => {
  const props = { label: "Map" };
  const markup = renderToString(createElement(ClientBoundaryPage, props));
  assert.match(markup, />Map fallback</);
  const dom = documentFor("client-boundary", props, markup);
  const restore = installDOM(dom);
  const recoverable: unknown[] = [];

  try {
    let result: ReturnType<typeof bootstrap> | undefined;
    await act(async () => {
      result = bootstrap({
        routes: { "client-boundary": ClientBoundaryPage },
        document: dom.window.document,
        onRecoverableError: (error) => recoverable.push(error),
      });
    });
    assert.equal(dom.window.document.querySelector("p")?.textContent, "Map active");
    assert.deepEqual(recoverable, []);
    await act(async () => result?.root.unmount());
  } finally {
    restore();
    dom.window.close();
  }
});
