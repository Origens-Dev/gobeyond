import { mkdir, readdir, writeFile } from 'node:fs/promises'
import { dirname, join } from 'node:path'

export class CreateProjectError extends Error {}

// This is the first public MVP release line. Keep the Go module and every
// published JavaScript package on the exact same release line in a starter.
const GOBEYOND_VERSION = '0.1.0-alpha.6'
const REACT_VERSION = '19.2.8'

/**
 * Create a complete GoBeyond starter. An existing target is accepted only
 * when it is empty: the scaffolder never merges with or overwrites a project.
 */
export async function createProject(destination, { projectName = 'my-gobeyond-site', tailwind = false } = {}) {
  const existing = await readDirectory(destination)
  if (existing !== null && existing.length > 0) {
    throw new CreateProjectError(`refusing to overwrite non-empty directory: ${destination}`)
  }
  await mkdir(destination, { recursive: true })

  for (const [relativePath, contents] of Object.entries(projectFiles(projectName, { tailwind }))) {
    const absolutePath = join(destination, relativePath)
    await mkdir(dirname(absolutePath), { recursive: true })
    await writeFile(absolutePath, contents, { encoding: 'utf8', flag: 'wx' })
  }
}

async function readDirectory(path) {
  try {
    return await readdir(path)
  } catch (error) {
    if (error && typeof error === 'object' && error.code === 'ENOENT') return null
    throw error
  }
}

function json(value) {
  return `${JSON.stringify(value, null, 2)}\n`
}

