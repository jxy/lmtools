package config

// Adapter methods for Config to implement core.RequestConfig interface

func (c Config) GetUser() string {
	return c.User
}

func (c Config) GetModel() string {
	return c.Model
}

func (c Config) GetSystem() string {
	return c.System
}

func (c Config) GetEnv() string {
	return c.Env
}

func (c Config) IsEmbed() bool {
	return c.Embed
}

func (c Config) IsStreamChat() bool {
	return c.StreamChat
}
