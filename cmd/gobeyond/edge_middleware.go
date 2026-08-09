package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"

	"github.com/Origens-Dev/gobeyond/buildpaths"
)

// buildEdgeMiddleware bundles the one root middleware.ts/js authoring entry
// into a provider-neutral module-worker artifact. Authors export one default
// function; deployment adapters receive the conventional default { fetch }
// module shape.
func buildEdgeMiddleware(root, website, dist, source string, environment []string, mode string) error {
	if source == "" {
		return nil
	}
	generatedRoot := filepath.Join(website, ".gobeyond")
	if err := os.MkdirAll(generatedRoot, 0o755); err != nil {
		return err
	}
	temporary, err := os.MkdirTemp(generatedRoot, "middleware-build-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(temporary)

	absoluteSource, err := filepath.Abs(source)
	if err != nil {
		return err
	}
	wrapper := fmt.Sprintf(`import middleware from %s

if (typeof middleware !== 'function') {
  throw new TypeError('middleware.ts/js must default-export a function')
}

export default {
  fetch(request) {
    return middleware(request)
  },
}
`, strconv.Quote(filepath.ToSlash(absoluteSource)))
	wrapperPath := filepath.Join(temporary, "entry.mjs")
	if err := os.WriteFile(wrapperPath, []byte(wrapper), 0o644); err != nil {
		return err
	}
	outputDirectory := filepath.Join(dist, buildpaths.EdgeMiddlewareDir)
	config := fmt.Sprintf(`export default {
  build: {
    target: 'es2022',
    outDir: %s,
    emptyOutDir: true,
    minify: false,
    sourcemap: false,
    lib: {
      entry: %s,
      formats: ['es'],
      fileName: () => %s,
    },
    rolldownOptions: {
      output: { codeSplitting: false },
    },
  },
}
`, strconv.Quote(filepath.ToSlash(outputDirectory)), strconv.Quote(filepath.ToSlash(wrapperPath)), strconv.Quote(buildpaths.EdgeMiddlewareEntryName))
	configPath := filepath.Join(temporary, "vite.config.mjs")
	if err := os.WriteFile(configPath, []byte(config), 0o644); err != nil {
		return err
	}

	vite := filepath.Join(root, "node_modules", ".bin", "vite")
	if _, err := os.Stat(vite); err != nil {
		vite = filepath.Join(website, "node_modules", ".bin", "vite")
	}
	command := exec.Command(vite, "build", "--config", configPath, "--mode", mode)
	command.Dir = website
	command.Env = withEnvironment(environment, "NODE_ENV="+browserNodeEnvironment(mode))
	command.Stdout, command.Stderr = os.Stdout, os.Stderr
	if err := command.Run(); err != nil {
		return fmt.Errorf("bundle middleware: %w", err)
	}
	entry := filepath.Join(outputDirectory, buildpaths.EdgeMiddlewareEntryName)
	if info, err := os.Stat(entry); err != nil || !info.Mode().IsRegular() {
		if err == nil {
			err = errors.New("output is not a regular file")
		}
		return fmt.Errorf("middleware bundle is missing: %w", err)
	}
	return nil
}

func edgeMiddlewareBundle(buildDirectory string) (string, bool, error) {
	entry := filepath.Join(buildDirectory, buildpaths.EdgeMiddlewareDir, buildpaths.EdgeMiddlewareEntryName)
	info, err := os.Stat(entry)
	if errors.Is(err, os.ErrNotExist) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	if !info.Mode().IsRegular() {
		return "", false, errors.New("built edge middleware entry must be a regular file")
	}
	return entry, true, nil
}

func edgeMiddlewareCommand(ctx context.Context, root, entry, address, upstream, publicOrigin string, environment []string) *exec.Cmd {
	command := exec.CommandContext(ctx, "node", "--input-type=module", "--eval", edgeMiddlewareRunnerSource)
	command.Dir = root
	command.Env = withEnvironment(environment,
		"GOBEYOND_MIDDLEWARE_ENTRY="+entry,
		"GOBEYOND_MIDDLEWARE_ADDR="+address,
		"GOBEYOND_MIDDLEWARE_UPSTREAM="+upstream,
		"GOBEYOND_PUBLIC_ORIGIN="+publicOrigin,
	)
	command.Stdout, command.Stderr = os.Stdout, os.Stderr
	return command
}

const edgeMiddlewareRunnerSource = `
import http from 'node:http'
import { Readable } from 'node:stream'
import { pathToFileURL } from 'node:url'

const entry = process.env.GOBEYOND_MIDDLEWARE_ENTRY
const address = process.env.GOBEYOND_MIDDLEWARE_ADDR
const upstream = new URL(process.env.GOBEYOND_MIDDLEWARE_UPSTREAM)
const publicOrigin = new URL(process.env.GOBEYOND_PUBLIC_ORIGIN)
if (!entry || !address) throw new Error('middleware runner configuration is incomplete')

const nativeFetch = globalThis.fetch
const decodedFetchResponses = new WeakSet()
async function trackedFetch(request) {
  const response = await nativeFetch(request)
  if (response.headers.has('content-encoding')) decodedFetchResponses.add(response)
  return response
}
globalThis.fetch = async (input, init) => {
  const request = input instanceof Request
    ? (init === undefined ? input : new Request(input, init))
    : new Request(new URL(String(input), publicOrigin), init)
  const destination = new URL(request.url)
  if (destination.origin !== publicOrigin.origin) return trackedFetch(request)
  const target = new URL(destination.pathname + destination.search, upstream)
  const headers = new Headers(request.headers)
  const originInit = {
    method: request.method,
    headers,
    redirect: 'manual',
    signal: request.signal,
  }
  if (request.method !== 'GET' && request.method !== 'HEAD') {
    originInit.body = request.body
    originInit.duplex = 'half'
  }
  return trackedFetch(new Request(target, originInit))
}

const loaded = await import(pathToFileURL(entry).href)
const worker = loaded.default
if (!worker || typeof worker.fetch !== 'function') {
  throw new TypeError('built middleware must default-export an object with fetch(request)')
}

function parseAddress(value) {
  const separator = value.lastIndexOf(':')
  if (separator < 0) throw new Error('middleware address must include a port')
  let host = value.slice(0, separator)
  if (host.startsWith('[') && host.endsWith(']')) host = host.slice(1, -1)
  if (!host) host = '0.0.0.0'
  const port = Number(value.slice(separator + 1))
  if (!Number.isInteger(port) || port < 1 || port > 65535) throw new Error('middleware port is invalid')
  return { host, port }
}

function incomingRequest(request) {
  const controller = new AbortController()
  request.once('aborted', () => controller.abort())
  const headers = new Headers()
  for (let index = 0; index < request.rawHeaders.length; index += 2) {
    headers.append(request.rawHeaders[index], request.rawHeaders[index + 1])
  }
  const init = {
    method: request.method,
    headers,
    redirect: 'manual',
    signal: controller.signal,
  }
  if (request.method !== 'GET' && request.method !== 'HEAD') {
    init.body = Readable.toWeb(request)
    init.duplex = 'half'
  }
  return new Request(new URL(request.url, publicOrigin), init)
}

async function writeResponse(response, output) {
  if (!(response instanceof Response)) throw new TypeError('middleware must return a Response')
  output.statusCode = response.status
  output.statusMessage = response.statusText
  const cookies = typeof response.headers.getSetCookie === 'function' ? response.headers.getSetCookie() : []
  const decoded = decodedFetchResponses.has(response)
  for (const [name, value] of response.headers) {
    if (name === 'set-cookie' || name === 'connection' || name === 'transfer-encoding') continue
    if (decoded && (name === 'content-encoding' || name === 'content-length')) continue
    output.setHeader(name, value)
  }
  if (cookies.length > 0) output.setHeader('set-cookie', cookies)
  if (response.body === null || output.req.method === 'HEAD') {
    output.end()
    return
  }
  await new Promise((resolve, reject) => {
    const body = Readable.fromWeb(response.body)
    body.once('error', reject)
    output.once('error', reject)
    output.once('finish', resolve)
    body.pipe(output)
  })
}

const server = http.createServer(async (input, output) => {
  try {
    const request = incomingRequest(input)
    const response = request.url.endsWith('/__gobeyond/readyz')
      ? await globalThis.fetch(request)
      : await worker.fetch(request, Object.freeze({}), Object.freeze({
          waitUntil(promise) { Promise.resolve(promise).catch((error) => console.error('middleware waitUntil:', error)) },
          passThroughOnException() {},
        }))
    await writeResponse(response, output)
  } catch (error) {
    console.error('GoBeyond middleware request failed:', error)
    if (!output.headersSent) {
      output.statusCode = 500
      output.setHeader('content-type', 'text/plain; charset=utf-8')
    }
    output.end('Middleware request failed')
  }
})
server.on('clientError', (_error, socket) => socket.end('HTTP/1.1 400 Bad Request\r\n\r\n'))
server.listen(parseAddress(address))
`