function projectFiles(projectName, { tailwind }) {
  const modulePath = `example.com/${projectName}`
  return {
    'package.json': json({
      name: projectName,
      private: true,
      version: '0.1.0',
      type: 'module',
      packageManager: 'pnpm@10.33.0',
      engines: { node: '>=22.0.0' },
      scripts: {
        // `gobeyond build` generates plans/contracts, bundles browser JS, and
        // writes the Node-free server binary to dist/server.
        build: 'gobeyond generate && gobeyond build',
        generate: 'gobeyond generate',
        'generate:check': 'gobeyond generate --check',
        routes: 'gobeyond routes',
        doctor: 'gobeyond doctor',
        dev: 'gobeyond dev',
        serve: './dist/server/gobeyond-server',
        preview: 'pnpm serve',
        typecheck: 'tsc -p tsconfig.json --noEmit',
        test: 'gobeyond generate && gobeyond generate --check && pnpm typecheck && go test ./...',
      },
      dependencies: {
        '@go-beyond/react': GOBEYOND_VERSION,
        '@go-beyond/schema': GOBEYOND_VERSION,
        react: REACT_VERSION,
        'react-dom': REACT_VERSION,
      },
      devDependencies: {
        ...(tailwind ? { '@tailwindcss/postcss': '^4.1.12' } : {}),
        '@go-beyond/compiler': GOBEYOND_VERSION,
        '@go-beyond/vite': GOBEYOND_VERSION,
        '@types/react': REACT_VERSION,
        '@types/react-dom': '19.2.3',
        '@vitejs/plugin-react': '6.0.4',
        typescript: '5.9.3',
        vite: '8.1.5',
        ...(tailwind ? { tailwindcss: '^4.1.12' } : {}),
      },
    }),
    'go.mod': `module ${modulePath}\n\ngo 1.24.0\n\nrequire github.com/Origens-Dev/gobeyond v${GOBEYOND_VERSION}\n`,
    '.gitignore': `.gobeyond/\ndist/\nnode_modules/\n.env\n.env.local\n.env.*.local\n`,
    '.env.example': `GOBEYOND_PUBLIC_ORIGIN=http://localhost:8080\n`,
    'tsconfig.json': json({
      compilerOptions: {
        target: 'ES2022', module: 'NodeNext', moduleResolution: 'NodeNext',
        lib: ['ES2022', 'DOM', 'DOM.Iterable'], strict: true, jsx: 'react-jsx',
        noUncheckedIndexedAccess: true, exactOptionalPropertyTypes: true,
        verbatimModuleSyntax: true, skipLibCheck: true,
      },
      include: ['app/**/*.ts', 'app/**/*.tsx', 'components/**/*.ts', 'components/**/*.tsx', 'client.tsx'],
    }),
    'AGENTS.md': managedAgentsBlock(),
    'README.md': starterReadme(projectName),
    'Dockerfile': dockerfile()
      .replace('RUN gobeyond build', 'RUN pnpm build')
      .replace('COPY --from=build /src/dist/static /app/dist/static\n', '')
      .replace('ENV GOBEYOND_STATIC_DIR=/app/dist/static\n', '')
      .replace(
        'ENV GOBEYOND_PLAN_DIR=/app/dist/server/render-plans',
        'ENV GOBEYOND_PLAN_DIR=/app/dist/server/render-plans\nENV GOBEYOND_RUNTIME_DATA_DIR=/app/dist/server/runtime-data',
      ),
    '.github/workflows/verify.yml': workflow(),
    'vite.config.ts': viteConfig().replace(
      '  publicDir: false,',
      "  resolve: { dedupe: ['react', 'react-dom'] },\n  publicDir: false,",
    ),
    'client.tsx': clientEntry(),
    'app/page.schema.ts': `import { definePage, schema } from '@go-beyond/schema'\n\nexport const page = definePage({ props: schema.object({}) })\n`,
    'app/page.metadata.ts': homeMetadata(),
    'app/page.tsx': `import { GreetingCounter } from '../components/greeting-counter.js'\nimport './site.css'\n\nexport default function HomePage() {\n  return (\n    <main>\n      <h1>Welcome to GoBeyond</h1>\n      <p>A website-first React application rendered by Go.</p>\n      <p><a href="/products/portable-react">See the dynamic product page</a></p>\n      <GreetingCounter initial={0} />\n    </main>\n  )\n}\n`,
    'app/site.css': `${tailwind ? '@import "tailwindcss";\n\n' : ''}:root { color: #17211b; background: #f4f1e8; font-family: system-ui, sans-serif; }\nbody { margin: 0; }\nmain { box-sizing: border-box; width: min(100% - 2rem, 64rem); margin-inline: auto; padding-block: 3rem; }\nimg { display: block; max-width: 100%; height: auto; }\nbutton { min-height: 2.75rem; padding-inline: 1rem; }\n`,
    ...(tailwind ? { 'postcss.config.mjs': `export default { plugins: { '@tailwindcss/postcss': {} } }\n` } : {}),
    'app/vite-env.d.ts': `/// <reference types="vite/client" />\n`,
    'components/greeting-counter.tsx': `import { useState } from 'react'\n\nexport function GreetingCounter({ initial }: { initial: number }) {\n  const [count, setCount] = useState(initial)\n  return <button type="button" onClick={() => setCount(count + 1)}>Clicks: {count}</button>\n}\n`,
    'app/products/[slug]/page.schema.ts': `import { definePage, schema } from '@go-beyond/schema'\n\nexport const page = definePage({\n  props: schema.object({\n    name: schema.string(),\n    description: schema.string(),\n    price: schema.string(),\n    availability: schema.string(),\n    imageURL: schema.string(),\n  }),\n  // Origin props ISR: the Go loader may be reused for 60s per URL, and a\n  // publish can drop it early with cache.RevalidateTag("products"). This is\n  // not an HTTP header; the loader's gb.CachePolicy still owns Cache-Control.\n  revalidate: 60,\n  tags: ['products'],\n})\n`,
    'app/products/[slug]/page.tsx': `import type { InferPageProps } from '@go-beyond/schema'\nimport '../../site.css'\nimport { AddToCart } from '../../../components/add-to-cart.js'\nimport { page } from './page.schema.js'\n\ntype Props = InferPageProps<typeof page>\n\nexport default function ProductPage({ name, description, price, availability, imageURL }: Props) {\n  return (\n    <main>\n      <article>\n        <img src={imageURL} alt={name} width="800" height="500" />\n        <h1>{name}</h1>\n        <p>{description}</p>\n        <p><strong>{price}</strong> · {availability}</p>\n        <AddToCart />\n      </article>\n    </main>\n  )\n}\n`,
    'app/products/[slug]/actions.ts': `import { defineAction, schema } from '@go-beyond/schema'\n\nexport const addToCart = defineAction({\n  input: schema.object({ productName: schema.string() }),\n  output: schema.object({ added: schema.boolean() }),\n})\n`,
    'app/products/[slug]/loading.tsx': `export default function Loading() { return <p aria-live="polite">Loading product…</p> }\n`,
    'app/products/[slug]/error.tsx': `export default function ErrorPage() { return <p role="alert">The product could not be loaded.</p> }\n`,
    'components/add-to-cart.tsx': `import { useState } from 'react'\n\nexport function AddToCart() {\n  const [added, setAdded] = useState(false)\n  return <button type="button" onClick={() => setAdded(true)}>{added ? 'Added to cart' : 'Add to cart'}</button>\n}\n`,
    'public/portable-react.svg': portableReactImage(),
    'public/social/home.svg': homeSocialImage(),
    'app/products/[slug]/page.go': dynamicPage(modulePath).replaceAll('/portable-react.jpg', '/portable-react.svg'),
    'app/products/[slug]/actions.go': actionHandler(modulePath)
      .replace('(gb.ActionResult[contract.Output], error)', '(contract.Output, error)')
      .replace('return gb.ActionResult[contract.Output]{}, errors.New', 'return contract.Output{}, errors.New')
      .replace('return gb.ActionResult[contract.Output]{Data: contract.Output{Added: true}}, nil', 'return contract.Output{Added: true}, nil'),
    'app/api/products/route.go': apiHandler(),
    'server/cmd/app/main.go': serverMain(modulePath),
  }
}

