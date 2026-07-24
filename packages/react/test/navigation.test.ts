import assert from "node:assert/strict";
import test from "node:test";
import { act, createElement, type ReactElement } from "react";
import { renderToString } from "react-dom/server";
import { JSDOM } from "jsdom";
import {
  BROWSER_PROTOCOL_VERSION,
  BUILD_ID_HEADER,
  NAVIGATION_ANNOUNCER_ID,
  bootstrap,
  bootstrapAsync,
  handleBuildMismatch,
  markBuildHealthy,
  matchBrowserRoute,
  parseRuntimeNavigationPayload,
  resolveRouteComponent,
  renderUpdateRequired,
  type BuildMismatchEnvironment,
  type RuntimeNavigationPayload,
} from "../dist/browser.js";

function Page({ name, href }: { name: string; href?: string }): ReactElement {
  return createElement(
    "main",
    null,
    createElement("h1", null, name),
    href ? createElement("a", { href }, `Visit ${href}`) : null,
  );
}

const Home = () => createElement(Page, { name: "Home", href: "/products/trail?view=full" });
const Product = ({ name }: { name: string }) => createElement(Page, { name });
const About = ({ name }: { name: string }) => createElement(Page, { name });

function payload(
  routeId: string,
  props: Record<string, unknown>,
  title: string,
): RuntimeNavigationPayload {
  return {
    apiVersion: BROWSER_PROTOCOL_VERSION,
    buildId: "build-test",
    routeId,
    result: {
      kind: "ok",
      props,
      status: 200,
      metadata: {
        lang: "en",
        title,
        description: `${title} description`,
        canonical: `https://example.gobeyond.dev/${routeId}`,
        robots: "index, follow",
        openGraph: {
          type: "product",
          title,
          description: `${title} social description`,
          url: `https://example.gobeyond.dev/${routeId}`,
          images: [
            "https://example.gobeyond.dev/social-one.png",
            "https://example.gobeyond.dev/social-two.png",
          ],
        },
        twitter: {
          card: "summary_large_image",
          title,
          description: `${title} tweet`,
          images: ["https://example.gobeyond.dev/twitter.png"],
        },
        alternates: [
          { language: "fr", url: `https://example.gobeyond.dev/fr/${routeId}` },
        ],
        jsonLd: [
          {
            "@context": "https://schema.org",
            "@type": "WebPage",
            name: `${title}</script><img src=x onerror=alert(1)>`,
          },
        ],
      },
    },
  };
}

function documentFor(): JSDOM {
  const data = JSON.stringify({
    apiVersion: BROWSER_PROTOCOL_VERSION,
    buildId: "build-test",
    routeId: "home",
    props: {},
  }).replaceAll("<", "\\u003c");
  return new JSDOM(
    "<!doctype html><html lang=fr><head><title>Old</title>" +
      '<meta name="robots" content="noindex"><meta property="og:title" content="Old">' +
      '<meta name="twitter:title" content="Old">' +
      '<link rel="canonical" href="https://example.gobeyond.dev/old">' +
      '<script type="application/ld+json">{"name":"Old"}</script></head><body>' +
      `<div id="__gobeyond">${renderToString(createElement(Home))}</div>` +
      `<script id="__GOBEYOND_DATA__" type="application/json" nonce="test-nonce">` +
      `${data}</script></body></html>`,
    { url: "https://example.gobeyond.dev/", pretendToBeVisual: true },
  );
}

function installDOM(dom: JSDOM) {
  const names = [
    "window",
    "document",
    "Element",
    "HTMLElement",
    "Node",
    "DOMException",
  ] as const;
  const previous = new Map<string, unknown>();
  for (const name of names) previous.set(name, globalThis[name]);
  Object.assign(globalThis, {
    window: dom.window,
    document: dom.window.document,
    Element: dom.window.Element,
    HTMLElement: dom.window.HTMLElement,
    Node: dom.window.Node,
    DOMException: dom.window.DOMException,
    IS_REACT_ACT_ENVIRONMENT: true,
  });
  return () => {
    for (const [name, value] of previous) {
      Object.assign(globalThis, { [name]: value });
    }
  };
}

