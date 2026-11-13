package main

import (
	"reflect"
	"testing"
	"time"

	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
)

func fakeCanonical(j types.JID) types.JID {
	if j.Server == types.HiddenUserServer {
		return types.NewJID("5491100001111", types.DefaultUserServer)
	}
	return j
}

func newReceipt(chat types.JID, fromMe bool, rt types.ReceiptType) *events.Receipt {
	return &events.Receipt{
		MessageSource: types.MessageSource{Chat: chat, IsFromMe: fromMe, IsGroup: chat.Server == types.GroupServer},
		MessageIDs:    []types.MessageID{"A1", "A2"},
		Timestamp:     time.Unix(1700000000, 0),
		Type:          rt,
	}
}

func TestReadReceiptFromContactIsIgnored(t *testing.T) {
	chat := types.NewJID("5491100001111", types.DefaultUserServer)
	if ev := readReceiptEvent(newReceipt(chat, false, types.ReceiptTypeRead), fakeCanonical); ev != nil {
		t.Fatalf("un recibo del contacto no debe emitir evento: %v", ev)
	}
}

func TestOwnReadReceiptEmitsEvent(t *testing.T) {
	chat := types.NewJID("5491100001111", types.DefaultUserServer)
	for _, rt := range []types.ReceiptType{types.ReceiptTypeRead, types.ReceiptTypeReadSelf} {
		ev := readReceiptEvent(newReceipt(chat, true, rt), fakeCanonical)
		if ev == nil {
			t.Fatalf("tipo %q: se esperaba evento", rt)
		}
		if ev.GetEventType() != "messages.read" || ev.GetRemoteMessageId() != "A1,A2" ||
			ev.GetChatJid() != chat.String() || ev.GetRemoteSenderId() != chat.String() ||
			ev.GetChannelType() != "dm" || ev.GetSentAtUnix() != 1700000000 {
			t.Fatalf("evento inesperado: %v", ev)
		}
	}
}

func TestReadReceiptCanonicalizesLIDChat(t *testing.T) {
	chat := types.NewJID("999", types.HiddenUserServer)
	ev := readReceiptEvent(newReceipt(chat, true, types.ReceiptTypeReadSelf), fakeCanonical)
	if ev == nil || ev.GetChatJid() != "5491100001111@s.whatsapp.net" {
		t.Fatalf("chat no canonicalizado: %v", ev)
	}
}

func TestReadReceiptGroupKeepsChat(t *testing.T) {
	chat := types.NewJID("123-456", types.GroupServer)
	ev := readReceiptEvent(newReceipt(chat, true, types.ReceiptTypeRead), func(types.JID) types.JID {
		t.Fatal("no se debe canonicalizar un grupo")
		return types.EmptyJID
	})
	if ev == nil || ev.GetChannelType() != "group" || ev.GetChatJid() != chat.String() {
		t.Fatalf("evento de grupo inesperado: %v", ev)
	}
}

func TestNonReadReceiptsAreIgnored(t *testing.T) {
	chat := types.NewJID("5491100001111", types.DefaultUserServer)
	if ev := readReceiptEvent(newReceipt(chat, true, types.ReceiptTypeDelivered), fakeCanonical); ev != nil {
		t.Fatalf("delivered debe ignorarse: %v", ev)
	}
	if ev := readReceiptEvent(newReceipt(types.StatusBroadcastJID, true, types.ReceiptTypeRead), fakeCanonical); ev != nil {
		t.Fatalf("status broadcast debe ignorarse: %v", ev)
	}
}

func TestGroupReadIDsBySender(t *testing.T) {
	got := groupReadIDsBySender(
		[]string{"1", "2", "3", "4"},
		[]string{"a@s.whatsapp.net", "b@s.whatsapp.net", "a@s.whatsapp.net", ""},
		"fallback@s.whatsapp.net",
	)
	want := []readBatch{
		{Sender: "a@s.whatsapp.net", IDs: []string{"1", "3"}},
		{Sender: "b@s.whatsapp.net", IDs: []string{"2"}},
		{Sender: "fallback@s.whatsapp.net", IDs: []string{"4"}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

func TestGroupReadIDsBySenderEmptyFallback(t *testing.T) {
	got := groupReadIDsBySender([]string{"1"}, []string{""}, "")
	want := []readBatch{{Sender: "", IDs: []string{"1"}}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

// S7: si el store no conoce el LID, se usa el número que WhatsApp manda en el propio recibo (peer_recipient_pn).
func TestReadReceiptUsesRecipientAltWhenLIDUnmapped(t *testing.T) {
	chat := types.NewJID("999", types.HiddenUserServer)
	r := newReceipt(chat, true, types.ReceiptTypeRead)
	r.RecipientAlt = types.NewJID("5491122223333", types.DefaultUserServer)
	noMapping := func(j types.JID) types.JID { return j }

	ev := readReceiptEvent(r, noMapping)
	if ev == nil || ev.GetChatJid() != "5491122223333@s.whatsapp.net" || ev.GetRemoteSenderId() != "5491122223333@s.whatsapp.net" {
		t.Fatalf("se esperaba el número de RecipientAlt: %v", ev)
	}
}