function managedAgentsBlock() {
  return [
    '# GoBeyond project instructions', '', '<!-- gobeyond:managed:start -->',
    '## GoBeyond rules', '',
    '- Start with `app/`: React owns content, layout, and component composition.',
    '- `page.tsx` alone is static; add its sibling `page.go` only for request-time data, status, metadata, or cache policy.',
    '- Keep route-specific actions in `actions.go` and APIs in `app/api/**/route.go`; keep reusable Go code in ordinary `internal/` packages.',
    '- The runtime imports generated-safe route projections, never `app/` source directories directly.',
    '- Do not build React fragments or duplicate templates in Go.',
    '- Values crossing TypeScript and Go must use a schema-generated contract.',
    '- SEO-critical initial markup must stay in the portable React profile; use explicit `ClientOnly` fallbacks for browser-only widgets.',
    '- Static props and generated route data are public: never put secrets in them.',
    '- Run `pnpm generate` and `pnpm test` after changing routes or contracts.',
    '- Production is Node-free: never add Node/npm/source TypeScript execution to the final server image.', '',
    '## Focused workflows', '',
    'Read the matching `.agents/skills/*/SKILL.md` before adding a page, connecting Go data, actions, APIs, debugging contracts, or changing the AWS reference.',
    '<!-- gobeyond:managed:end -->', '',
  ].join('\n')
}

function runtimeAssetHelpers() {
  return `func loadBrowserAssets(path, buildID string) (*browserassets.Manifest, error) {
  data, err := os.ReadFile(path)
  if errors.Is(err, os.ErrNotExist) { return nil, nil }
  if err != nil { return nil, err }
  var runtimeManifest struct { BuildID string \`json:"buildId"\`; Assets json.RawMessage \`json:"assets"\` }
  if err := json.Unmarshal(data, &runtimeManifest); err != nil { return nil, err }
  if runtimeManifest.BuildID != buildID { return nil, errors.New("runtime asset manifest build ID mismatch") }
  manifest, err := browserassets.Parse(runtimeManifest.Assets)
  if err != nil { return nil, err }
  if manifest.BuildID != buildID { return nil, errors.New("browser asset manifest build ID mismatch") }
  return &manifest, nil
}

func legacyBrowserAssets(buildID string, manifest *browserassets.Manifest) (string, []string) {
  if manifest != nil { return "", nil }
  return buildpaths.AssetURL(buildID, "app.js"), []string{}
}`
}

