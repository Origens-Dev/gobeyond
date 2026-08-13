import assert from "node:assert/strict";
import test from "node:test";
import { act, createElement, useEffect, type ReactElement, type ReactNode } from "react";
import { renderToString } from "react-dom/server";
import { JSDOM } from "jsdom";
import {
  applyDocumentMetadata,
  BROWSER_PROTOCOL_VERSION,
  BUILD_ID_HEADER,
  NAVIGATION_ANNOUNCER_ID,
  bootstrap,
  bootstrapAsync,
  commonLayoutPrefixLength,
  composeRouteElement,
  handleBuildMismatch,
  markBuildHealthy,
  matchBrowserRoute,
  parseRuntimeNavigationPayload,
  refreshNavigation,
  resolveBrowserRoute,
  resolveRouteComponent,
  renderUpdateRequired,
  subscribeNavigation,
  type BuildMismatchEnvironment,
  type CachePolicy,
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
  cache?: CachePolicy,
): RuntimeNavigationPayload {
  return {
    apiVersion: BROWSER_PROTOCOL_VERSION,
    buildId: "build-test",
    routeId,
    result: {
      kind: "ok",
      cache,
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
          siteName: "GoBeyond Example",
          locale: "en_US",
          image: {
            url: "https://example.gobeyond.dev/social-primary.png",
            width: 1200,
            height: 630,
            alt: `${title} social card`,
            type: "image/png",
          },
          images: [
            "https://example.gobeyond.dev/social-one.png",
            "https://example.gobeyond.dev/social-two.png",
          ],
        },
        twitter: {
          card: "summary_large_image",
          title,
          description: `${title} tweet`,
          site: "@gobeyond",
          imageAlt: `${title} social card`,
          images: ["https://example.gobeyond.dev/twitter.png"],
        },
        icons: {
          icon: "/favicon-32x32.png",
          appleTouch: "/apple-touch-icon.png",
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

test("applyDocumentMetadata defaults favicon when icons are omitted", () => {
  const dom = new JSDOM("<!doctype html><html><head></head><body></body></html>", {
    url: "https://example.gobeyond.dev/",
  });
  applyDocumentMetadata(
    {
      lang: "en",
      title: "Home",
      description: "A site",
      robots: "index, follow",
      canonical: "https://example.gobeyond.dev/",
    },
    dom.window.document,
  );
  assert.equal(
    dom.window.document.querySelector('link[rel="icon"]')?.getAttribute("href"),
    "/favicon.ico",
  );
  assert.equal(dom.window.document.querySelector('link[rel="apple-touch-icon"]'), null);
  dom.window.close();
});

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

test("runtime navigation parses result.cache into the CachePolicy shape", () => {
  const withPublicCache = parseRuntimeNavigationPayload(
    JSON.stringify(
      payload("product", { name: "Trail" }, "Trail", {
        mode: "public",
        maxAge: 60,
        sharedMaxAge: 300,
        staleWhileRevalidate: 30,
        staleIfError: 3600,
      }),
    ),
  );
  assert.deepEqual(withPublicCache.result.cache, {
    mode: "public",
    maxAge: 60,
    sharedMaxAge: 300,
    staleWhileRevalidate: 30,
    staleIfError: 3600,
  });

  const withoutCache = parseRuntimeNavigationPayload(
    JSON.stringify(payload("product", { name: "Trail" }, "Trail")),
  );
  assert.equal(withoutCache.result.cache, undefined);

  assert.throws(
    () =>
      parseRuntimeNavigationPayload(
        JSON.stringify(
          payload("product", { name: "Trail" }, "Trail", {
            mode: "cache-forever" as unknown as CachePolicy["mode"],
          }),
        ),
      ),
    /result\.cache\.mode is unsupported/,
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

test("data prefetch loads route code and warms opted-in route data", async () => {
  const dom = documentFor();
  const restore = installDOM(dom);
  let imports = 0;
  let requests = 0;
  const warmedImages: string[] = [];
  try {
    dom.window.Image = class {
      decoding = "async";
      set src(value: string) {
        warmedImages.push(value);
      }
    } as unknown as typeof dom.window.Image;
    let app: ReturnType<typeof bootstrap> | undefined;
    await act(async () => {
      app = bootstrap({
        routes: {
          home: { component: Home, pattern: "/" },
          product: {
            pattern: "/products/[slug]",
            prefetch: "data",
            prefetchImages: [{ path: "hero", w: 1920 }],
            load: async () => {
              imports += 1;
              return { default: Product };
            },
          },
        },
        document: dom.window.document,
        fetch: async () => {
          requests += 1;
          return new Response(
            JSON.stringify(
              payload("product", { name: "Trail", hero: "/hero.jpg" }, "Trail", { mode: "public", maxAge: 60 }),
            ),
            { status: 200, headers: { "content-type": "application/json" } },
          );
        },
        scrollTo() {},
      });
    });
    const link = dom.window.document.querySelector("a");
    assert.ok(link);
    const linkTarget = link.getAttribute("href");
    assert.ok(linkTarget);
    link.dispatchEvent(new dom.window.Event("pointerover", { bubbles: true }));
    await waitFor(() => imports === 1);
    await waitFor(() => requests === 1);
    assert.equal(requests, 1, "prefetch warms the runtime data payload, not only the JS chunk");
    assert.deepEqual(warmedImages, ["/_gobeyond/image?url=%2Fhero.jpg&w=1920&q=75"]);
    await app?.prefetch(linkTarget);
    assert.equal(imports, 1);
    assert.equal(requests, 1, "an already-warm, still-fresh entry is not re-fetched");

    await act(async () => {
      await app?.navigate(linkTarget);
    });
    assert.equal(
      requests,
      1,
      "navigating to a warmed public route serves from the Router Cache instead of re-fetching",
    );
    assert.equal(dom.window.document.querySelector("h1")?.textContent, "Trail");

    app?.destroy();
    await act(async () => app?.root.unmount());
  } finally {
    restore();
    dom.window.close();
  }
});

test("default prefetch is code-only, so navigation performs the first data request", async () => {
  const dom = documentFor();
  const restore = installDOM(dom);
  let requests = 0;
  try {
    let app: ReturnType<typeof bootstrap> | undefined;
    await act(async () => {
      app = bootstrap({
        routes: {
          home: { component: Home, pattern: "/" },
          product: { component: Product, pattern: "/products/[slug]" },
        },
        document: dom.window.document,
        fetch: async () => {
          requests += 1;
          return new Response(
            JSON.stringify(payload("product", { name: "Trail" }, "Trail")),
            { status: 200, headers: { "content-type": "application/json" } },
          );
        },
        scrollTo() {},
      });
    });
    await app?.prefetch("/products/trail");
    assert.equal(requests, 0, "code-only prefetch does not fetch runtime data");

    await act(async () => {
      await app?.navigate("/products/trail");
    });
    assert.equal(
      requests,
      1,
      "navigation performs the first runtime request",
    );

    app?.destroy();
    await act(async () => app?.root.unmount());
  } finally {
    restore();
    dom.window.close();
  }
});

test("viewport-visible Link prefetches once and observes links added later", async () => {
  const dom = documentFor();
  const restore = installDOM(dom);
  let requests = 0;
  let imports = 0;
  type ObserverEntry = { isIntersecting: boolean; target: Element };
  class FakeIntersectionObserver {
    static current: FakeIntersectionObserver | undefined;
    readonly targets = new Set<Element>();
    private readonly callback: (entries: ObserverEntry[]) => void;
    constructor(callback: (entries: ObserverEntry[]) => void) {
      this.callback = callback;
      FakeIntersectionObserver.current = this;
    }
    observe(target: Element) {
      this.targets.add(target);
    }
    unobserve(target: Element) {
      this.targets.delete(target);
    }
    disconnect() {
      this.targets.clear();
    }
    trigger(target: Element) {
      this.callback([{ isIntersecting: true, target }]);
    }
  }
  class FakeMutationObserver {
    static current: FakeMutationObserver | undefined;
    private readonly callback: (records: MutationRecord[]) => void;
    constructor(callback: (records: MutationRecord[]) => void) {
      this.callback = callback;
      FakeMutationObserver.current = this;
    }
    observe() {}
    disconnect() {}
    trigger(node: Node) {
      this.callback([{ addedNodes: [node] } as unknown as MutationRecord]);
    }
  }

  try {
    Object.assign(dom.window, {
      IntersectionObserver: FakeIntersectionObserver,
      MutationObserver: FakeMutationObserver,
    });
    const initialLink = dom.window.document.createElement("a");
    initialLink.href = "/products/trail?view=full";
    dom.window.document.body.append(initialLink);
    initialLink.setAttribute("data-gobeyond-link", "");
    initialLink.setAttribute("data-gobeyond-prefetch", "data");
    let app: ReturnType<typeof bootstrap> | undefined;
    await act(async () => {
      app = bootstrap({
        routes: {
          home: { component: Home, pattern: "/" },
          product: {
            pattern: "/products/[slug]",
            prefetch: "data",
            load: async () => {
              imports += 1;
              return { default: Product };
            },
          },
        },
        document: dom.window.document,
        fetch: async () => {
          requests += 1;
          return new Response(
            JSON.stringify(payload("product", { name: "Trail" }, "Trail", { mode: "private_no_store" })),
            { status: 200, headers: { "content-type": "application/json" } },
          );
        },
        scrollTo() {},
      });
    });

    const observer = FakeIntersectionObserver.current;
    assert.ok(observer);
    assert.equal(observer.targets.has(initialLink), true);
    observer.trigger(initialLink);
    await waitFor(() => imports === 1);
    await waitFor(() => requests === 1);

    const dynamicLink = dom.window.document.createElement("a");
    dynamicLink.href = "/products/second";
    dynamicLink.dataset.gobeyondLink = "";
    dynamicLink.dataset.gobeyondPrefetch = "code";
    dom.window.document.body.append(dynamicLink);
    FakeMutationObserver.current?.trigger(dynamicLink);
    assert.equal(observer.targets.has(dynamicLink), true);
    observer.trigger(dynamicLink);
    await waitFor(() => imports === 1);
    assert.equal(requests, 1, "code-only dynamic Link does not fetch route data");

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
    assert.equal(dom.window.document.querySelectorAll('meta[property="og:image"]').length, 3);
    assert.equal(
      dom.window.document.querySelector('meta[property="og:image:width"]')?.getAttribute("content"),
      "1200",
    );
    assert.equal(
      dom.window.document.querySelector('meta[name="twitter:image:alt"]')?.getAttribute("content"),
      "Trail <Pack> social card",
    );
    assert.equal(dom.window.document.querySelectorAll('meta[name="twitter:image"]').length, 1);
    assert.equal(
      dom.window.document.querySelector('link[rel="icon"]')?.getAttribute("href"),
      "/favicon-32x32.png",
    );
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

test("refresh re-fetches the current route in place without pushing history or moving focus", async () => {
  const dom = documentFor();
  const restore = installDOM(dom);
  const scrolls: Array<[number, number]> = [];
  let requests = 0;
  const request: typeof fetch = async () => {
    requests += 1;
    return new Response(JSON.stringify(payload("home", { count: requests }, "Home")));
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
        scrollTo: (x, y) => scrolls.push([x, y]),
      });
    });
    assert.ok(app);
    const initialHistoryLength = dom.window.history.length;
    const previousActiveElement = dom.window.document.activeElement;

    let refreshed: unknown;
    await act(async () => {
      refreshed = await app?.refresh();
    });

    assert.equal(requests, 1);
    assert.equal(dom.window.history.length, initialHistoryLength, "refresh must not push history");
    assert.equal(dom.window.location.pathname, "/");
    assert.deepEqual(
      scrolls,
      [[0, 0]],
      "refresh restores the same scroll position it started at",
    );
    assert.equal(
      dom.window.document.activeElement,
      previousActiveElement,
      "refresh must not move focus",
    );
    assert.ok(refreshed);

    app?.destroy();
    await act(async () => app?.root.unmount());
  } finally {
    restore();
    dom.window.close();
  }
});

test("refresh only re-fetches when the current path is among the given paths", async () => {
  const dom = documentFor();
  const restore = installDOM(dom);
  let requests = 0;
  const request: typeof fetch = async () => {
    requests += 1;
    return new Response(JSON.stringify(payload("home", {}, "Home")));
  };

  try {
    let app: ReturnType<typeof bootstrap> | undefined;
    await act(async () => {
      app = bootstrap({
        routes: { home: { component: Home, pattern: "/" } },
        document: dom.window.document,
        fetch: request,
        scrollTo() {},
      });
    });
    assert.ok(app);

    const skipped = await app!.refresh(["/products/widget"]);
    assert.equal(skipped, undefined);
    assert.equal(requests, 0);

    let matched: unknown;
    await act(async () => {
      matched = await app?.refresh(["/", "/products/widget"]);
    });
    assert.equal(requests, 1);
    assert.ok(matched);

    app?.destroy();
    await act(async () => app?.root.unmount());
  } finally {
    restore();
    dom.window.close();
  }
});

test("back and forward serve a warmed public route from the Router Cache without an extra fetch", async () => {
  const dom = documentFor();
  const restore = installDOM(dom);
  let requests = 0;
  const request: typeof fetch = async (input) => {
    const path = new URL(String(input)).searchParams.get("path");
    requests += 1;
    if (path === "/products/trail") {
      return new Response(
        JSON.stringify(
          payload("product", { name: "Trail" }, "Trail", { mode: "public", maxAge: 60 }),
        ),
        { status: 200, headers: { "content-type": "application/json" } },
      );
    }
    if (path === "/about") {
      return new Response(
        JSON.stringify(
          payload("about", { name: "About" }, "About", { mode: "public", maxAge: 60 }),
        ),
        { status: 200, headers: { "content-type": "application/json" } },
      );
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
        scrollTo() {},
      });
    });
    assert.ok(app);
    await act(async () => {
      await app.navigate("/products/trail");
    });
    await act(async () => {
      await app.navigate("/about");
    });
    assert.equal(requests, 2, "each fresh navigation still fetches once");

    await act(async () => {
      dom.window.history.back();
      await waitFor(() => dom.window.document.querySelector("h1")?.textContent === "Trail");
    });
    assert.equal(requests, 2, "back replays the warmed Router Cache entry instead of re-fetching");

    await act(async () => {
      dom.window.history.forward();
      await waitFor(() => dom.window.document.querySelector("h1")?.textContent === "About");
    });
    assert.equal(
      requests,
      2,
      "forward replays the warmed Router Cache entry instead of re-fetching",
    );

    app.destroy();
    await act(async () => app?.root.unmount());
  } finally {
    restore();
    dom.window.close();
  }
});

test("clearRouterCache drops warmed entries so a later back/forward re-fetches", async () => {
  const dom = documentFor();
  const restore = installDOM(dom);
  let requests = 0;
  const request: typeof fetch = async (input) => {
    const path = new URL(String(input)).searchParams.get("path");
    requests += 1;
    if (path === "/products/trail") {
      return new Response(
        JSON.stringify(
          payload("product", { name: "Trail" }, "Trail", { mode: "public", maxAge: 60 }),
        ),
        { status: 200, headers: { "content-type": "application/json" } },
      );
    }
    if (path === "/about") {
      return new Response(
        JSON.stringify(
          payload("about", { name: "About" }, "About", { mode: "public", maxAge: 60 }),
        ),
        { status: 200, headers: { "content-type": "application/json" } },
      );
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
        scrollTo() {},
      });
    });
    await act(async () => {
      await app?.navigate("/products/trail");
    });
    await act(async () => {
      await app?.navigate("/about");
    });
    assert.equal(requests, 2);

    app?.clearRouterCache();

    await act(async () => {
      dom.window.history.back();
      await waitFor(() => dom.window.document.querySelector("h1")?.textContent === "Trail");
    });
    assert.equal(requests, 3, "clearRouterCache forces the next navigation to re-fetch");

    app?.destroy();
    await act(async () => app?.root.unmount());
  } finally {
    restore();
    dom.window.close();
  }
});

