package main

import (
	"context"
	"testing"

	conduitv1 "github.com/cestila/ConduitOmniChat/sdk/conduitv1"
	"go.mau.fi/whatsmeow"
	waAdv "go.mau.fi/whatsmeow/proto/waAdv"
	"go.mau.fi/whatsmeow/store/sqlstore"
	"go.mau.fi/whatsmeow/types"
	waLog "go.mau.fi/whatsmeow/util/log"
	_ "modernc.org/sqlite"
)

func TestSyncContactsExcludesGroupsAndResolvesLID(t *testing.T) {
	ctx := context.Background()
	logger := waLog.Noop
	container, err := sqlstore.New(ctx, "sqlite", "file::memory:?_pragma=foreign_keys(1)", logger)
	if err != nil {
		t.Fatalf("failed to create sqlstore: %v", err)
	}
	defer container.Close()

	deviceStore := container.NewDevice()
	ownJID := types.NewJID("5491100000000", types.DefaultUserServer)
	deviceStore.ID = &ownJID
	deviceStore.AdvSecretKey = make([]byte, 32)
	deviceStore.Account = &waAdv.ADVSignedDeviceIdentity{
		Details:             []byte{1},
		AccountSignature:    make([]byte, 64),
		AccountSignatureKey: make([]byte, 32),
		DeviceSignature:     make([]byte, 64),
	}
	if err := container.PutDevice(ctx, deviceStore); err != nil {
		t.Fatalf("PutDevice: %v", err)
	}
	client := whatsmeow.NewClient(deviceStore, logger)

	// Seed contacts: 1 group, 1 broadcast, status@broadcast, 1 LID mapped to PN, 1 direct PN.
	groupJID := types.NewJID("120363000000000001", types.GroupServer)
	broadcastJID := types.NewJID("1234567890", types.BroadcastServer)
	statusJID := types.StatusBroadcastJID
	lidJID := types.NewJID("999888777666", types.HiddenUserServer)
	resolvedPNJID := types.NewJID("5491155551234", types.DefaultUserServer)
	directPNJID := types.NewJID("5491166667777", types.DefaultUserServer)

	if err := deviceStore.Contacts.PutContactName(ctx, groupJID, "Family Group", "Family"); err != nil {
		t.Fatalf("PutContactName group: %v", err)
	}
	if err := deviceStore.Contacts.PutContactName(ctx, broadcastJID, "Broadcast List", "Broadcast"); err != nil {
		t.Fatalf("PutContactName broadcast: %v", err)
	}
	if err := deviceStore.Contacts.PutContactName(ctx, statusJID, "Status", "Status"); err != nil {
		t.Fatalf("PutContactName status: %v", err)
	}
	if err := deviceStore.Contacts.PutContactName(ctx, lidJID, "Alice", "Alice LID"); err != nil {
		t.Fatalf("PutContactName lid: %v", err)
	}
	if err := deviceStore.LIDs.PutLIDMapping(ctx, lidJID, resolvedPNJID); err != nil {
		t.Fatalf("PutLIDMapping: %v", err)
	}
	if err := deviceStore.Contacts.PutContactName(ctx, directPNJID, "Bob Direct", "Bob"); err != nil {
		t.Fatalf("PutContactName direct: %v", err)
	}

	plugin := NewWhatsAppPlugin()
	plugin.client = client

	resp, err := plugin.SyncContacts(ctx, &conduitv1.SyncContactsRequest{
		Provider:  "whatsapp",
		AccountId: "wa-acc-1",
	})
	if err != nil {
		t.Fatalf("SyncContacts returned error: %v", err)
	}
	if resp.GetError() != "" {
		t.Fatalf("SyncContacts returned response error: %s", resp.GetError())
	}

	got := make(map[string]*conduitv1.PluginContact, len(resp.GetContacts()))
	for _, c := range resp.GetContacts() {
		if c.GetAccountId() != "wa-acc-1" {
			t.Errorf("contact %s has account_id %q, want %q", c.GetRemoteId(), c.GetAccountId(), "wa-acc-1")
		}
		got[c.GetRemoteId()] = c
	}

	if len(got) != 2 {
		t.Fatalf("expected 2 1:1 contacts, got %d: %+v", len(got), resp.GetContacts())
	}

	alice, ok := got[resolvedPNJID.String()]
	if !ok {
		t.Fatalf("expected resolved LID contact %s in %+v", resolvedPNJID.String(), got)
	}
	if alice.GetPhone() != "+5491155551234" {
		t.Errorf("Alice phone = %q, want %q", alice.GetPhone(), "+5491155551234")
	}
	if alice.GetDisplayName() != "Alice LID" {
		t.Errorf("Alice display_name = %q, want %q", alice.GetDisplayName(), "Alice LID")
	}

	bob, ok := got[directPNJID.String()]
	if !ok {
		t.Fatalf("expected direct contact %s in %+v", directPNJID.String(), got)
	}
	if bob.GetPhone() != "+5491166667777" {
		t.Errorf("Bob phone = %q, want %q", bob.GetPhone(), "+5491166667777")
	}
}