function portableReactImage() {
  return `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 1200 750" role="img" aria-labelledby="title"><title id="title">Portable React pack</title><rect width="1200" height="750" fill="#efe4c9"/><circle cx="860" cy="190" r="145" fill="#d7a84c"/><path d="M0 620 330 280l220 210 190-235 460 365v130H0z" fill="#2f765f"/><text x="90" y="155" fill="#173f35" font-family="system-ui,sans-serif" font-size="68" font-weight="700">Portable React</text></svg>\n`
}

function homeSocialImage() {
  return `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 1200 630" role="img" aria-labelledby="title"><title id="title">Welcome to GoBeyond</title><rect width="1200" height="630" fill="#173f35"/><path d="M0 540 310 250l180 170 180-230 530 440H0z" fill="#2f765f"/><text x="90" y="170" fill="#fff8e8" font-family="system-ui,sans-serif" font-size="76" font-weight="700">GoBeyond</text></svg>\n`
}

function starterReadme(projectName) {
  return [
    `# ${projectName}`, '',
    'A website-first GoBeyond project. React owns the website; Go joins where a request needs data, mutation, middleware, or an HTTP API.', '',
    '## Run it', '',
    'Install the matching GoBeyond CLI release once, then install this project’s pinned browser/compiler dependencies:', '',
    '```bash',
    `go install github.com/Origens-Dev/gobeyond/cmd/gobeyond@v${GOBEYOND_VERSION}`,
    'pnpm install',
    'pnpm dev',
    '```', '',
    'Open `http://localhost:3000/` or `http://localhost:3000/products/portable-react`. `pnpm dev` watches the project, builds each replacement Go server on a fresh internal port, switches traffic only after readiness succeeds, and reloads the browser. Use `pnpm dev --port 4000` to select another public port. `pnpm serve` starts an existing production build on port 8080.', '',
    '## Environment variables', '',
    '`gobeyond dev` reads `.env`, `.env.development`, `.env.local`, and `.env.development.local`; `gobeyond build` uses the corresponding `production` files. Later files override earlier files, while variables already present in the shell always win. The values are available to the Go build and runtime and to Vite. Only Vite variables whose names start with `VITE_` are included in browser code—keep Contentful tokens and other secrets unprefixed.', '',
    'Tailwind is optional. Start a new Tailwind v4 project with `create-gobeyond --tailwind my-site`; it adds `tailwindcss`, `@tailwindcss/postcss`, a project-owned `postcss.config.mjs`, and the CSS import. Existing projects can opt in by adding those same dependencies and PostCSS config. GoBeyond does not add a Tailwind runtime layer.', '',
    '## What is where', '',
    '- `app/page.tsx`: static React content.',
    '- `app/page.metadata.ts`: build-time document metadata packaged into `static-build.json`.',
    '- `app/products/[slug]/page.tsx`: the React view; this route is static without its sibling Go file.',
    '- `app/products/[slug]/page.go`: request-time props and metadata, using the generated Go contract.',
    '- `app/products/[slug]/actions.go`: typed Go mutation handler beside its browser contract.',
    '- `app/api/products/route.go`: Go HTTP API.',
    '- `internal/`: reusable Go services and policy.',
    '- `server/cmd/app/main.go`: explicit runtime registry. It is the one place Go connects route IDs, loaders, actions, APIs, middleware, and assets. The starter keeps product-scoped middleware inline here; a `server/middleware/middleware.go` file would make Discover promote every `page.tsx` route to dynamic and skip static packaging for `/`.', '',
    'Run `pnpm generate` after changing schemas/routes. It commits the deterministic route registry and Go contracts under `internal/gobeyondgen/`; check them with `pnpm generate:check`.', '',
    'Generation also creates ignored, managed `go.mod` sidecars in route folders so `gopls` can type-check names such as `[slug]`. The production server imports only the safe generated packages.', '',
    '## Production', '',
    'The Dockerfile uses Node and Go only in its build stage. The final scratch image contains only the compiled Go server, rendering plans, runtime data, and manifests—never Node, npm, TypeScript, or browser assets. Upload `dist/static` to your CDN separately.', '',
    'Add a square `app/icon.png` to generate 16/32 favicons and an Apple touch icon. Put authored social cards under `public/social/` and reference them with absolute HTTPS metadata URLs.', '',
    'GoBeyond generates the browser page/layout registry and safe Go route projections during `pnpm build`. The runtime imports those generated projections rather than source directories in `app/`. See `AGENTS.md` for the cross-language rules.', '',
  ].join('\n')
}