test("refresh with explicit paths invalidates the Router Cache even for routes other than the current one", async () => {
  const dom = documentFor();
  const restore = installDOM(dom);
  let requests = 0;
  const request: typeof fetch = async (input) => {
    const path = new URL(String(input)).searchParams.get("path");
    requests += 1;
    if (path === "/products/trail") {
      return new Response(
        JSON.stringify(
          payload("product", { name: "Trail" }, "Trail", { mode: "public", maxAge: 60 }),
        ),
        { status: 200, headers: { "content-type": "application/json" } },
      );
    }
    if (path === "/about") {
      return new Response(
        JSON.stringify(
          payload("about", { name: "About" }, "About", { mode: "public", maxAge: 60 }),
        ),
        { status: 200, headers: { "content-type": "application/json" } },
      );
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
        scrollTo() {},
      });
    });
    await act(async () => {
      await app?.navigate("/products/trail");
    });
    await act(async () => {
      await app?.navigate("/about");
    });
    assert.equal(requests, 2);

    // Currently mounted on "/about", but an action recorded a refresh for
    // "/products/trail" - refresh() must drop that cached entry even though
    // it does not match the mounted route.
    let refreshed: unknown;
    await act(async () => {
      refreshed = await app?.refresh(["/products/trail"]);
    });
    assert.equal(refreshed, undefined, "refresh does not re-render a path that is not mounted");
    assert.equal(requests, 2, "refresh itself does not fetch a path it is not mounted on");

    await act(async () => {
      dom.window.history.back();
      await waitFor(() => dom.window.document.querySelector("h1")?.textContent === "Trail");
    });
    assert.equal(
      requests,
      3,
      "the invalidated entry must be re-fetched instead of replayed from the Router Cache",
    );

    app?.destroy();
    await act(async () => app?.root.unmount());
  } finally {
    restore();
    dom.window.close();
  }
});

