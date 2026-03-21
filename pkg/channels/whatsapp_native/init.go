package whatsapp

import (
	"os"
	"path/filepath"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/channels"
	"github.com/sipeed/picoclaw/pkg/config"
)

func hasConfigFile() bool {
	if path := os.Getenv(config.EnvConfig); path != "" {
		_, err := os.Stat(path)
		return err == nil
	}
	if home, _ := os.UserHomeDir(); home != "" {
		path := filepath.Join(home, ".picoclaw", "config.json")
		_, err := os.Stat(path)
		return err == nil
	}
	return false
}

func init() {
	channels.RegisterFactory("whatsapp_native", func(cfg *config.Config, b *bus.MessageBus) (channels.Channel, error) {
		waCfg := cfg.Channels.WhatsApp
		storePath := waCfg.SessionStorePath
		if storePath == "" {
			storePath = filepath.Join(filepath.Dir(cfg.WorkspacePath()), "whatsapp")
		}
		return NewWhatsAppNativeChannel(waCfg, b, storePath, hasConfigFile())
	})
}
