//go:build !whatsapp_native

package whatsapp

import (
	"fmt"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/channels"
	"github.com/sipeed/picoclaw/pkg/config"
)

func NewWhatsAppNativeChannel(
	cfg config.WhatsAppConfig,
	bus *bus.MessageBus,
	storePath string,
	hasConfigFile bool,
) (channels.Channel, error) {
	return nil, fmt.Errorf("whatsapp native not compiled in; build with -tags whatsapp_native")
}
