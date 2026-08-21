import assert from 'node:assert/strict'
import { access, copyFile, mkdtemp, mkdir, readFile, rm, symlink } from 'node:fs/promises'
import { spawn } from 'node:child_process'
import { tmpdir } from 'node:os'
import { dirname, join, resolve } from 'node:path'
import test from 'node:test'

import { CreateProjectError, createProject } from '../src/scaffold.js'

const workspaceRoot = resolve(new URL('../../..', import.meta.url).pathname)
const gobeyondVersion = JSON.parse(
  await readFile(new URL('../package.json', import.meta.url), 'utf8'),
).version

test('scaffolds an internally consistent GoBeyond hello world', async () => {
  const root = await mkdtemp(join(tmpdir(), 'create-gobeyond-'))
  const destination = join(root, 'hello')
  await createProject(destination, { projectName: 'hello' })

  const files = [
    'app/page.tsx',
    'app/page.schema.ts',
    'app/page.metadata.ts',
    'app/site.css',
    'app/products/[slug]/page.tsx',
    'app/products/[slug]/page.schema.ts',
    'app/products/[slug]/actions.ts',
    'client.tsx',
    'vite.config.ts',
    'app/products/[slug]/page.go',
    'app/products/[slug]/actions.go',
    'app/api/products/route.go',
    'gobeyond.json',
    'middleware.go',
    'public/portable-react.svg',
    'public/social/home.svg',
    'Dockerfile',
    '.github/workflows/verify.yml',
    'AGENTS.md',
  ]
  for (const file of files) {
    const contents = await readFile(join(destination, file), 'utf8')
    assert.ok(contents.length > 0, `${file} should not be empty`)
  }

  const packageJSON = JSON.parse(await readFile(join(destination, 'package.json'), 'utf8'))
  assert.equal(packageJSON.packageManager, 'pnpm@10.33.0')
  assert.equal(packageJSON.dependencies.react, '19.2.8')
  assert.equal(packageJSON.dependencies['react-dom'], '19.2.8')
  assert.equal(packageJSON.dependencies['@go-beyond/react'], gobeyondVersion)
  assert.equal(packageJSON.dependencies['@go-beyond/schema'], gobeyondVersion)
  assert.equal(packageJSON.devDependencies['@go-beyond/compiler'], gobeyondVersion)
  assert.equal(packageJSON.devDependencies['@go-beyond/vite'], gobeyondVersion)
  assert.equal(packageJSON.devDependencies['@go-beyond/cli'], undefined)
  assert.equal(packageJSON.devDependencies.tailwindcss, undefined)
  assert.equal(packageJSON.devDependencies['@tailwindcss/postcss'], undefined)
  assert.doesNotMatch(packageJSON.scripts.test, /gobeyond test/)
  assert.equal(packageJSON.scripts.build, 'gobeyond build')
  assert.match(packageJSON.scripts.generate, /^gobeyond generate$/)
  assert.equal(packageJSON.scripts.dev, 'gobeyond dev')
  assert.equal(packageJSON.scripts.serve, 'gobeyond preview')
  assert.equal(packageJSON.scripts.preview, 'gobeyond preview')

  const gitignore = await readFile(join(destination, '.gitignore'), 'utf8')
  assert.match(gitignore, /^\.env\.local$/m)
  assert.match(gitignore, /^\.env\.\*\.local$/m)
  assert.match(gitignore, /generated\/workflows/)
  assert.match(gitignore, /generated\/agents/)
  assert.doesNotMatch(gitignore, /generated\/workers/)

  const goMod = await readFile(join(destination, 'go.mod'), 'utf8')
  assert.ok(
    goMod.includes(`github.com/Origens-Dev/gobeyond v${gobeyondVersion}`),
    `go.mod should require gobeyond v${gobeyondVersion}:\n${goMod}`,
  )

  const dockerfile = await readFile(join(destination, 'Dockerfile'), 'utf8')
  assert.match(dockerfile, /FROM scratch/)
  assert.match(dockerfile, /Production is deliberately Node-free/)
  assert.match(dockerfile, /RUN pnpm build/)
  assert.doesNotMatch(dockerfile, /COPY --from=build \/src\/dist\/static/)
  assert.match(dockerfile, /ENV GOBEYOND_PLAN_PACK=\/app\/dist\/server\/render-plans\.gbp/)
  assert.match(dockerfile, /ENV GOBEYOND_STATIC_PACK=\/app\/dist\/server\/runtime-data\/static-build\.gbs/)
  assert.doesNotMatch(dockerfile, /GOBEYOND_PLAN_DIR/)

  const client = await readFile(join(destination, 'client.tsx'), 'utf8')
  assert.match(client, /@go-beyond\/react\/browser/)
  assert.match(client, /r_route_8a5edab2/)
  assert.match(client, /r_products__slug_3e2e8eb9/)
  const vite = await readFile(join(destination, 'vite.config.ts'), 'utf8')
  assert.match(vite, /dedupe: \['react', 'react-dom'\]/)
  assert.match(vite, /sourcemap: false/)
  assert.match(vite, /chunkFileNames: 'chunks\/\[hash\]\.js'/)
  const tsconfig = JSON.parse(await readFile(join(destination, 'tsconfig.json'), 'utf8'))
  assert.ok(!tsconfig.include.includes('middleware.ts'))
  const loader = await readFile(join(destination, 'app/products/[slug]/page.go'), 'utf8')
  assert.match(loader, /type Props struct/)
  assert.match(loader, /var Config = gb\.PageConfig/)
  assert.match(loader, /func Page\(ctx \*gb\.PageContext\)/)
  const action = await readFile(join(destination, 'app/products/[slug]/actions.go'), 'utf8')
  assert.match(action, /contracts\/actions\/r_products_slug_3e2e8eb9_add_to_cart/)
  const middleware = await readFile(join(destination, 'middleware.go'), 'utf8')
  assert.match(middleware, /package middleware/)
  assert.match(middleware, /func Middleware\(next gb\.Handler\) gb\.Handler/)
  const proxyPolicy = JSON.parse(await readFile(join(destination, 'gobeyond.json'), 'utf8'))
  assert.equal(proxyPolicy.apiVersion, 'gobeyond.proxy-policy/v1alpha1')
  assert.equal(proxyPolicy.redirects[0].status, 308)
  const gitignoreFull = await readFile(join(destination, '.gitignore'), 'utf8')
  assert.match(gitignoreFull, /generated\/cmd\//)

  const homeMetadata = await readFile(join(destination, 'app/page.metadata.ts'), 'utf8')
  assert.match(homeMetadata, /export function metadata/)
  assert.match(homeMetadata, /GOBEYOND_PUBLIC_ORIGIN/)

  const productSchema = await readFile(join(destination, 'app/products/[slug]/page.schema.ts'), 'utf8')
  assert.match(productSchema, /revalidate: 60/)
  assert.match(productSchema, /tags: \['products'\]/)

  const agents = await readFile(join(destination, 'AGENTS.md'), 'utf8')
  assert.match(agents, /React owns content/)
  assert.match(agents, /Production is Node-free/)
})

test('local workspace integration generates contracts and type-checks the starter without network', async (t) => {
  const nodeModules = join(workspaceRoot, 'node_modules')
  const cli = join(workspaceRoot, 'cmd/gobeyond')
  try {
    await access(nodeModules)
    await access(cli)
  } catch {
    t.skip('workspace dependencies are not available')
    return
  }

  // Make only the package links a published install would expose. This makes
  // the Go CLI's project-local compiler lookup exercise the public shape
  // without fetching anything from a registry.
  const destination = join(workspaceRoot, '.tmp-create-gobeyond-integration')
  await rm(destination, { recursive: true, force: true })
  t.after(async () => { await rm(destination, { recursive: true, force: true }) })
  await createProject(destination, { projectName: 'starter-integration' })
  await linkWorkspacePackages(destination)

  await run('go', ['mod', 'edit', '-replace', `github.com/Origens-Dev/gobeyond=${workspaceRoot}`], destination)
  // Seed the starter with the workspace go.sum so the replaced module's own
  // dependencies (for example the pack container's zstd codec) resolve from
  // the local module cache without touching the network; the later
  // `go mod tidy` prunes it back down to what the starter actually uses.
  await copyFile(join(workspaceRoot, 'go.sum'), join(destination, 'go.sum'))
  await run('go', ['run', join(workspaceRoot, 'cmd/gobeyond'), 'generate'], destination)
  await run('go', ['mod', 'tidy'], destination)
  await run(join(nodeModules, '.bin', 'tsc'), ['-p', 'tsconfig.json', '--noEmit'], destination)
  await run('go', ['test', './...'], destination)
  // Bake home metadata for the ephemeral listen origin so packaged canonical
  // URLs match runtime PublicOrigin / AllowedHosts.
  const serveOrigin = 'http://localhost:18887'
  await run('go', ['run', join(workspaceRoot, 'cmd/gobeyond'), 'build'], destination, {
    GOBEYOND_PUBLIC_ORIGIN: serveOrigin,
  })

  const generatedContract = join(destination, 'generated/contracts/routes/r_products_slug_3e2e8eb9/types.gobeyond_gen.go')
  await access(generatedContract)
  await access(join(destination, 'dist/server/gobeyond-server'))
  const runtimeManifest = JSON.parse(await readFile(join(destination, 'dist/server/runtime-manifest.json'), 'utf8'))
  await access(join(destination, 'dist/static/_gobeyond/builds', runtimeManifest.buildId, 'assets', 'app.js'))
  const response = await serveAndFetch(join(destination, 'dist/server/gobeyond-server'), destination, serveOrigin)
  assert.equal(response.rootStatus, 200)
  assert.match(response.rootHTML, /<h1>Welcome to GoBeyond<\/h1>/)
  assert.equal(response.status, 200)
  assert.match(response.html, /<h1>Portable React<\/h1>/)
  assert.match(response.html, /rel="canonical"/)
  assert.match(response.stylesheetURL, /^\/_gobeyond\/builds\/[^/]+\/assets\/assets\/[^/]+\.css$/)
  assert.equal(response.stylesheetStatus, 200)
  assert.match(response.stylesheet, /:root/)
  assert.equal(response.imageStatus, 200)
  assert.match(response.image, /^<svg/)
})

test('never overwrites a non-empty destination', async () => {
  const root = await mkdtemp(join(tmpdir(), 'create-gobeyond-'))
  const destination = join(root, 'already-there')
  await mkdir(destination)
  await createProject(destination)
  await assert.rejects(
    () => createProject(destination),
    (error) => error instanceof CreateProjectError && /refusing to overwrite/.test(error.message),
  )
})

test('Tailwind v4 is an explicit scaffold option with project-owned PostCSS', async () => {
  const root = await mkdtemp(join(tmpdir(), 'create-gobeyond-tailwind-'))
  const destination = join(root, 'tailwind-site')
  await createProject(destination, { projectName: 'tailwind-site', tailwind: true })

  const packageJSON = JSON.parse(await readFile(join(destination, 'package.json'), 'utf8'))
  assert.match(packageJSON.devDependencies.tailwindcss, /^\^4\./)
  assert.match(packageJSON.devDependencies['@tailwindcss/postcss'], /^\^4\./)
  const postcss = await readFile(join(destination, 'postcss.config.mjs'), 'utf8')
  assert.match(postcss, /@tailwindcss\/postcss/)
  const css = await readFile(join(destination, 'app/site.css'), 'utf8')
  assert.match(css, /@import "tailwindcss"/)
})

function run(command, args, cwd, env = {}) {
  return new Promise((resolvePromise, reject) => {
    const child = spawn(command, args, { cwd, env: { ...process.env, ...env }, stdio: 'pipe' })
    let output = ''
    child.stdout.on('data', (chunk) => { output += chunk })
    child.stderr.on('data', (chunk) => { output += chunk })
    child.on('error', reject)
    child.on('close', (code) => {
      if (code === 0) {
        resolvePromise()
        return
      }
      reject(new Error(`${command} ${args.join(' ')} failed with ${code}:\n${output}`))
    })
  })
}

async function linkWorkspacePackages(destination) {
  const nodeModules = join(destination, 'node_modules')
  await mkdir(join(nodeModules, '@go-beyond'), { recursive: true })
  await mkdir(join(nodeModules, '@types'), { recursive: true })
  await mkdir(join(nodeModules, '@vitejs'), { recursive: true })
  await mkdir(join(nodeModules, '.bin'), { recursive: true })
  for (const [name, source] of Object.entries({
    compiler: join(workspaceRoot, 'packages/compiler'),
    react: join(workspaceRoot, 'packages/react'),
    schema: join(workspaceRoot, 'packages/schema'),
    vite: join(workspaceRoot, 'packages/vite'),
  })) {
    await symlink(source, join(nodeModules, '@go-beyond', name), 'dir')
  }
  for (const [name, source] of Object.entries({
    react: join(workspaceRoot, 'packages/react/node_modules/react'),
    'react-dom': join(workspaceRoot, 'packages/react/node_modules/react-dom'),
    '@types/react': join(workspaceRoot, 'packages/react/node_modules/@types/react'),
    '@types/react-dom': join(workspaceRoot, 'packages/react/node_modules/@types/react-dom'),
  })) {
    const target = name.startsWith('@types/')
      ? join(nodeModules, '@types', name.slice('@types/'.length))
      : join(nodeModules, name)
    await symlink(source, target, 'dir')
  }
  await symlink(join(workspaceRoot, 'node_modules/typescript'), join(nodeModules, 'typescript'), 'dir')
  await symlink(join(workspaceRoot, 'examples/seo-site/node_modules/@vitejs/plugin-react'), join(nodeModules, '@vitejs/plugin-react'), 'dir')
  await symlink(join(workspaceRoot, 'examples/seo-site/node_modules/vite'), join(nodeModules, 'vite'), 'dir')
  await symlink(join(workspaceRoot, 'node_modules/.bin/tsc'), join(nodeModules, '.bin/tsc'))
  await symlink(join(workspaceRoot, 'node_modules/.bin/vite'), join(nodeModules, '.bin/vite'))
}

async function serveAndFetch(binary, cwd, publicOrigin) {
  const address = new URL(publicOrigin).host
  const child = spawn(binary, [], {
    cwd,
    env: {
      ...process.env,
      GOBEYOND_ADDR: address,
      GOBEYOND_PUBLIC_ORIGIN: publicOrigin,
    },
    stdio: 'pipe',
  })
  let output = ''
  child.stdout.on('data', (chunk) => { output += chunk })
  child.stderr.on('data', (chunk) => { output += chunk })
  try {
    for (let attempt = 0; attempt < 30; attempt += 1) {
      try {
        const rootResponse = await fetch(`http://${address}/`)
        const rootHTML = await rootResponse.text()
        const response = await fetch(`http://${address}/products/portable-react`)
        const html = await response.text()
        const stylesheetURL = html.match(/<link rel="stylesheet" href="([^"]+\.css)">/)?.[1] ?? ''
        const stylesheetResponse = await fetch(`http://${address}${stylesheetURL}`)
        const imageResponse = await fetch(`http://${address}/portable-react.svg`)
        return {
          rootStatus: rootResponse.status,
          rootHTML,
          status: response.status,
          html,
          stylesheetURL,
          stylesheetStatus: stylesheetResponse.status,
          stylesheet: await stylesheetResponse.text(),
          imageStatus: imageResponse.status,
          image: await imageResponse.text(),
        }
      } catch {
        await new Promise((resolvePromise) => setTimeout(resolvePromise, 100))
      }
    }
    throw new Error(`starter server did not become ready:\n${output}`)
  } finally {
    child.kill('SIGTERM')
  }
}
