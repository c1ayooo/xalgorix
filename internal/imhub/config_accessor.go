package imhub

// LoadConfig returns the current inbound IM configuration. Callback handlers
// and polling clients read it per request so a settings-page write takes effect
// without restarting the hub. Kept as a Hub method (rather than calling the
// package function at each site) so a future cached config can be introduced
// without touching the adapters.
func (h *Hub) LoadConfig() Config { return LoadConfigFromEnv() }