function viteConfig() {
  return `import goBeyond from '@go-beyond/vite'\nimport react from '@vitejs/plugin-react'\nimport { defineConfig } from 'vite'\n\nconst buildID = process.env.GOBEYOND_BUILD_ID ?? 'development'\nconst clientEntry = process.env.GOBEYOND_CLIENT_ENTRY ?? 'client.tsx'\nconst outDir = process.env.GOBEYOND_STATIC_OUT ?? \`dist/static/_gobeyond/builds/\${buildID}/assets\`\n\nexport default defineConfig({\n  plugins: [goBeyond(), react()],\n  publicDir: false,\n  build: {\n    outDir,\n    emptyOutDir: false,\n    sourcemap: true,\n    rollupOptions: {\n      input: clientEntry,\n      output: {\n        entryFileNames: 'app.js',\n        chunkFileNames: 'chunks/[name]-[hash].js',\n        assetFileNames: 'assets/[name]-[hash][extname]',\n      },\n    },\n  },\n})\n`
}

function clientEntry() {
  return `import './app/site.css'\nimport { bootstrap } from '@go-beyond/react/browser'\nimport HomePage from './app/page.js'\nimport ProductPage from './app/products/[slug]/page.js'\n\nbootstrap({\n  routes: {\n    r_route_8a5edab2: HomePage,\n    r_products__slug_3e2e8eb9: ProductPage,\n  },\n})\n`
}

function dynamicPage(modulePath) {
  return `// Package products_slug supplies request-time props for app/products/[slug]/page.tsx.\npackage products_slug\n\nimport (\n  \"os\"\n  \"strings\"\n\n  gb \"github.com/Origens-Dev/gobeyond\"\n  contract \"${modulePath}/server/internal/gobeyondgen/contracts/routes/r_products_slug_3e2e8eb9\"\n)\n\n// Page receives only request-time concerns. React remains the source of truth\n// for markup in app/products/[slug]/page.tsx.\nfunc Page(ctx *gb.PageContext) (gb.PageResult[contract.Props], error) {\n  slug := ctx.Params[\"slug\"]\n  if slug != \"portable-react\" {\n    return gb.NotFound[contract.Props](gb.Metadata{Lang: \"en\", Title: \"Product not found\", Robots: \"noindex, nofollow\"}), nil\n  }\n  origin := os.Getenv(\"GOBEYOND_PUBLIC_ORIGIN\")\n  if origin == \"\" { origin = \"http://localhost:8080\" }\n  canonical := origin + \"/products/portable-react\"\n  image := origin + \"/portable-react.jpg\"\n  socialImage := strings.Replace(origin, \"http://\", \"https://\", 1) + \"/portable-react.jpg\"\n  return gb.OK(contract.Props{\n    Name: \"Portable React\", Description: \"Crawler-visible React markup rendered by Go.\",\n    Price: \"$49\", Availability: \"In stock\", ImageURL: image,\n  }, gb.Metadata{\n    Lang: \"en\", Title: \"Portable React\", Description: \"A Go-rendered product page.\", Canonical: canonical, Robots: \"index, follow\",\n    OpenGraph: gb.OpenGraph{Type: \"product\", Title: \"Portable React\", Description: \"A Go-rendered product page.\", URL: canonical, Images: []string{socialImage}},\n    Twitter: gb.Twitter{Card: \"summary_large_image\", Title: \"Portable React\", Description: \"A Go-rendered product page.\", Images: []string{socialImage}},\n    JSONLD: []gb.JSONLD{{\"@context\": \"https://schema.org\", \"@type\": \"Product\", \"name\": \"Portable React\", \"offers\": map[string]any{\"@type\": \"Offer\", \"price\": \"49\", \"priceCurrency\": \"USD\", \"availability\": \"https://schema.org/InStock\"}}},\n  }), nil\n}\n`
    .replaceAll('/server/internal/gobeyondgen/', '/internal/gobeyondgen/')
}

