package control

// KeepListenerPolicies filters a mutable candidate baseline to the policies its
// remaining listeners actually use. Callers add any newly reviewed publication
// policy afterwards. Immutable artifacts and catalog history are never edited.
func (b *BundleV2) KeepListenerPolicies() {
	used := make(map[Pin]bool, len(b.Listeners))
	for _, listener := range b.Listeners {
		if listener.Forward.Terminal != nil {
			used[*listener.Forward.Terminal] = true
		}
	}
	policies := make([]TerminalV2, 0, len(b.Terminal))
	for _, policy := range b.Terminal {
		if used[policy.Pin] {
			policies = append(policies, policy)
		}
	}
	b.Terminal = policies
}
