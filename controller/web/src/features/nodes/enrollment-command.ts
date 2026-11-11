import type { NodeForm } from './node-model'

// This describes the future installer CLI; it never downloads or runs software.
// Comment the copied preview out so pasting the prototype cannot install a node.
export function enrollmentCommand(options: {
  name: string; form: NodeForm; controller: string; token: string; relayNodeId: string
}): string {
  const quote = (value: string) => `'${value.replace(/[\u0000-\u001f\u007f\u0085\u2028\u2029]/g, ' ').replace(/'/g, "'\\''")}'`
  const args = [
    'sudo meshstrata-install node',
    '--controller', quote(options.controller),
    '--trust-sha256', quote('DEMO-NOT-A-TRUST-ANCHOR'),
    '--enrollment-token', quote(options.token),
    '--name', quote(options.name.trim()),
    '--connection-mode', options.form.mode === 'direct' ? 'direct' : 'reverse',
    ...(options.form.mode === 'direct'
      ? ['--advertise-address', quote(options.form.address.trim())]
      : ['--relay-node-id', quote(options.relayNodeId)]),
  ]
  return `# PREVIEW ONLY: installer and enrollment service are not connected.\n# ${args.join(' ')}`
}
