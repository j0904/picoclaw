package whatsapp

import (
	"os"
	"path/filepath"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/channels"
	"github.com/sipeed/picoclaw/pkg/config"
)

func init() {
	channels.RegisterFactory(
		config.ChannelWhatsAppNative,
		func(channelName, channelType string, cfg *config.Config, b *bus.MessageBus) (channels.Channel, error) {
			bc := cfg.Channels[channelName]
			decoded, err := bc.GetDecoded()
			if err != nil {
				return nil, err
			}
			c, ok := decoded.(*config.WhatsAppSettings)
			if !ok {
				return nil, channels.ErrSendFailed
			}
			storePath := c.SessionStorePath
			if storePath == "" {
				storePath = filepath.Join(cfg.WorkspacePath(), "whatsapp")
			}
			_, hasConfigFile := os.Stat(filepath.Join(storePath, "config.json"))
			ch, err := NewWhatsAppNativeChannel(bc, channelName, c, b, storePath, hasConfigFile == nil)
			if err != nil {
				return nil, err
			}
			return ch, nil
		},
	)
}
