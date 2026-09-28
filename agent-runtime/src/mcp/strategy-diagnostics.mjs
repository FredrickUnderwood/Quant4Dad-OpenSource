// Select only the declared mode for diagnostics. Admission always uses the
// complete original schema; this projection must never authorize a call.
export function strategyDiagnosticSchema(schema, args) {
  const mode = args?.strategy?.body?.mode ?? 'config'
  if (!['config', 'script'].includes(mode)) return schema
  let selected = structuredClone(schema)
  if (selected.oneOf) {
    const branch = selected.oneOf.find(option => option.properties?.strategy?.properties?.body?.properties?.mode?.enum?.includes(mode))
    if (branch) selected = branch
  }
  const body = selected.properties?.strategy?.properties?.body
  if (body?.oneOf) {
    const branch = body.oneOf.find(option => option.properties?.mode?.enum?.includes(mode))
    if (branch) selected.properties.strategy.properties.body = branch
  }
  // Empty array and null are both accepted for unused script logic. Report an
  // array's actual size error instead of the irrelevant null alternative.
  if (mode === 'script') for (const key of ['indicators', 'rules']) {
    const node = selected.properties?.strategy?.properties?.body?.properties?.[key]
    if (node?.anyOf && Array.isArray(args?.strategy?.body?.[key])) {
      selected.properties.strategy.properties.body.properties[key] = node.anyOf.find(option => option.type === 'array')
    }
  }
  return selected
}
