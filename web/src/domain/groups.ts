// Group identifiers derive from stable IDs, never editable node display names.
export function exitGroupName(revision: { readonly nodeIds: readonly string[]; readonly policyId?: string }): string {
  const nodeId = revision.nodeIds.at(-1)
  if (!nodeId) throw new Error('EXIT_NODE_REQUIRED')
  const known: Record<string, string> = { hk: 'HK-01', sg: 'SG-01', jp: 'JP-01', relay: 'Local-Relay', reverse: 'Reverse-Exit' }
  const nodeName = Object.hasOwn(known, nodeId) ? known[nodeId]! : nodeId.replace(/[^a-zA-Z0-9_-]/g, '_')
  return `Exit-${nodeName}${revision.policyId ? `-Egress-${revision.policyId.replace(/[^a-zA-Z0-9_-]/g, '_')}` : ''}`
}
