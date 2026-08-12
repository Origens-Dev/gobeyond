# `@go-beyond/react`

Pinned React 19 hydration runtime for GoBeyond sites.

## Browser entry

Generated client entries call `bootstrapAsync` with a route registry. Each
lazy route module exports:

- `page` — the route page component (also the `default` export)
- `layouts` — outermost→innermost layout components from applicable
  `app/**/layout.tsx` files

The runtime composes `Layout0(Layout1(...Page))` for hydration (matching Go
SSR nesting) and soft navigation. When consecutive routes share layout module
identities, React keeps those layout instances mounted and only remounts
diverging segments plus the page.

Eager registrations may use `{ page, layouts, pattern }` (or legacy
`{ component, pattern }` with an empty layout chain).

Routes default to code-only intent prefetch. Generated routes can opt into
data and image warming with a page contract:

```ts
export const page = definePage({
  props: schema.object({ hero: schema.string() }),
  prefetch: {
    data: true,
    images: [{ path: "hero", w: 1920 }],
  },
});
```

Data warming is kept in the current tab's in-memory cache for up to 60 seconds
and is never promoted to a shared HTTP or CDN cache. Repeated prefetches and a
navigation that arrives while warming is in progress share the same request.

## Soft navigation lifecycle

`bootstrap` / `bootstrapAsync` return a controller with `navigate`, `prefetch`,
`subscribe`, and `destroy`. Generated client entries discard that return value,
so layout components should use the module-level helper instead:

```ts
import { subscribeNavigation } from "@go-beyond/react/browser";

useEffect(() => {
  return subscribeNavigation((event) => {
    if (event.type === "start") showProgress();
    if (event.type === "success" || event.type === "error") hideProgress();
  });
}, []);
```

You can also pass `onNavigationStart` / `onNavigationSettled` into a custom
bootstrap call, or use `app.subscribe` when you own the bootstrap return value.
Events:

| `type` | When |
| --- | --- |
| `start` | Soft navigation matched a registry route and began |
| `success` | Soft tree update completed, or navigation handed off to a document load |
| `error` | Soft navigation threw (non-abort); still rethrown to the caller |

## Active route hooks

`usePathname` and `useRoute` from `@go-beyond/react` expose the active request
pathname (and route id / params). The compiler bakes request data into the Go
plan; the browser hooks read module-level soft-navigation state seeded at
bootstrap and updated on soft-nav success so hydration matches.

```ts
import { usePathname, useRoute } from "@go-beyond/react";

function NavLink({ href, children }: { href: string; children: React.ReactNode }) {
  const pathname = usePathname();
  const active = pathname === href;
  return <a href={href} aria-current={active ? "page" : undefined}>{children}</a>;
}
```

## Imports

```ts
import { ClientOnly, Columns, useId, usePathname, useRoute } from "@go-beyond/react";
import { bootstrapAsync } from "@go-beyond/react/browser";
```