test("a build mismatch clears the Router Cache", async () => {
  const dom = documentFor();
  const restore = installDOM(dom);
  let requests = 0;
  const request: typeof fetch = async (input) => {
    const path = new URL(String(input)).searchParams.get("path");
    requests += 1;
    if (path === "/products/trail") {
      return new Response(
        JSON.stringify(
          payload("product", { name: "Trail" }, "Trail", { mode: "public", maxAge: 60 }),
        ),
        { status: 200, headers: { "content-type": "application/json" } },
      );
    }
    return new Response(null, {
      status: 409,
      headers: { "x-gobeyond-error": "build_mismatch", "x-gobeyond-build": "build-next" },
    });
  };
  const reloads: string[] = [];
  const sessionStore = new Map<string, string>();
  const mismatchEnvironment: BuildMismatchEnvironment = {
    location: { reload: () => reloads.push("reload") },
    sessionStorage: {
      getItem: (key) => sessionStore.get(key) ?? null,
      setItem: (key, value) => sessionStore.set(key, value),
      removeItem: (key) => sessionStore.delete(key),
      get length() {
        return sessionStore.size;
      },
      key: (index) => [...sessionStore.keys()][index] ?? null,
    },
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
        mismatchEnvironment,
        scrollTo() {},
      });
    });
    await act(async () => {
      await app?.navigate("/products/trail");
    });
    assert.equal(requests, 1);

    await act(async () => {
      await assert.rejects(app!.navigate("/about"));
    });
    assert.deepEqual(reloads, ["reload"], "the guarded mismatch reload ran once");

    await act(async () => {
      await app?.navigate("/products/trail");
    });
    assert.equal(
      requests,
      3,
      "the mismatch cleared the Router Cache, so the previously-warmed route is re-fetched",
    );

    app?.destroy();
    await act(async () => app?.root.unmount());
  } finally {
    restore();
    dom.window.close();
  }
});

