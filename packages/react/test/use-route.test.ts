import assert from "node:assert/strict";
import test from "node:test";
import { act, createElement, useEffect } from "react";
import { createRoot } from "react-dom/client";
import { renderToStaticMarkup } from "react-dom/server";
import { JSDOM } from "jsdom";
import {
  getActiveNavigation,
  setActiveNavigation,
} from "../dist/active-navigation.js";
import { Columns } from "../dist/columns.js";
import { extractRouteParams } from "../dist/navigation.js";
import { usePathname, useRoute } from "../dist/use-route.js";

test("usePathname returns baked pathname when no active route is set", () => {
  setActiveNavigation(undefined);
  const seen: string[] = [];
  function Probe() {
    seen.push(usePathname("/products/widget/"));
    return null;
  }
  renderToStaticMarkup(createElement(Probe));
  assert.equal(seen[0], "/products/widget");
});

test("usePathname and useRoute read module-level active navigation state", () => {
  setActiveNavigation({
    routeId: "product",
    pathname: "/products/trail",
    params: { slug: "trail" },
  });
  const pathnames: string[] = [];
  const routes: Array<ReturnType<typeof useRoute>> = [];
  function Probe() {
    pathnames.push(usePathname());
    routes.push(useRoute());
    return null;
  }
  renderToStaticMarkup(createElement(Probe));
  assert.equal(pathnames[0], "/products/trail");
  assert.deepEqual(routes[0], {
    routeId: "product",
    pathname: "/products/trail",
    params: { slug: "trail" },
  });
  setActiveNavigation(undefined);
});

test("usePathname re-renders when active navigation changes", async () => {
  const dom = new JSDOM("<!doctype html><html><body><div id='root'></div></body></html>");
  const { window } = dom;
  const previous = {
    window: globalThis.window,
    document: globalThis.document,
  };
  Object.defineProperty(globalThis, "window", { value: window, configurable: true });
  Object.defineProperty(globalThis, "document", {
    value: window.document,
    configurable: true,
  });

  setActiveNavigation({
    routeId: "home",
    pathname: "/",
    params: {},
  });

  const seen: string[] = [];
  function Probe() {
    const pathname = usePathname();
    useEffect(() => {
      seen.push(pathname);
    }, [pathname]);
    return createElement("span", null, pathname);
  }

  const root = createRoot(window.document.getElementById("root")!);
  try {
    await act(async () => {
      root.render(createElement(Probe));
    });
    assert.equal(window.document.querySelector("span")?.textContent, "/");

    await act(async () => {
      setActiveNavigation({
        routeId: "about",
        pathname: "/about",
        params: {},
      });
    });
    assert.equal(window.document.querySelector("span")?.textContent, "/about");
    assert.ok(seen.includes("/"));
    assert.ok(seen.includes("/about"));
  } finally {
    await act(async () => root.unmount());
    setActiveNavigation(undefined);
    Object.defineProperty(globalThis, "window", {
      value: previous.window,
      configurable: true,
    });
    Object.defineProperty(globalThis, "document", {
      value: previous.document,
      configurable: true,
    });
    dom.window.close();
  }
});

test("extractRouteParams mirrors bracket pattern matching", () => {
  assert.deepEqual(extractRouteParams("/products/[slug]", "/products/trail"), {
    slug: "trail",
  });
  assert.deepEqual(extractRouteParams("/docs/[[...path]]", "/docs"), {
    path: "",
  });
  assert.deepEqual(extractRouteParams("/docs/[...path]", "/docs/a/b"), {
    path: "a/b",
  });
});

test("getActiveNavigation exposes the current snapshot", () => {
  setActiveNavigation({
    routeId: "home",
    pathname: "/home/",
    params: {},
  });
  assert.deepEqual(getActiveNavigation(), {
    routeId: "home",
    pathname: "/home",
    params: {},
  });
  setActiveNavigation(undefined);
  assert.equal(getActiveNavigation(), undefined);
});

test("Columns emits CSS multi-column styles without measuring", () => {
  const markup = renderToStaticMarkup(
    createElement(
      Columns,
      { columnCount: 3, gap: 16 },
      createElement("img", { src: "/a.jpg", alt: "A" }),
      createElement("img", { src: "/b.jpg", alt: "B" }),
    ),
  );
  assert.match(markup, /column-count:3/);
  assert.match(markup, /column-gap:16px/);
  assert.match(markup, /src="\/a\.jpg"/);
  assert.match(markup, /src="\/b\.jpg"/);
});
