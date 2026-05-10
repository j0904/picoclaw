//go:build !whatsapp_native

package whatsapp

import (
	"fmt"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/channels"
	"github.com/sipeed/picoclaw/pkg/config"
)

func NewWhatsAppNativeChannel(
	bc *config.Channel,
	name string,
	cfg *config.WhatsAppSettings,
	bus *bus.MessageBus,
	storePath string,
	hasConfigFile bool,
) (channels.Channel, error) {
	_ = bc
	_ = name
	_ = cfg
	_ = bus
	_ = storePath
	return nil, fmt.Errorf("whatsapp native not compiled in; build with -tags whatsapp_native")
}
