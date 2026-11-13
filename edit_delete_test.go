package main

import (
	"context"
	"testing"
	"time"

	conduitv1 "github.com/cestila/ConduitOmniChat/sdk/conduitv1"
	waProto "go.mau.fi/whatsmeow/binary/proto"
	"go.mau.fi/whatsmeow/types"
	"google.golang.org/protobuf/proto"
)

func TestResolveCanonicalJIDNonLIDUnchanged(t *testing.T) {
	plugin := NewWhatsAppPlugin()
	origJID := types.NewJID("5491144332211", types.DefaultUserServer)

	resolved := plugin.resolveCanonicalJID(context.Background(), origJID)
	if resolved.String() != origJID.String() {
		t.Fatalf("resolveCanonicalJID modificó un JID canónico: %s -> %s", origJID.String(), resolved.String())
	}
}

func TestExtractEditNewText(t *testing.T) {
	// 1. Mensaje de texto simple
	protoText := &waProto.ProtocolMessage{
		Type: waProto.ProtocolMessage_MESSAGE_EDIT.Enum(),
		Key:  &waProto.MessageKey{ID: proto.String("msg-1")},
		EditedMessage: &waProto.Message{
			Conversation: proto.String("Texto editado"),
		},
	}
	if got := protoText.GetEditedMessage().GetConversation(); got != "Texto editado" {
		t.Errorf("GetConversation = %q, want 'Texto editado'", got)
	}

	// 2. Mensaje de texto extendido
	protoExt := &waProto.ProtocolMessage{
		Type: waProto.ProtocolMessage_MESSAGE_EDIT.Enum(),
		Key:  &waProto.MessageKey{ID: proto.String("msg-2")},
		EditedMessage: &waProto.Message{
			ExtendedTextMessage: &waProto.ExtendedTextMessage{
				Text: proto.String("Texto extendido editado"),
			},
		},
	}
	if got := protoExt.GetEditedMessage().GetExtendedTextMessage().GetText(); got != "Texto extendido editado" {
		t.Errorf("ExtendedTextMessage.GetText = %q, want 'Texto extendido editado'", got)
	}

	// 3. Imagen con caption editado
	protoImg := &waProto.ProtocolMessage{
		Type: waProto.ProtocolMessage_MESSAGE_EDIT.Enum(),
		Key:  &waProto.MessageKey{ID: proto.String("msg-3")},
		EditedMessage: &waProto.Message{
			ImageMessage: &waProto.ImageMessage{
				Caption: proto.String("Pie de foto corregido"),
			},
		},
	}
	if got := protoImg.GetEditedMessage().GetImageMessage().GetCaption(); got != "Pie de foto corregido" {
		t.Errorf("ImageMessage.GetCaption = %q, want 'Pie de foto corregido'", got)
	}
}

func TestWhatsAppRevokeProtocolMessageStructure(t *testing.T) {
	targetID := "3EB0ABC123DEF456"
	protoMsg := &waProto.ProtocolMessage{
		Type: waProto.ProtocolMessage_REVOKE.Enum(),
		Key: &waProto.MessageKey{
			ID: proto.String(targetID),
		},
	}

	if protoMsg.GetType() != waProto.ProtocolMessage_REVOKE {
		t.Fatalf("Tipo esperado REVOKE, obtenido %v", protoMsg.GetType())
	}
	if protoMsg.GetKey().GetID() != targetID {
		t.Fatalf("Target ID esperado %q, obtenido %q", targetID, protoMsg.GetKey().GetID())
	}

	ev := &conduitv1.PluginEvent{
		EventType:       "message.deleted",
		RemoteMessageId: protoMsg.GetKey().GetID(),
		ChatJid:         "5491144332211@s.whatsapp.net",
		RemoteSenderId:  "5491144332211@s.whatsapp.net",
		SentAtUnix:      time.Now().Unix(),
	}

	if ev.EventType != "message.deleted" {
		t.Errorf("EventType = %q, want message.deleted", ev.EventType)
	}
	if ev.RemoteMessageId != targetID {
		t.Errorf("RemoteMessageId = %q, want %q", ev.RemoteMessageId, targetID)
	}
}
