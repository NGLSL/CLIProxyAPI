package config

// HomeConfig stores runtime-only Home control plane settings from -home-jwt.
type HomeConfig struct {
	Enabled bool `yaml:"enabled" json:"enabled"`
	// NodeID 是 Home 控制中心给当前节点分配的稳定标识，只来自 JWT 运行时解析，
	// 不写入本地配置，避免控制中心节点身份被落盘后误复用。
	NodeID                  string        `yaml:"-" json:"-"`
	Host                    string        `yaml:"host" json:"-"`
	Port                    int           `yaml:"port" json:"-"`
	DisableClusterDiscovery bool          `yaml:"disable-cluster-discovery" json:"-"`
	TLS                     HomeTLSConfig `yaml:"tls" json:"-"`
}

// HomeTLSConfig configures client-side TLS for the home Redis connection.
type HomeTLSConfig struct {
	Enable              bool   `yaml:"enable" json:"-"`
	ServerName          string `yaml:"server-name" json:"-"`
	InsecureSkipVerify  bool   `yaml:"insecure-skip-verify" json:"-"`
	CACert              string `yaml:"ca-cert" json:"-"`
	ClientCert          string `yaml:"-" json:"-"`
	ClientKey           string `yaml:"-" json:"-"`
	UseTargetServerName bool   `yaml:"-" json:"-"`
}