function actionHandler(modulePath) {
  return `// Package products_slug implements the action declared in app/products/[slug]/actions.ts.\npackage products_slug\n\nimport (\n  \"errors\"\n\n  gb \"github.com/Origens-Dev/gobeyond\"\n  contract \"${modulePath}/server/internal/gobeyondgen/contracts/actions/r_products_slug_3e2e8eb9_add_to_cart\"\n)\n\nfunc AddToCart(_ *gb.ActionContext, input contract.Input) (gb.ActionResult[contract.Output], error) {\n  if input.ProductName == \"\" { return gb.ActionResult[contract.Output]{}, errors.New(\"productName is required\") }\n  return gb.ActionResult[contract.Output]{Data: contract.Output{Added: true}}, nil\n}\n`
    .replaceAll('/server/internal/gobeyondgen/', '/internal/gobeyondgen/')
}

function apiHandler() {
  return `// Package products exposes the non-page HTTP API at /api/products.\npackage products\n\nimport (\n  \"net/http\"\n\n  gb \"github.com/Origens-Dev/gobeyond\"\n)\n\nfunc GET(_ *gb.RequestContext) (gb.Response, error) {\n  return gb.Response{Status: http.StatusOK, Headers: http.Header{\"Content-Type\": {\"application/json\"}}, Body: []byte(\`[{\"slug\":\"portable-react\",\"name\":\"Portable React\"}]\`)}, nil\n}\n`
}

function homeMetadata() {
  return `import type { DocumentMetadata } from '@go-beyond/react'
import type { InferPageProps } from '@go-beyond/schema'

import { page } from './page.schema.js'

type Props = InferPageProps<typeof page>

declare const process: { env: Record<string, string | undefined> }

// Packaged into static-build.json. Canonical/social URLs must match the
// runtime PublicOrigin used at serve time (and therefore the build-time
// GOBEYOND_PUBLIC_ORIGIN when you bake a deployment-specific origin).
export function metadata(_props: Props): DocumentMetadata {
  const origin = process.env.GOBEYOND_PUBLIC_ORIGIN ?? 'http://localhost:8080'
  const title = 'Welcome to GoBeyond'
  const description = 'A website-first React application rendered by Go.'
  const canonical = \`\${origin}/\`
  const image = \`\${origin.replace(/^http:\\/\\//, 'https://')}/social/home.svg\`
  return {
    lang: 'en',
    title,
    description,
    canonical,
    robots: 'index, follow',
    openGraph: { type: 'website', title, description, url: canonical, images: [image] },
    twitter: { card: 'summary_large_image', title, description, images: [image] },
  }
}
`
}

