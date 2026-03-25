package acp

import (
	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/channels"
	"github.com/sipeed/picoclaw/pkg/config"
)

func init() {
	channels.RegisterFactory("acp", func(cfg *config.Config, b *bus.MessageBus) (channels.Channel, error) {
		return NewACPChannel(cfg.Channels.ACP, b)
	})
}