func TestSyncContactsPrefersCanonicalNameOverLID(t *testing.T) {
	ctx := context.Background()
	logger := waLog.Noop
	container, err := sqlstore.New(ctx, "sqlite", "file::memory:?_pragma=foreign_keys(1)", logger)
	if err != nil {
		t.Fatalf("failed to create sqlstore: %v", err)
	}
	defer container.Close()

	deviceStore := container.NewDevice()
	ownJID := types.NewJID("5491100000000", types.DefaultUserServer)
	deviceStore.ID = &ownJID
	deviceStore.AdvSecretKey = make([]byte, 32)
	deviceStore.Account = &waAdv.ADVSignedDeviceIdentity{
		Details:             []byte{1},
		AccountSignature:    make([]byte, 64),
		AccountSignatureKey: make([]byte, 32),
		DeviceSignature:     make([]byte, 64),
	}
	if err := container.PutDevice(ctx, deviceStore); err != nil {
		t.Fatalf("PutDevice: %v", err)
	}
	client := whatsmeow.NewClient(deviceStore, logger)

	lidJID := types.NewJID("111222333444", types.HiddenUserServer)
	pnJID := types.NewJID("5491177778888", types.DefaultUserServer)

	if err := deviceStore.Contacts.PutContactName(ctx, lidJID, "LID", "LID Alias"); err != nil {
		t.Fatalf("PutContactName lid: %v", err)
	}
	if err := deviceStore.Contacts.PutContactName(ctx, pnJID, "Canonical", "Canonical Name"); err != nil {
		t.Fatalf("PutContactName pn: %v", err)
	}
	if err := deviceStore.LIDs.PutLIDMapping(ctx, lidJID, pnJID); err != nil {
		t.Fatalf("PutLIDMapping: %v", err)
	}

	plugin := NewWhatsAppPlugin()
	plugin.client = client

	for i := 0; i < 10; i++ {
		resp, err := plugin.SyncContacts(ctx, &conduitv1.SyncContactsRequest{
			Provider:  "whatsapp",
			AccountId: "wa-acc-1",
		})
		if err != nil {
			t.Fatalf("iteration %d: SyncContacts error: %v", i, err)
		}
		if len(resp.GetContacts()) != 1 {
			t.Fatalf("iteration %d: expected 1 contact, got %d", i, len(resp.GetContacts()))
		}
		if got := resp.GetContacts()[0].GetDisplayName(); got != "Canonical Name" {
			t.Fatalf("iteration %d: DisplayName = %q, want %q", i, got, "Canonical Name")
		}
	}
}

func TestResolveAvatarURLDoesNotNegativeCacheOnCanceledContext(t *testing.T) {
	plugin := NewWhatsAppPlugin()
	jid := types.NewJID("5491199990000", types.DefaultUserServer)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if got := plugin.resolveAvatarURL(ctx, jid); got != "" {
		t.Fatalf("expected empty avatar URL on canceled context, got %q", got)
	}
	if _, cached := plugin.avatarCache.Load(jid.String()); cached {
		t.Fatalf("expected canceled context not to negative-cache %s", jid.String())
	}
}