// serverMain emits the starter's explicit runtime registry. It mirrors the
// examples/seo-site production entrypoint: packaged static props and contracts
// from GOBEYOND_RUNTIME_DATA_DIR, the supported cache constructor, generated
// contract constants for route ISR, and the shared static file handler.
//
// Deliberately omit server/middleware/middleware.go: Discover treats that file
// as promoting every page.tsx route to dynamic, which skips static packaging
// for /. Product-scoped request-ID middleware stays inline below instead.
function serverMain(modulePath) {
  return `// Command app starts the generated GoBeyond production server.
package main

import (
  "context"
  "encoding/json"
  "errors"
  "log"
  "net/http"
  "os"
  "os/signal"
  "path/filepath"
  "syscall"
  "time"

  gb "github.com/Origens-Dev/gobeyond"
  browserassets "github.com/Origens-Dev/gobeyond/browserassets"
  "github.com/Origens-Dev/gobeyond/buildpaths"
  cacheenv "github.com/Origens-Dev/gobeyond/cache/openfromenv"
  gbmiddleware "github.com/Origens-Dev/gobeyond/middleware"
  "github.com/Origens-Dev/gobeyond/renderplan"
  "github.com/Origens-Dev/gobeyond/router"
  gbruntime "github.com/Origens-Dev/gobeyond/runtime"
  productsapi "${modulePath}/internal/gobeyondgen/api/r_api_products_3637094a"
  actioncontract "${modulePath}/internal/gobeyondgen/contracts/actions/r_products_slug_3e2e8eb9_add_to_cart"
  productcontract "${modulePath}/internal/gobeyondgen/contracts/routes/r_products_slug_3e2e8eb9"
  routes "${modulePath}/internal/gobeyondgen/routes"
  productroute "${modulePath}/internal/gobeyondgen/routes/r_products__slug_3e2e8eb9"
)

func main() {
  origin := env("GOBEYOND_PUBLIC_ORIGIN", "http://localhost:8080")
  planDirectory := env("GOBEYOND_PLAN_DIR", "dist/server/render-plans")
  staticDirectory := env("GOBEYOND_STATIC_DIR", "dist/static")
  runtimeDataDirectory := env("GOBEYOND_RUNTIME_DATA_DIR", filepath.Join(filepath.Dir(planDirectory), "runtime-data"))
  plans, err := loadPlans(planDirectory, routes.RouteRoot, routes.RouteProductsSlug)
  if err != nil { log.Fatal(err) }
  browserAssets, err := loadBrowserAssets(filepath.Join(filepath.Dir(planDirectory), "runtime-manifest.json"), routes.BuildID)
  if err != nil { log.Fatal(err) }
  legacyClientScript, legacyStyles := legacyBrowserAssets(routes.BuildID, browserAssets)

  // Packaged build data: static route props for static pages and soft
  // navigation, plus the value contracts cached props are decoded against.
  staticStore, err := gbruntime.LoadStaticStore(filepath.Join(runtimeDataDirectory, "static-build.json"), filepath.Join(runtimeDataDirectory, "contracts.json"))
  if err != nil { log.Fatal(err) }

  // Bounded in-process L1, plus a shared Redis L2 and its tag-bump watcher
  // when the deployment injects GOBEYOND_CACHE_*.
  cacheConfig, closeCache, err := cacheenv.OpenFromEnv()
  if err != nil { log.Fatal(err) }
  defer closeCache()

  server, err := gbruntime.New(gbruntime.Config{
    BuildID: routes.BuildID, PublicOrigin: origin, BrowserAssets: browserAssets,
    Cache: cacheConfig, Contracts: staticStore.Contracts(),
    Pages: []gbruntime.PageRoute{
      // / stays ModeStatic (no page.go, no server/middleware/middleware.go),
      // so Discover packages home props/metadata into static-build.json.
      {Route: router.Route{ID: routes.RouteRoot, Pattern: "/", Mode: router.ModeStatic}, Plan: plans[routes.RouteRoot], Load: staticStore.Loader(routes.RouteRoot), Indexable: true, ClientScript: legacyClientScript, Styles: legacyStyles},
      // Revalidate/Tags come from app/products/[slug]/page.schema.ts through
      // the generated contract, so the schema stays the single source of truth.
      {Route: router.Route{ID: routes.RouteProductsSlug, Pattern: "/products/[slug]", Mode: router.ModeDynamic}, Plan: plans[routes.RouteProductsSlug], Load: productLoader, Revalidate: productcontract.Revalidate, Tags: productcontract.Tags, Indexable: true, ClientScript: legacyClientScript, Styles: legacyStyles},
    },
    Actions: []gbruntime.Action{actioncontract.Register(productroute.AddToCart)},
    APIs: []gbruntime.APIRoute{{Route: router.Route{ID: "api_products", Pattern: "/api/products", Mode: router.ModeAPI}, Methods: map[string]gb.Handler{http.MethodGet: productsapi.GET}}},
    // Inline and product-scoped: a server/middleware/middleware.go file would
    // make Discover promote every static page.tsx route to dynamic.
    Middleware: []gbmiddleware.Rule{{
      Name: "starter-request-id",
      Config: gb.MiddlewareConfig{Patterns: []string{"/products/[slug]"}},
      Middleware: func(next gb.Handler) gb.Handler {
        return func(ctx *gb.RequestContext) (gb.Response, error) {
          ctx.Values["source"] = "starter"
          return next(ctx)
        }
      },
    }},
  })
  if err != nil { log.Fatal(err) }

  // Serves dist/static with gzip and immutable Cache-Control for
  // content-addressed build artifacts; everything else falls through.
  handler := gbruntime.StaticFiles(staticDirectory, server)
  httpServer := &http.Server{Addr: env("GOBEYOND_ADDR", ":8080"), Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 20 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 1 << 20}
  stopping, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
  defer stop()
  go func() { <-stopping.Done(); ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second); defer cancel(); _ = httpServer.Shutdown(ctx) }()
  if err := httpServer.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) { log.Fatal(err) }
}

func productLoader(ctx *gb.PageContext) (gbruntime.LoadedPage, error) {
  result, err := productroute.Page(ctx)
  return gbruntime.LoadedPage{Kind: result.Kind, Props: result.Props, Metadata: result.Metadata, Status: result.Status, Cache: result.Cache, RedirectTo: result.RedirectTo, ErrorCode: result.ErrorCode, Message: result.Message}, err
}

func loadPlans(directory string, routeIDs ...string) (map[string]*renderplan.Plan, error) {
  plans := make(map[string]*renderplan.Plan, len(routeIDs))
  for _, routeID := range routeIDs {
    source, err := os.ReadFile(filepath.Join(directory, routeID+".json"))
    if err != nil { return nil, err }
    plan, err := renderplan.Parse(source)
    if err != nil { return nil, err }
    plans[routeID] = plan
  }
  return plans, nil
}

${runtimeAssetHelpers()}

func env(key, fallback string) string { if value := os.Getenv(key); value != "" { return value }; return fallback }
`
}