async function waitFor(check: () => boolean): Promise<void> {
  for (let attempt = 0; attempt < 50; attempt += 1) {
    if (check()) return;
    await new Promise((resolve) => setTimeout(resolve, 5));
  }
  assert.fail("condition was not reached");
}

test("runtime navigation accepts only the explicit lower-camel result DTO", () => {
  assert.equal(
    parseRuntimeNavigationPayload(
      JSON.stringify(payload("product", { name: "Trail" }, "Trail")),
    ).result.props.name,
    "Trail",
  );
  assert.throws(
    () =>
      parseRuntimeNavigationPayload(
        JSON.stringify({
          apiVersion: BROWSER_PROTOCOL_VERSION,
          buildId: "build-test",
          routeId: "product",
          result: { Kind: "ok", Props: {}, Metadata: {} },
        }),
      ),
    /missing kind/,
  );
});

test("route matching gives static manifest routes priority over dynamic routes", () => {
  const matched = matchBrowserRoute("/products/new", {
    catchAll: { component: Home, pattern: "/[...path]" },
    dynamic: { component: Product, pattern: "/products/[slug]" },
    static: { component: About, pattern: "/products/new" },
  });
  assert.equal(matched?.routeId, "static");
});

test("lazy route imports share success and retry after failure", async () => {
  let successfulCalls = 0;
  const successful = {
    pattern: "/products/[slug]",
    load: async () => {
      successfulCalls += 1;
      return { default: Product };
    },
  };
  const [first, second] = await Promise.all([
    resolveRouteComponent(successful),
    resolveRouteComponent(successful),
  ]);
  assert.equal(first, Product);
  assert.equal(second, Product);
  assert.equal(successfulCalls, 1);

  let attempts = 0;
  const retryable = {
    pattern: "/about",
    load: async () => {
      attempts += 1;
      if (attempts === 1) throw new Error("stale chunk");
      return { default: About };
    },
  };
  await assert.rejects(resolveRouteComponent(retryable), /stale chunk/);
  assert.equal(await resolveRouteComponent(retryable), About);
  assert.equal(attempts, 2);
});

test("bootstrapAsync hydrates a lazy initial route", async () => {
  const dom = documentFor();
  const restore = installDOM(dom);
  let imports = 0;
  try {
    let app: Awaited<ReturnType<typeof bootstrapAsync>>;
    await act(async () => {
      app = await bootstrapAsync({
        routes: {
          home: {
            pattern: "/",
            load: async () => {
              imports += 1;
              return { default: Home };
            },
          },
        },
        document: dom.window.document,
        navigation: false,
      });
    });
    assert.ok(app);
    assert.equal(imports, 1);
    assert.equal(dom.window.document.querySelector("h1")?.textContent, "Home");
    await act(async () => app?.root.unmount());
  } finally {
    restore();
    dom.window.close();
  }
});

test("delegated intent prefetch loads route code without route data", async () => {
  const dom = documentFor();
  const restore = installDOM(dom);
  let imports = 0;
  let requests = 0;
  try {
    let app: ReturnType<typeof bootstrap> | undefined;
    await act(async () => {
      app = bootstrap({
        routes: {
          home: { component: Home, pattern: "/" },
          product: {
            pattern: "/products/[slug]",
            load: async () => {
              imports += 1;
              return { default: Product };
            },
          },
        },
        document: dom.window.document,
        fetch: async () => {
          requests += 1;
          return new Response(JSON.stringify(payload("product", { name: "Trail" }, "Trail")));
        },
        scrollTo() {},
      });
    });
    const link = dom.window.document.querySelector("a");
    assert.ok(link);
    link.dispatchEvent(new dom.window.Event("pointerover", { bubbles: true }));
    await waitFor(() => imports === 1);
    assert.equal(requests, 0);
    await app?.prefetch("/products/trail");
    assert.equal(imports, 1);
    app?.destroy();
    await act(async () => app?.root.unmount());
  } finally {
    restore();
    dom.window.close();
  }
});

