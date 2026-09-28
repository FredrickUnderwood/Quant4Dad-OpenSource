import { mkdtempSync, rmSync } from 'node:fs'
import { createRequire } from 'node:module'
import { tmpdir } from 'node:os'
import { dirname, join, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

// Run after fetch-dsh-source.sh provenance validation and frozen installation.
// Preserve vendor compiler boundaries: their relaxed options must not leak into
// our strict host check, nor may host options be imposed on vendor source.
// Emit declarations only into a private temporary directory; never touch DSH.
if (!process.env.Q4D_DSH_SOURCE_DIR) throw new Error('Set Q4D_DSH_SOURCE_DIR to the verified installed source')
const source = resolve(process.env.Q4D_DSH_SOURCE_DIR)
const ts = createRequire(join(source, 'package.json'))('typescript')
const runtime = dirname(dirname(fileURLToPath(import.meta.url)))
const temporary = mkdtempSync(join(tmpdir(), 'q4d-bootstrap-types-'))
const aliases = {}
const diagnosticHost = { getCanonicalFileName: name => name, getCurrentDirectory: () => process.cwd(), getNewLine: () => '\n' }
function check(program, emit = false) {
  const diagnostics = ts.getPreEmitDiagnostics(program)
  if (diagnostics.length) throw new Error(ts.formatDiagnostics(diagnostics, diagnosticHost))
  if (emit) {
    const result = program.emit()
    if (result.emitSkipped || result.diagnostics.length) throw new Error(ts.formatDiagnostics(result.diagnostics, diagnosticHost))
  }
}
function config(file) {
  const read = ts.readConfigFile(file, ts.sys.readFile)
  if (read.error) throw new Error(ts.formatDiagnostics([read.error], diagnosticHost))
  const parsed = ts.parseJsonConfigFileContent(read.config, ts.sys, dirname(file))
  if (parsed.errors.length) throw new Error(ts.formatDiagnostics(parsed.errors, diagnosticHost))
  return parsed
}
try {
  for (const vendor of ['cosmokit', 'cordis', 'schemastery']) {
    const parsed = config(join(source, 'vendor', vendor, 'tsconfig.json'))
    const outDir = join(temporary, vendor)
    check(ts.createProgram(parsed.fileNames, { ...parsed.options, paths: { ...parsed.options.paths, ...aliases },
      composite: false, incremental: false, noEmit: false, emitDeclarationOnly: true, declaration: true,
      sourceMap: false, declarationMap: false, outDir, typeRoots: [join(source, 'node_modules/@types')] }), true)
    aliases[`@deepseek-ai/${vendor}`] = [join(outDir, 'index.d.ts')]
  }
  const base = config(join(source, 'tsconfig.base.json'))
  const files = ['src/bootstrap/dsh-generation.ts', 'fixtures/bootstrap-dsh-host.ts',
    'src/runtime/session-host.ts', 'src/runtime/bootstrap-runtime.ts', 'src/runtime/main.ts', 'src/operations/inspect-sessions.ts',
    'fixtures/session-runtime-host.ts', 'fixtures/catalog-output-host.ts'].map(path => join(runtime, path))
  check(ts.createProgram(files, { ...base.options, paths: { ...base.options.paths, ...aliases },
    composite: false, incremental: false, noEmit: true, rewriteRelativeImportExtensions: false,
    allowJs: true, checkJs: false, typeRoots: [join(source, 'node_modules/@types')] }))
  process.stdout.write('Bootstrap TypeScript and pinned public DSH interfaces: passed\n')
} finally { rmSync(temporary, { recursive: true, force: true }) }
