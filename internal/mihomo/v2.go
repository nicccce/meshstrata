package mihomo

// OutboundV2 is deliberately finite. No arbitrary map or YAML crosses this API.
type OutboundV2 struct {
	Name              string             `json:"name"`
	Type              string             `json:"type"`
	Server            string             `json:"server,omitempty"`
	Port              uint16             `json:"port,omitempty"`
	Cipher            string             `json:"cipher,omitempty"`
	Password          string             `json:"password,omitempty"`
	UUID              string             `json:"uuid,omitempty"`
	Username          string             `json:"username,omitempty"`
	SNI               string             `json:"sni,omitempty"`
	ServerName        string             `json:"servername,omitempty"`
	TLS               bool               `json:"tls,omitempty"`
	Network           string             `json:"network,omitempty"`
	Flow              string             `json:"flow,omitempty"`
	SkipCertVerify    bool               `json:"skip-cert-verify"`
	Fingerprint       string             `json:"fingerprint,omitempty"`
	ClientFingerprint string             `json:"client-fingerprint,omitempty"`
	Reality           *OutboundRealityV2 `json:"reality-opts,omitempty"`
	UDP               bool               `json:"udp"`
	Interface         string             `json:"interface-name,omitempty"`
}
type OutboundRealityV2 struct {
	PublicKey string `json:"public-key"`
	ShortID   string `json:"short-id"`
}
