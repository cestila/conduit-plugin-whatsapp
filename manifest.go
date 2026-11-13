package main

import (
	"context"

	"github.com/cestila/ConduitOmniChat/sdk/conduitv1"
	"github.com/cestila/ConduitOmniChat/sdk/pluginsdk"
)

// version is set at release time with -ldflags "-X main.version=vX.Y.Z".
var version = "dev"

// GetManifest describes the WhatsApp plugin to conduitd and its clients.
func (p *WhatsAppPlugin) GetManifest(context.Context, *conduitv1.GetManifestRequest) (*conduitv1.PluginManifest, error) {
	return &conduitv1.PluginManifest{
		Id:              "whatsapp",
		DisplayName:     "WhatsApp",
		Description:     "WhatsApp multi-device (QR) vía whatsmeow",
		Version:         version,
		Author:          "Conduit contributors",
		Homepage:        "https://github.com/cestila/conduit-plugin-whatsapp",
		Provider:        conduitv1.Provider_PROVIDER_WHATSAPP,
		Icon:            "fa-brands fa-whatsapp",
		Color:           "#25d366",
		LoginFlows:      []conduitv1.LoginFlow{conduitv1.LoginFlow_LOGIN_FLOW_QR},
		QrInstructions:  "Ajustes → Dispositivos vinculados → Vincular un dispositivo",
		CredentialsHint: "WhatsApp se vincula escaneando el código QR; usa el identificador para nombrar el dispositivo.",
		Capabilities:    []string{"reactions", "replies", "polls", "edit", "delete", "pin", "read_receipts", "media", "voice"},
		ProtocolVersion: pluginsdk.ProtocolVersion,
	}, nil
}
