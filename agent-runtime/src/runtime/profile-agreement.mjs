const keys = ['id', 'revision', 'promptBundleDigest', 'skillsDigest', 'toolCatalogRevision']

export function profileIdentity(profile) {
  return Object.fromEntries(keys.map(key => [key, profile[key]]))
}

export function profilesAgree(profiles, bootstrap, required = false) {
  if (!bootstrap.profiles) return !required
  if (new Set(bootstrap.profiles.map(p => p.id)).size !== bootstrap.profiles.length) return false
  return (!required || profiles.length === bootstrap.profiles.length) && profiles.every(p => {
    const remote = bootstrap.profiles.find(r => r.id === p.id)
    return remote && keys.every(key => remote[key] === p[key])
  })
}
