package subspacerelay

import (
	"github.com/nvx/go-rfid"
	subspacerelaypb "github.com/nvx/subspace-relay"
	"log/slog"
)

func RelayInfoAttrs(relayInfo *subspacerelaypb.RelayInfo) slog.Attr {
	attrs := []slog.Attr{
		slog.String("connection_type", relayInfo.ConnectionType.String()),
	}
	if relayInfo.DeviceName != "" {
		attrs = append(attrs, slog.String("device_name", relayInfo.DeviceName))
	}
	if len(relayInfo.Atr) != 0 {
		attrs = append(attrs, rfid.LogHex("atr", relayInfo.Atr))
	}
	if len(relayInfo.DeviceAddress) != 0 {
		attrs = append(attrs, rfid.LogHex("device_address", relayInfo.DeviceAddress))
	}
	if relayInfo.Rssi != 0 {
		attrs = append(attrs, slog.Int("rssi", int(relayInfo.Rssi)))
	}

	return slog.GroupAttrs("relay_info", attrs...)
}
