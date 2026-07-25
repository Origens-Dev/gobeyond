#!/usr/bin/env node

import { readFile, writeFile } from 'node:fs/promises'
import { dirname, isAbsolute, resolve } from 'node:path'

import {
  buildPortabilityReport,
  compileFile,
  compileProject,
  formatDiagnostics,
  formatPortabilityReport,
  type ClientBoundaryManifest,
  type CompileProjectOptions,
  type RenderPlan,
  type SourceRoot,
} from './index.js'

function usage(): never {
  process.stderr.write(`usage:
  gobeyond-compile --route <route-id> [--project-root <dir>] [--source-root <prefix=dir>] [--out <plan.json>] <page.tsx>
  gobeyond-compile --project <project.json> [--out <plans.json>]
  gobeyond-compile report-portability --project <compiler-output.json> [--out <report.json>]
`)
  process.exit(2)
}

const args = process.argv.slice(2)

if (args[0] === 'report-portability') {
  let projectOutputPath: string | undefined
  let outputPath: string | undefined
  for (let index = 1; index < args.length; index += 1) {
    const argument = args[index]!
    if (argument === '--project') {
      projectOutputPath = args[++index]
    } else if (argument === '--out') {
      outputPath = args[++index]
    } else {
      usage()
    }
  }
  if (!projectOutputPath) usage()
  const raw = JSON.parse(await readFile(resolve(projectOutputPath), 'utf8')) as {
    plans?: RenderPlan[]
    clientBoundaries?: ClientBoundaryManifest
  }
  if (!Array.isArray(raw.plans) || !raw.clientBoundaries) {
    process.stderr.write(
      'report-portability expects gobeyond.compiler-project output with plans and clientBoundaries.\n',
    )
    process.exitCode = 1
  } else {
    const report = buildPortabilityReport(
      raw.plans,
      raw.clientBoundaries.boundaries,
    )
    if (outputPath) {
      await writeOutput(report, outputPath)
    } else {
      process.stdout.write(formatPortabilityReport(report))
    }
  }
  process.exit(process.exitCode ?? 0)
}

let routeId: string | undefined
let outputPath: string | undefined
let inputPath: string | undefined
let projectConfigPath: string | undefined
let projectRoot: string | undefined
const sourceRoots: SourceRoot[] = []

for (let index = 0; index < args.length; index += 1) {
  const argument = args[index]!
  if (argument === '--route') {
    routeId = args[++index]
  } else if (argument === '--out') {
    outputPath = args[++index]
  } else if (argument === '--project') {
    projectConfigPath = args[++index]
  } else if (argument === '--project-root') {
    projectRoot = args[++index]
  } else if (argument === '--source-root') {
    const value = args[++index]
    if (!value) usage()
    const separator = value.indexOf('=')
    if (separator <= 0 || separator === value.length - 1) usage()
    sourceRoots.push({
      prefix: value.slice(0, separator),
      directory: value.slice(separator + 1),
    })
  } else if (!argument.startsWith('-') && !inputPath) {
    inputPath = argument
  } else {
    usage()
  }
}

if (projectConfigPath) {
  if (routeId || inputPath || projectRoot || sourceRoots.length > 0) usage()
  const absoluteConfig = resolve(projectConfigPath)
  const raw = JSON.parse(await readFile(absoluteConfig, 'utf8')) as Partial<CompileProjectOptions>
  if (typeof raw.projectRoot !== 'string' || !Array.isArray(raw.routes)) usage()
  const configDirectory = dirname(absoluteConfig)
  const options: CompileProjectOptions = {
    projectRoot: isAbsolute(raw.projectRoot)
      ? raw.projectRoot
      : resolve(configDirectory, raw.projectRoot),
    routes: raw.routes,
    ...(raw.sourceRoots === undefined ? {} : { sourceRoots: raw.sourceRoots }),
  }
  const result = await compileProject(options)
  if (!result.ok) {
    process.stderr.write(`${formatDiagnostics(result.diagnostics)}\n`)
    process.exitCode = 1
  } else {
    await writeOutput(result.output, outputPath)
  }
} else {
  if (!routeId || !inputPath) usage()
  const result = await compileFile({
    entryFile: resolve(inputPath),
    routeId,
    projectRoot: resolve(projectRoot ?? process.cwd()),
    ...(sourceRoots.length === 0 ? {} : { sourceRoots }),
  })
  if (!result.ok) {
    process.stderr.write(`${formatDiagnostics(result.diagnostics)}\n`)
    process.exitCode = 1
  } else {
    await writeOutput(result.plan, outputPath)
  }
}

async function writeOutput(value: unknown, selectedPath: string | undefined): Promise<void> {
  const json = `${JSON.stringify(value, null, 2)}\n`
  if (selectedPath) await writeFile(resolve(selectedPath), json)
  else process.stdout.write(json)
}