test("refreshNavigation drives whichever soft navigation controller is currently installed", async () => {
  const dom = documentFor();
  const restore = installDOM(dom);
  let requests = 0;
  const request: typeof fetch = async () => {
    requests += 1;
    return new Response(JSON.stringify(payload("home", {}, "Home")));
  };

  try {
    assert.equal(await refreshNavigation(), undefined, "no-op before any controller exists");

    let app: ReturnType<typeof bootstrap> | undefined;
    await act(async () => {
      app = bootstrap({
        routes: { home: { component: Home, pattern: "/" } },
        document: dom.window.document,
        fetch: request,
        scrollTo() {},
      });
    });
    assert.ok(app);

    await act(async () => {
      await refreshNavigation();
    });
    assert.equal(requests, 1);

    app?.destroy();
    await act(async () => app?.root.unmount());
    assert.equal(
      await refreshNavigation(),
      undefined,
      "no-op again once the controller is destroyed",
    );
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

test("composeRouteElement nests outermost layouts around the page", () => {
  function Root({ children }: { children?: ReactNode }) {
    return createElement("div", { id: "root" }, children);
  }
  function Nested({ children }: { children?: ReactNode }) {
    return createElement("section", { id: "nested" }, children);
  }
  function NestedPage({ name }: { name: string }) {
    return createElement("h1", null, name);
  }
  const html = renderToString(
    composeRouteElement(
      { page: NestedPage, layouts: [Root, Nested] },
      { name: "Trail" },
    ),
  );
  assert.equal(html, '<div id="root"><section id="nested"><h1>Trail</h1></section></div>');
});

test("commonLayoutPrefixLength compares layout identity", () => {
  function A() {
    return null;
  }
  function B() {
    return null;
  }
  function C() {
    return null;
  }
  assert.equal(commonLayoutPrefixLength([A, B], [A, B, C]), 2);
  assert.equal(commonLayoutPrefixLength([A, B], [A, C]), 1);
  assert.equal(commonLayoutPrefixLength([A], [B]), 0);
});

test("resolveBrowserRoute reads page and layouts from a lazy module", async () => {
  function Layout({ children }: { children?: ReactNode }) {
    return createElement("div", null, children);
  }
  function LazyPage() {
    return createElement("h1", null, "Page");
  }
  const resolved = await resolveBrowserRoute({
    pattern: "/page",
    load: async () => ({ default: LazyPage, page: LazyPage, layouts: [Layout] }),
  });
  assert.equal(resolved?.page, LazyPage);
  assert.deepEqual(resolved?.layouts, [Layout]);
});

test("persistent layouts survive page-only soft navigation", async () => {
  let rootMounts = 0;
  let productsMounts = 0;
  let homeMounts = 0;
  let productMounts = 0;

  function RootLayout({ children }: { children?: ReactNode }) {
    useEffect(() => {
      rootMounts += 1;
    }, []);
    return createElement("div", { "data-layout": "root" }, children);
  }
  function ProductsLayout({ children }: { children?: ReactNode }) {
    useEffect(() => {
      productsMounts += 1;
    }, []);
    return createElement("section", { "data-layout": "products" }, children);
  }
  function HomePage() {
    useEffect(() => {
      homeMounts += 1;
    }, []);
    return createElement(
      "main",
      null,
      createElement("h1", null, "Home"),
      createElement("a", { href: "/products/trail" }, "Product"),
    );
  }
  function ProductPage({ name }: { name: string }) {
    useEffect(() => {
      productMounts += 1;
    }, []);
    return createElement("main", null, createElement("h1", null, name));
  }

  const homeRoute = { page: HomePage, layouts: [RootLayout] };
  const markup = renderToString(composeRouteElement(homeRoute, {}));
  const data = JSON.stringify({
    apiVersion: BROWSER_PROTOCOL_VERSION,
    buildId: "build-test",
    routeId: "home",
    props: {},
  }).replaceAll("<", "\\u003c");
  const dom = new JSDOM(
    "<!doctype html><html><head><title>Home</title></head><body>" +
      `<div id="__gobeyond">${markup}</div>` +
      `<script id="__GOBEYOND_DATA__" type="application/json">${data}</script></body></html>`,
    { url: "https://example.gobeyond.dev/", pretendToBeVisual: true },
  );
  const restore = installDOM(dom);

  try {
    let app: ReturnType<typeof bootstrap> | undefined;
    await act(async () => {
      app = bootstrap({
        routes: {
          home: { page: HomePage, layouts: [RootLayout], pattern: "/" },
          product: {
            page: ProductPage,
            layouts: [RootLayout, ProductsLayout],
            pattern: "/products/[slug]",
          },
        },
        document: dom.window.document,
        fetch: async () =>
          new Response(JSON.stringify(payload("product", { name: "Trail" }, "Trail"))),
        scrollTo() {},
      });
    });
    assert.equal(rootMounts, 1);
    assert.equal(homeMounts, 1);
    assert.equal(productsMounts, 0);

    await act(async () => {
      await app?.navigate("/products/trail");
      await waitFor(
        () => dom.window.document.querySelector("h1")?.textContent === "Trail",
      );
    });

    assert.equal(rootMounts, 1, "shared root layout must stay mounted");
    assert.equal(productsMounts, 1);
    assert.equal(productMounts, 1);
    assert.equal(homeMounts, 1, "home page unmounts but does not remount");
    assert.equal(
      dom.window.document.querySelector('[data-layout="root"] [data-layout="products"] h1')
        ?.textContent,
      "Trail",
    );

    await act(async () => {
      await app?.navigate("/products/trail?view=full");
    });
    assert.equal(rootMounts, 1);
    assert.equal(productsMounts, 1);

    app?.destroy();
    await act(async () => app?.root.unmount());
  } finally {
    restore();
    dom.window.close();
  }
});

test("subscribe and onNavigationStart/Settled emit start then success", async () => {
  const dom = documentFor();
  const restore = installDOM(dom);
  const events: string[] = [];
  const optionEvents: string[] = [];

  try {
    let app: ReturnType<typeof bootstrap> | undefined;
    await act(async () => {
      app = bootstrap({
        routes: {
          home: { component: Home, pattern: "/" },
          product: { component: Product, pattern: "/products/[slug]" },
        },
        document: dom.window.document,
        fetch: async () =>
          new Response(JSON.stringify(payload("product", { name: "Trail" }, "Trail"))),
        scrollTo() {},
        onNavigationStart: (event) => optionEvents.push(`start:${event.routeId}`),
        onNavigationSettled: (event) =>
          optionEvents.push(`${event.type}:${event.routeId}`),
      });
    });
    assert.ok(app);
    const unsubscribe = app.subscribe((event) => {
      events.push(event.type);
    });

    await act(async () => {
      await app?.navigate("/products/trail");
    });

    assert.deepEqual(events, ["start", "success"]);
    assert.deepEqual(optionEvents, ["start:product", "success:product"]);
    unsubscribe();
    events.length = 0;
    await act(async () => {
      await app?.navigate("/products/trail?again=1");
    });
    assert.deepEqual(events, []);
    assert.ok(optionEvents.length >= 4);

    app.destroy();
    await act(async () => app?.root.unmount());
  } finally {
    restore();
    dom.window.close();
  }
});

test("subscribeNavigation receives events without the bootstrap return value", async () => {
  const dom = documentFor();
  const restore = installDOM(dom);
  const events: string[] = [];
  const unsubscribe = subscribeNavigation((event) => {
    events.push(event.type);
  });

  try {
    let app: ReturnType<typeof bootstrap> | undefined;
    await act(async () => {
      app = bootstrap({
        routes: {
          home: { component: Home, pattern: "/" },
          product: { component: Product, pattern: "/products/[slug]" },
        },
        document: dom.window.document,
        fetch: async () =>
          new Response(JSON.stringify(payload("product", { name: "Trail" }, "Trail"))),
        scrollTo() {},
      });
    });
    await act(async () => {
      await app?.navigate("/products/trail");
    });
    assert.deepEqual(events, ["start", "success"]);
    app?.destroy();
    await act(async () => app?.root.unmount());
  } finally {
    unsubscribe();
    restore();
    dom.window.close();
  }
});

test("navigation lifecycle emits error when the runtime request fails", async () => {
  const dom = documentFor();
  const restore = installDOM(dom);
  const events: Array<{ type: string; error?: unknown }> = [];
  const hardNavigations: string[] = [];

  try {
    let app: ReturnType<typeof bootstrap> | undefined;
    await act(async () => {
      app = bootstrap({
        routes: {
          home: { component: Home, pattern: "/" },
          product: { component: Product, pattern: "/products/[slug]" },
        },
        document: dom.window.document,
        fetch: async () => {
          throw new Error("runtime unavailable");
        },
        hardNavigate: (url) => hardNavigations.push(url),
        scrollTo() {},
        onNavigationError() {},
      });
    });
    assert.ok(app);
    app.subscribe((event) => {
      events.push(
        event.type === "error"
          ? { type: event.type, error: event.error }
          : { type: event.type },
      );
    });

    await act(async () => {
      await assert.rejects(app!.navigate("/products/trail"), /runtime unavailable/);
    });

    assert.equal(events[0]?.type, "start");
    assert.equal(events[1]?.type, "error");
    assert.match(String(events[1]?.error), /runtime unavailable/);
    assert.deepEqual(hardNavigations, []);

    app.destroy();
    await act(async () => app?.root.unmount());
  } finally {
    restore();
    dom.window.close();
  }
});
