import assert from 'node:assert/strict'
import { access, mkdtemp, mkdir, readFile, rm, symlink } from 'node:fs/promises'
import { spawn } from 'node:child_process'
import { tmpdir } from 'node:os'
import { dirname, join, resolve } from 'node:path'
import test from 'node:test'

import { CreateProjectError, createProject } from '../src/scaffold.js'

const workspaceRoot = resolve(new URL('../../..', import.meta.url).pathname)

test('scaffolds an internally consistent website-first hello world', async () => {
  const root = await mkdtemp(join(tmpdir(), 'create-gobeyond-'))
  const destination = join(root, 'hello')
  await createProject(destination, { projectName: 'hello' })

  const files = [
    'app/page.tsx',
    'app/page.schema.ts',
    'app/site.css',
    'app/products/[slug]/page.tsx',
    'app/products/[slug]/page.schema.ts',
    'app/products/[slug]/actions.ts',
    'client.tsx',
    'vite.config.ts',
    'server/pages/products_slug/page.go',
    'server/actions/products_slug/actions.go',
    'server/api/products/route.go',
    'server/middleware/middleware.go',
    'server/cmd/app/main.go',
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
  assert.equal(packageJSON.dependencies['@gobeyond/react'], '0.1.0-alpha.0')
  assert.equal(packageJSON.dependencies['@gobeyond/schema'], '0.1.0-alpha.0')
  assert.equal(packageJSON.devDependencies['@gobeyond/compiler'], '0.1.0-alpha.0')
  assert.equal(packageJSON.devDependencies['@gobeyond/cli'], undefined)
  assert.doesNotMatch(packageJSON.scripts.test, /gobeyond test/)
  assert.match(packageJSON.scripts.build, /^gobeyond generate && gobeyond build$/)
  assert.match(packageJSON.scripts.generate, /^gobeyond generate$/)
  assert.equal(packageJSON.scripts.dev, 'gobeyond dev')

  const goMod = await readFile(join(destination, 'go.mod'), 'utf8')
  assert.match(goMod, /github\.com\/gobeyond-dev\/gobeyond v0\.1\.0-alpha\.0/)

  const dockerfile = await readFile(join(destination, 'Dockerfile'), 'utf8')
  assert.match(dockerfile, /FROM scratch/)
  assert.match(dockerfile, /Production is deliberately Node-free/)
  assert.match(dockerfile, /RUN pnpm build/)
  assert.doesNotMatch(dockerfile, /COPY --from=build \/src\/dist\/static/)

  const client = await readFile(join(destination, 'client.tsx'), 'utf8')
  assert.match(client, /@gobeyond\/react\/browser/)
  assert.match(client, /r_route_8a5edab2/)
  assert.match(client, /r_products__slug_3e2e8eb9/)
  const vite = await readFile(join(destination, 'vite.config.ts'), 'utf8')
  assert.match(vite, /dedupe: \['react', 'react-dom'\]/)
  const loader = await readFile(join(destination, 'server/pages/products_slug/page.go'), 'utf8')
  assert.match(loader, /contracts\/routes\/r_products_slug_3e2e8eb9/)
  assert.match(loader, /func Page\(ctx \*gb\.PageContext\)/)
  const action = await readFile(join(destination, 'server/actions/products_slug/actions.go'), 'utf8')
  assert.match(action, /contracts\/actions\/r_products_slug_3e2e8eb9_add_to_cart/)
  const main = await readFile(join(destination, 'server/cmd/app/main.go'), 'utf8')
  assert.match(main, /routes\.RouteProductsSlug/)
  assert.match(main, /withStaticAssets/)
  assert.match(main, /productaction\.AddToCart/)
  assert.match(main, /actioncontract\.Register\(productaction\.AddToCart\)/)
  assert.match(main, /Static: home\(origin\)/)
  assert.doesNotMatch(main, /staticStore\.Loader/)
  assert.doesNotMatch(main, /func addToCart\(ctx \*gb\.ActionContext, raw json\.RawMessage\)/)

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

  await run('go', ['mod', 'edit', '-replace', `github.com/gobeyond-dev/gobeyond=${workspaceRoot}`], destination)
  await run('go', ['run', join(workspaceRoot, 'cmd/gobeyond'), 'generate'], destination)
  await run('go', ['mod', 'tidy'], destination)
  await run(join(nodeModules, '.bin', 'tsc'), ['-p', 'tsconfig.json', '--noEmit'], destination)
  await run('go', ['test', './...'], destination)
  await run('go', ['run', join(workspaceRoot, 'cmd/gobeyond'), 'build'], destination)

  const generatedContract = join(destination, 'server/internal/gobeyondgen/contracts/routes/r_products_slug_3e2e8eb9/types.gobeyond_gen.go')
  await access(generatedContract)
  await access(join(destination, 'dist/server/gobeyond-server'))
  const runtimeManifest = JSON.parse(await readFile(join(destination, 'dist/server/runtime-manifest.json'), 'utf8'))
  await access(join(destination, 'dist/static/_gobeyond/assets', runtimeManifest.buildId, 'app.js'))
  const response = await serveAndFetch(join(destination, 'dist/server/gobeyond-server'), destination)
  assert.equal(response.rootStatus, 200)
  assert.match(response.rootHTML, /<h1>Welcome to GoBeyond<\/h1>/)
  assert.equal(response.status, 200)
  assert.match(response.html, /<h1>Portable React<\/h1>/)
  assert.match(response.html, /rel="canonical"/)
  assert.match(response.stylesheetURL, /^\/_gobeyond\/assets\/[^/]+\/assets\/[^/]+\.css$/)
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

function run(command, args, cwd) {
  return new Promise((resolvePromise, reject) => {
    const child = spawn(command, args, { cwd, stdio: 'pipe' })
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
  await mkdir(join(nodeModules, '@gobeyond'), { recursive: true })
  await mkdir(join(nodeModules, '@types'), { recursive: true })
  await mkdir(join(nodeModules, '@vitejs'), { recursive: true })
  await mkdir(join(nodeModules, '.bin'), { recursive: true })
  for (const [name, source] of Object.entries({
    compiler: join(workspaceRoot, 'packages/compiler'),
    react: join(workspaceRoot, 'packages/react'),
    schema: join(workspaceRoot, 'packages/schema'),
  })) {
    await symlink(source, join(nodeModules, '@gobeyond', name), 'dir')
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

async function serveAndFetch(binary, cwd) {
  const address = '127.0.0.1:18887'
  const child = spawn(binary, [], {
    cwd,
    env: { ...process.env, GOBEYOND_ADDR: address, GOBEYOND_PUBLIC_ORIGIN: `http://${address}` },
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
