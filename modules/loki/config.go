package loki

// Config holds the Loki module configuration.
type Config struct {
	Instances []InstanceConfig `yaml:"instances"`
}

// InstanceConfig holds configuration for a Loki instance.
type InstanceConfig struct {
	Name        string `yaml:"name" json:"name"`
	Description string `yaml:"description,omitempty" json:"description,omitempty"`
	URL         string `yaml:"url" json:"url"`
	Username    string `yaml:"username,omitempty" json:"username,omitempty"`
	Password    string `yaml:"password,omitempty" json:"password,omitempty"`
	SkipVerify  bool   `yaml:"skip_verify,omitempty" json:"skip_verify,omitempty"`
	Timeout     int    `yaml:"timeout,omitempty" json:"timeout,omitempty"`
}