function dockerfile() {
  return `# Node and Go exist only in this build stage.\nFROM golang:1.24-alpine AS build\nARG GOBEYOND_VERSION=${GOBEYOND_VERSION}\nRUN apk add --no-cache nodejs npm && corepack enable\nRUN go install github.com/Origens-Dev/gobeyond/cmd/gobeyond@v${GOBEYOND_VERSION}\nWORKDIR /src\nCOPY package.json pnpm-lock.yaml* ./\nRUN pnpm install --frozen-lockfile\nCOPY . .\nRUN gobeyond build\n\n# Production is deliberately Node-free.\nFROM scratch\nCOPY --from=build /src/dist/server/gobeyond-server /gobeyond-server\n# dist/server carries render plans and runtime-data (static-build.json,\n# contracts.json) the server loads at startup.\nCOPY --from=build /src/dist/server /app/dist/server\nCOPY --from=build /src/dist/static /app/dist/static\nENV GOBEYOND_PLAN_DIR=/app/dist/server/render-plans\nENV GOBEYOND_STATIC_DIR=/app/dist/static\nEXPOSE 8080\nENTRYPOINT [\"/gobeyond-server\"]\n`
}

function workflow() {
  return `name: verify\non:\n  push:\n  pull_request:\njobs:\n  verify:\n    runs-on: ubuntu-latest\n    steps:\n      - uses: actions/checkout@v4\n      - uses: pnpm/action-setup@v4\n        with:\n          version: 10\n      - uses: actions/setup-node@v4\n        with:\n          node-version: 22\n          cache: pnpm\n      - uses: actions/setup-go@v5\n        with:\n          go-version: '1.24.x'\n      - run: go install github.com/Origens-Dev/gobeyond/cmd/gobeyond@v${GOBEYOND_VERSION}\n      - run: pnpm install --frozen-lockfile\n      - run: pnpm test\n      - run: pnpm build\n      - run: test ! -e dist/server/node_modules\n`
}