test("intercepts a same-origin anchor and updates route metadata and accessibility", async () => {
  const dom = documentFor();
  const restore = installDOM(dom);
  const requests: URL[] = [];
  const hardNavigations: string[] = [];
  const scrolls: Array<[number, number]> = [];
  const request: typeof fetch = async (input, init) => {
    const url = new URL(String(input));
    requests.push(url);
    assert.equal(new Headers(init?.headers).get(BUILD_ID_HEADER), "build-test");
    return new Response(
      JSON.stringify(payload("product", { name: "Trail </script> Pack" }, "Trail <Pack>")),
      { status: 200, headers: { "content-type": "application/json" } },
    );
  };

  try {
    let app: ReturnType<typeof bootstrap> | undefined;
    await act(async () => {
      app = bootstrap({
        routes: {
          home: { component: Home, pattern: "/" },
          product: { component: Product, pattern: "/products/[slug]" },
        },
        document: dom.window.document,
        fetch: request,
        hardNavigate: (url) => hardNavigations.push(url),
        scrollTo: (x, y) => scrolls.push([x, y]),
      });
    });

    const link = dom.window.document.querySelector("a");
    assert.ok(link);
    await act(async () => {
      const allowed = link.dispatchEvent(
        new dom.window.MouseEvent("click", { bubbles: true, cancelable: true, button: 0 }),
      );
      assert.equal(allowed, false);
      await waitFor(
        () =>
          dom.window.document.querySelector("h1")?.textContent ===
          "Trail </script> Pack",
      );
    });

    assert.equal(requests.length, 1);
    assert.equal(requests[0].pathname, "/_gobeyond/builds/build-test/runtime/product");
    assert.equal(requests[0].searchParams.get("path"), "/products/trail?view=full");
    assert.equal(dom.window.location.pathname, "/products/trail");
    assert.equal(dom.window.location.search, "?view=full");
    assert.deepEqual(hardNavigations, []);
    assert.equal(dom.window.document.title, "Trail <Pack>");
    assert.equal(dom.window.document.documentElement.lang, "en");
    assert.equal(
      dom.window.document.querySelector('link[rel="canonical"]')?.getAttribute("href"),
      "https://example.gobeyond.dev/product",
    );
    assert.equal(
      dom.window.document.querySelector('meta[name="robots"]')?.getAttribute("content"),
      "index, follow",
    );
    assert.equal(dom.window.document.querySelectorAll('meta[property="og:image"]').length, 2);
    assert.equal(dom.window.document.querySelectorAll('meta[name="twitter:image"]').length, 1);
    assert.equal(
      dom.window.document.querySelectorAll('script[type="application/ld+json"]')
        .length,
      1,
    );
    const jsonLdElement = dom.window.document.querySelector('script[type="application/ld+json"]');
    assert.equal(jsonLdElement?.getAttribute("nonce"), "test-nonce");
    const jsonLd = jsonLdElement?.textContent ?? "";
    assert.match(jsonLd, /\\u003c\/script/);
    assert.equal(dom.window.document.querySelector("img"), null);
    assert.equal(
      dom.window.document.getElementById(NAVIGATION_ANNOUNCER_ID)?.textContent,
      "Navigated to Trail <Pack>",
    );
    assert.equal(dom.window.document.activeElement?.tagName, "MAIN");
    assert.deepEqual(scrolls.at(-1), [0, 0]);

    const modified = dom.window.document.createElement("a");
    modified.href = "/products/other";
    dom.window.document.body.append(modified);
    dom.window.addEventListener("click", (event) => event.preventDefault(), {
      once: true,
    });
    modified.dispatchEvent(
      new dom.window.MouseEvent("click", {
        bubbles: true,
        cancelable: true,
        ctrlKey: true,
      }),
    );
    assert.equal(requests.length, 1);

    await act(async () => app?.root.unmount());
    app?.destroy();
  } finally {
    restore();
    dom.window.close();
  }
});

