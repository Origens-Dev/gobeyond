#!/usr/bin/env node

import { basename, resolve } from 'node:path'

import { createProject, CreateProjectError } from './scaffold.js'

function usage() {
  process.stderr.write('usage: create-gobeyond <project-directory>\n')
}

const [target, ...extra] = process.argv.slice(2)
if (!target || extra.length > 0 || target.startsWith('-')) {
  usage()
  process.exitCode = 2
} else {
  try {
    const destination = resolve(target)
    await createProject(destination, { projectName: basename(destination) })
    process.stdout.write(`Created GoBeyond project in ${destination}\n\n`)
    process.stdout.write(`Next:\n  cd ${target}\n  pnpm install\n  pnpm dev\n`)
  } catch (error) {
    const message = error instanceof CreateProjectError ? error.message : String(error)
    process.stderr.write(`create-gobeyond: ${message}\n`)
    process.exitCode = 1
  }
}
