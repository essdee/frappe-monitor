package config

import "github.com/knadh/koanf/providers/confmap"

func confmapProvider(m map[string]any) *confmap.Confmap {
	return confmap.Provider(m, ".")
}