test(
  "middleware redirects hard-navigate without parsing or mutating the route",
  async () => {
    const dom = documentFor();
    const restore = installDOM(dom);
    const hardNavigations: string[] = [];
    const request: typeof fetch = async (input, init) => {
      const url = new URL(String(input));
      assert.equal(init?.redirect, "manual");
      assert.equal(url.searchParams.get("path"), "/products/private");
      return new Response("<!doctype html><title>Sign in</title>", {
        status: 308,
        headers: {
          "content-type": "text/html; charset=utf-8",
          location: "/sign-in?next=%2Fproducts%2Fprivate",
        },
      });
    };

    try {
      let app: ReturnType<typeof bootstrap> | undefined;
      await act(async () => {
        app = bootstrap({
          routes: {
            home: { component: Home, pattern: "/" },
            product: {
              pattern: "/products/[slug]",
              load: async () => {
                throw new Error("removed route chunk");
              },
            },
          },
          document: dom.window.document,
          fetch: request,
          hardNavigate: (url) => hardNavigations.push(url),
          scrollTo() {},
        });
      });
      assert.ok(app);
      const initialHistoryLength = dom.window.history.length;

      await act(async () => {
        await app.navigate("/products/private");
      });

      assert.deepEqual(hardNavigations, [
        "https://example.gobeyond.dev/sign-in?next=%2Fproducts%2Fprivate",
      ]);
      assert.equal(dom.window.document.querySelector("h1")?.textContent, "Home");
      assert.equal(dom.window.location.pathname, "/");
      assert.equal(dom.window.history.length, initialHistoryLength);

      app.destroy();
      await act(async () => app?.root.unmount());
    } finally {
      restore();
      dom.window.close();
    }
  },
);

test(
  "route result redirects retain their explicit document destination",
  async () => {
    const dom = documentFor();
    const restore = installDOM(dom);
    const hardNavigations: string[] = [];
    const request: typeof fetch = async (_input, init) => {
      assert.equal(init?.redirect, "manual");
      return new Response(
        JSON.stringify({
          ...payload("product", {}, "Redirecting"),
          result: {
            kind: "redirect",
            props: {},
            status: 307,
            redirectTo: "/products/current",
          },
        }),
        {
          status: 307,
          headers: { "content-type": "application/json" },
        },
      );
    };

    try {
      let app: ReturnType<typeof bootstrap> | undefined;
      await act(async () => {
        app = bootstrap({
          routes: {
            home: { component: Home, pattern: "/" },
            product: { component: Product, pattern: "/products/[slug]" },
          },
          document: dom.window.document,
          fetch: request,
          hardNavigate: (url) => hardNavigations.push(url),
          scrollTo() {},
        });
      });
      assert.ok(app);

      await act(async () => {
        await app.navigate("/products/legacy");
      });

      assert.deepEqual(hardNavigations, [
        "https://example.gobeyond.dev/products/current",
      ]);
      assert.equal(dom.window.document.querySelector("h1")?.textContent, "Home");
      assert.equal(dom.window.location.pathname, "/");

      app.destroy();
      await act(async () => app?.root.unmount());
    } finally {
      restore();
      dom.window.close();
    }
  },
);

test(
  "runtime errors fall back to document navigation without rendering route props",
  async () => {
    const dom = documentFor();
    const restore = installDOM(dom);
    const hardNavigations: string[] = [];
    let requests = 0;
    const request: typeof fetch = async () => {
      requests += 1;
      if (requests === 1) {
        return new Response("server unavailable", { status: 503 });
      }
      return new Response(
        JSON.stringify({
          ...payload("product", {}, "Missing"),
          result: { kind: "public_error", props: {}, status: 200 },
        }),
      );
    };

    try {
      let app: ReturnType<typeof bootstrap> | undefined;
      await act(async () => {
        app = bootstrap({
          routes: {
            home: { component: Home, pattern: "/" },
            product: { component: Product, pattern: "/products/[slug]" },
          },
          document: dom.window.document,
          fetch: request,
          hardNavigate: (url) => hardNavigations.push(url),
          scrollTo() {},
        });
      });
      assert.ok(app);

      await act(async () => {
        await app.navigate("/products/unavailable");
        await app.navigate("/products/error");
      });
      assert.equal(dom.window.document.querySelector("h1")?.textContent, "Home");
      assert.equal(dom.window.location.pathname, "/");
      assert.deepEqual(hardNavigations, [
        "https://example.gobeyond.dev/products/unavailable",
        "https://example.gobeyond.dev/products/error",
      ]);

      app.destroy();
      await act(async () => app?.root.unmount());
    } finally {
      restore();
      dom.window.close();
    }
  },
);

test("back and forward refetch routes and restore per-entry scroll", async () => {
  const dom = documentFor();
  const restore = installDOM(dom);
  let scrollX = 12;
  let scrollY = 24;
  const scrolls: Array<[number, number]> = [];
  Object.defineProperties(dom.window, {
    scrollX: { configurable: true, get: () => scrollX },
    scrollY: { configurable: true, get: () => scrollY },
  });
  const request: typeof fetch = async (input) => {
    const path = new URL(String(input)).searchParams.get("path");
    if (path === "/products/trail") {
      return new Response(JSON.stringify(payload("product", { name: "Trail" }, "Trail")));
    }
    if (path === "/about") {
      return new Response(JSON.stringify(payload("about", { name: "About" }, "About")));
    }
    throw new Error(`unexpected runtime path ${path}`);
  };

  try {
    let app: ReturnType<typeof bootstrap> | undefined;
    await act(async () => {
      app = bootstrap({
        routes: {
          home: { component: Home, pattern: "/" },
          product: { component: Product, pattern: "/products/[slug]" },
          about: { component: About, pattern: "/about" },
        },
        document: dom.window.document,
        fetch: request,
        scrollTo: (x, y) => {
          scrollX = x;
          scrollY = y;
          scrolls.push([x, y]);
        },
      });
    });
    assert.ok(app);
    await act(async () => {
      await app.navigate("/products/trail");
    });
    scrollX = 40;
    scrollY = 80;
    dom.window.dispatchEvent(new dom.window.Event("scroll"));
    await act(async () => {
      await app.navigate("/about");
    });

    await act(async () => {
      dom.window.history.back();
      await waitFor(() => dom.window.document.querySelector("h1")?.textContent === "Trail");
    });
    assert.equal(dom.window.location.pathname, "/products/trail");
    assert.deepEqual(scrolls.at(-1), [40, 80]);

    await act(async () => {
      dom.window.history.forward();
      await waitFor(() => dom.window.document.querySelector("h1")?.textContent === "About");
    });
    assert.equal(dom.window.location.pathname, "/about");
    assert.deepEqual(scrolls.at(-1), [0, 0]);

    app.destroy();
    await act(async () => app?.root.unmount());
  } finally {
    restore();
    dom.window.close();
  }
});

test("a stale document reloads once and then renders persistent update-required UI", () => {
  const dom = new JSDOM("<!doctype html><body><main>Stale</main></body>", {
    url: "https://example.gobeyond.dev/",
  });
  let reloads = 0;
  const environment: BuildMismatchEnvironment = {
    location: { reload: () => reloads++ },
    sessionStorage: dom.window.sessionStorage,
  };

  const first = handleBuildMismatch("old", "new", { environment });
  assert.equal(first.disposition, "reloading");
  markBuildHealthy("old", environment);
  const second = handleBuildMismatch("old", "new", {
    environment,
    onUpdateRequired: (error) => renderUpdateRequired(error, dom.window.document),
  });

  assert.equal(second.disposition, "update-required");
  assert.equal(reloads, 1);
  assert.equal(dom.window.document.querySelectorAll('[role="alert"]').length, 1);
  assert.equal(dom.window.document.activeElement?.textContent, "Reload page");
  handleBuildMismatch("old", "new", {
    environment,
    onUpdateRequired: (error) => renderUpdateRequired(error, dom.window.document),
  });
  assert.equal(dom.window.document.querySelectorAll('[role="alert"]').length, 1);
  assert.equal(reloads, 1);
  dom.window.close();
});
