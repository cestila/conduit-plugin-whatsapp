package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	conduitv1 "github.com/cestila/ConduitOmniChat/sdk/conduitv1"
	"github.com/skip2/go-qrcode"
	"go.mau.fi/whatsmeow"
	waProto "go.mau.fi/whatsmeow/binary/proto"
	waCommon "go.mau.fi/whatsmeow/proto/waCommon"
	waHistorySync "go.mau.fi/whatsmeow/proto/waHistorySync"
	"go.mau.fi/whatsmeow/store/sqlstore"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	waLog "go.mau.fi/whatsmeow/util/log"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	_ "modernc.org/sqlite"
)

// WhatsAppPlugin implementa la conexión real con whatsmeow.
type WhatsAppPlugin struct {
	conduitv1.UnimplementedPluginServiceServer
	mu          sync.RWMutex
	client      *whatsmeow.Client
	eventCh     chan *conduitv1.PluginEvent
	store       *sqlstore.Container
	cancelFn    context.CancelFunc
	avatarCache sync.Map
	pollCache   sync.Map
	mediaDir    string
}

type whatsAppPollMeta struct {
	mu          sync.Mutex
	SenderJID   types.JID
	Question    string
	OptionNames []string
	Selectable  int32
	UserVotes   map[string][]string
}

func (p *WhatsAppPlugin) rememberPollMeta(msgID string, senderJID types.JID, pollProto *conduitv1.ProtoPoll) {
	if msgID == "" || pollProto == nil {
		return
	}
	var optionNames []string
	for _, opt := range pollProto.Options {
		optionNames = append(optionNames, opt.Text)
	}
	meta := &whatsAppPollMeta{
		SenderJID:   senderJID,
		Question:    pollProto.Question,
		OptionNames: optionNames,
		Selectable:  pollProto.SelectableCount,
		UserVotes:   make(map[string][]string),
	}
	p.pollCache.Store(msgID, meta)
}

func (m *whatsAppPollMeta) buildProtoPoll() *conduitv1.ProtoPoll {
	m.mu.Lock()
	defer m.mu.Unlock()

	counts := make(map[string]int32)
	totalVotes := int32(0)
	for _, votes := range m.UserVotes {
		for _, v := range votes {
			counts[v]++
			totalVotes++
		}
	}

	var options []*conduitv1.ProtoPollOption
	for idx, optName := range m.OptionNames {
		vCount := counts[optName]
		options = append(options, &conduitv1.ProtoPollOption{
			Id:        strconv.Itoa(idx),
			Text:      optName,
			VoteCount: vCount,
		})
	}

	return &conduitv1.ProtoPoll{
		Question:        m.Question,
		Options:         options,
		TotalVotes:      totalVotes,
		SelectableCount: m.Selectable,
	}
}

func (m *whatsAppPollMeta) recordVotes(voterID string, selectedNames []string) *conduitv1.ProtoPoll {
	m.mu.Lock()
	if m.UserVotes == nil {
		m.UserVotes = make(map[string][]string)
	}
	m.UserVotes[voterID] = selectedNames
	m.mu.Unlock()
	return m.buildProtoPoll()
}

// NewWhatsAppPlugin inicializa una nueva instancia del plugin real de WhatsApp.
func NewWhatsAppPlugin() *WhatsAppPlugin {
	homeDir, _ := os.UserHomeDir()
	mDir := filepath.Join(homeDir, ".local", "share", "conduit", "media")
	_ = os.MkdirAll(mDir, 0755)
	return &WhatsAppPlugin{
		eventCh:  make(chan *conduitv1.PluginEvent, 200),
		mediaDir: mDir,
	}
}

// resolveAvatarURL consulta de manera segura y en caché la foto de perfil de un contacto o grupo
func (p *WhatsAppPlugin) resolveAvatarURL(ctx context.Context, jid types.JID) string {
	if jid.IsEmpty() {
		return ""
	}
	cacheKey := jid.String()
	if val, ok := p.avatarCache.Load(cacheKey); ok {
		if s, ok := val.(string); ok {
			return s
		}
	}

	p.mu.RLock()
	client := p.client
	p.mu.RUnlock()

	if client == nil || !client.IsConnected() {
		return ""
	}

	callCtx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()

	picInfo, err := client.GetProfilePictureInfo(callCtx, jid, &whatsmeow.GetProfilePictureParams{
		Preview: true,
	})
	if err != nil || picInfo == nil || picInfo.URL == "" {
		p.avatarCache.Store(cacheKey, "")
		return ""
	}

	p.avatarCache.Store(cacheKey, picInfo.URL)
	return picInfo.URL
}

// syncContacts vuelca todos los contactos almacenados en whatsmeow al stream de eventos
func (p *WhatsAppPlugin) syncContacts(ctx context.Context) {
	if p.client == nil || p.client.Store == nil || p.client.Store.Contacts == nil {
		return
	}
	contacts, err := p.client.Store.Contacts.GetAllContacts(ctx)
	if err != nil {
		return
	}

	// 1. Sincronizar grupos en los que participa el usuario primero
	groups, err := p.client.GetJoinedGroups(ctx)
	if err == nil {
		for _, g := range groups {
			gJID := g.JID
			gName := g.Name
			go func() {
				avatar := p.resolveAvatarURL(context.Background(), gJID)
				p.eventCh <- &conduitv1.PluginEvent{
					EventType:      "contact.sync",
					RemoteSenderId: gJID.String(),
					SenderName:     gName,
					Body:           "group",
					SentAtUnix:     0,
					AvatarUrl:      avatar,
				}
			}()
		}
	}

	// 2. Sincronizar contactos individuales con un semáforo de concurrencia para evitar saturación
	sem := make(chan struct{}, 5)
	for jid, c := range contacts {
		if jid.Server == types.GroupServer || jid.Server == types.BroadcastServer {
			continue
		}
		name := c.FullName
		if name == "" {
			name = c.BusinessName
		}
		if name == "" {
			name = c.PushName
		}
		if name == "" {
			continue
		}

		targetJID := p.resolveCanonicalJID(ctx, jid)
		targetName := name
		targetPush := c.PushName
		phoneHint := phoneFromCanonicalJID(targetJID)

		go func() {
			sem <- struct{}{}
			defer func() { <-sem }()

			avatar := p.resolveAvatarURL(context.Background(), targetJID)
			p.eventCh <- &conduitv1.PluginEvent{
				EventType:      "contact.sync",
				RemoteSenderId: targetJID.String(),
				SenderName:     targetName,
				Body:           targetPush,
				SentAtUnix:     0,
				AvatarUrl:      avatar,
				IdentityPhone:  phoneHint,
			}
		}()
	}
}

// phoneFromCanonicalJID extrae el número E.164 si el JID corresponde a un usuario estándar (@s.whatsapp.net).
func phoneFromCanonicalJID(jid types.JID) string {
	if jid.Server == types.DefaultUserServer && jid.User != "" {
		return "+" + jid.User
	}
	return ""
}

// resolveCanonicalJID convierte un JID de tipo LID (@lid) a su número de teléfono canónico (@s.whatsapp.net) si está mapeado
func (p *WhatsAppPlugin) resolveCanonicalJID(ctx context.Context, jid types.JID) types.JID {
	if jid.Server == types.HiddenUserServer && p.client != nil && p.client.Store != nil && p.client.Store.LIDs != nil {
		if pnJID, err := p.client.Store.LIDs.GetPNForLID(ctx, jid); err == nil && !pnJID.IsEmpty() {
			return pnJID
		}
	}
	return jid
}

// resolveContactName busca el nombre más amigable para un JID dado
// Prioridad: 1º Libreta de contactos guardada (FullName / BusinessName)
//
//	2º Nombre de grupo si es un grupo
//	3º PushName si está disponible
func (p *WhatsAppPlugin) resolveContactName(ctx context.Context, jid types.JID, pushName string) string {
	if p.client != nil {
		// 1. Si es un grupo, intentar obtener el nombre del grupo
		if jid.Server == types.GroupServer {
			if gInfo, err := p.client.GetGroupInfo(ctx, jid); err == nil && gInfo != nil && gInfo.Name != "" {
				return gInfo.Name
			}
		}

		if p.client.Store != nil {
			canonicalJID := p.resolveCanonicalJID(ctx, jid)

			// 2. Probar resolución en libreta con JID canónico
			if p.client.Store.Contacts != nil {
				if c, err := p.client.Store.Contacts.GetContact(ctx, canonicalJID); err == nil {
					if c.FullName != "" {
						return c.FullName
					}
					if c.BusinessName != "" {
						return c.BusinessName
					}
					if c.PushName != "" {
						return c.PushName
					}
				}

				// 3. Si falló y canonicalJID es diferente al jid original, probar con jid
				if canonicalJID != jid {
					if c, err := p.client.Store.Contacts.GetContact(ctx, jid); err == nil {
						if c.FullName != "" {
							return c.FullName
						}
						if c.BusinessName != "" {
							return c.BusinessName
						}
						if c.PushName != "" {
							return c.PushName
						}
					}
				}
			}
		}
	}

	// 4. Fallback al pushName proporcionado solo si no se encontró en la libreta
	if pushName != "" {
		return pushName
	}

	return ""
}

// Connect inicializa el almacén de sesiones SQLite y la conexión WebSocket Noise con WhatsApp.
func (p *WhatsAppPlugin) Connect(ctx context.Context, req *conduitv1.ConnectRequest) (*conduitv1.ConnectResponse, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.client != nil && p.client.IsConnected() {
		go p.syncContacts(context.Background())
		return &conduitv1.ConnectResponse{Success: true, Message: "Ya conectado a WhatsApp"}, nil
	}

	homeDir, err := os.UserHomeDir()
	if err != nil {
		return nil, status.Errorf(codes.Internal, "error obteniendo home: %v", err)
	}
	dbDir := filepath.Join(homeDir, ".local", "share", "conduit", "whatsapp")
	_ = os.MkdirAll(dbDir, 0755)
	dbPath := filepath.Join(dbDir, "session.db")

	logger := waLog.Stdout("WhatsApp", "WARN", true)
	connStr := fmt.Sprintf("file:%s?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)", dbPath)
	container, err := sqlstore.New(ctx, "sqlite", connStr, logger)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "error abriendo sesión SQLite de WhatsApp: %v", err)
	}
	p.store = container

	deviceStore, err := container.GetFirstDevice(ctx)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "error cargando dispositivo: %v", err)
	}

	client := whatsmeow.NewClient(deviceStore, logger)
	p.client = client

	// Registrar handler de eventos reales entrantes
	client.AddEventHandler(func(rawEv interface{}) {
		switch v := rawEv.(type) {
		case *events.Message:
			chatJID := v.Info.Chat
			if chatJID.IsEmpty() {
				chatJID = v.Info.Sender
			}

			// Ignorar estados de WhatsApp (Stories / status@broadcast)
			if chatJID.Server == types.BroadcastServer || chatJID.String() == "status@broadcast" {
				return
			}

			// Canonicalizar chatJID (@lid -> @s.whatsapp.net si aplica) para que coincida con los canales guardados en Conduit
			chatJID = p.resolveCanonicalJID(context.Background(), chatJID)

			// Manejar reacciones con emoji a mensajes previos
			if react := v.Message.GetReactionMessage(); react != nil {
				senderJID := v.Info.Sender
				if senderJID.IsEmpty() {
					senderJID = chatJID
				} else {
					senderJID = p.resolveCanonicalJID(context.Background(), senderJID)
				}
				p.eventCh <- &conduitv1.PluginEvent{
					EventType:       "message.reaction",
					RemoteMessageId: react.GetKey().GetID(),
					RemoteSenderId:  senderJID.String(),
					ParticipantId:   senderJID.String(),
					ChatJid:         chatJID.String(),
					ReactionEmoji:   react.GetText(),
					ReactionRemoved: react.GetText() == "",
				}
				return
			}

			// Manejar mensajes de protocolo (edición y borrado / revocación)
			if protoMsg := v.Message.GetProtocolMessage(); protoMsg != nil {
				targetID := protoMsg.GetKey().GetID()
				targetChat := chatJID.String()
				if keyJIDStr := protoMsg.GetKey().GetRemoteJID(); keyJIDStr != "" {
					if parsed, err := types.ParseJID(keyJIDStr); err == nil {
						targetChat = p.resolveCanonicalJID(context.Background(), parsed).String()
					}
				}
				senderJID := v.Info.Sender
				if senderJID.IsEmpty() {
					senderJID = chatJID
				} else {
					senderJID = p.resolveCanonicalJID(context.Background(), senderJID)
				}
				switch protoMsg.GetType() {
				case waProto.ProtocolMessage_REVOKE:
					p.eventCh <- &conduitv1.PluginEvent{
						EventType:       "message.deleted",
						RemoteMessageId: targetID,
						RemoteSenderId:  senderJID.String(),
						ChatJid:         targetChat,
					}
					return
				case waProto.ProtocolMessage_MESSAGE_EDIT:
					newText := ""
					if edited := protoMsg.GetEditedMessage(); edited != nil {
						newText = edited.GetConversation()
						if newText == "" && edited.GetExtendedTextMessage() != nil {
							newText = edited.GetExtendedTextMessage().GetText()
						}
						if newText == "" && edited.GetImageMessage() != nil {
							newText = edited.GetImageMessage().GetCaption()
						}
						if newText == "" && edited.GetVideoMessage() != nil {
							newText = edited.GetVideoMessage().GetCaption()
						}
						if newText == "" && edited.GetDocumentMessage() != nil {
							newText = edited.GetDocumentMessage().GetCaption()
						}
					}
					p.eventCh <- &conduitv1.PluginEvent{
						EventType:       "message.edited",
						RemoteMessageId: targetID,
						RemoteSenderId:  senderJID.String(),
						ChatJid:         targetChat,
						Body:            newText,
						IsEdited:        true,
						EditedAtUnix:    v.Info.Timestamp.Unix(),
					}
					return
				}
			}

			// Manejar mensajes fijados
			var pin *waProto.PinInChatMessage
			if v.Message != nil {
				pin = extractWhatsAppPinInChatMessage(v.Message)
			}
			if pin == nil && v.RawMessage != nil {
				pin = extractWhatsAppPinInChatMessage(v.RawMessage)
			}
			if pin != nil && pin.GetKey() != nil {
				targetID := pin.GetKey().GetID()
				isPinned := pin.GetType() == waProto.PinInChatMessage_PIN_FOR_ALL
				p.eventCh <- &conduitv1.PluginEvent{
					EventType:       "message.pinned",
					RemoteMessageId: targetID,
					RemoteSenderId:  chatJID.String(),
					ChatJid:         chatJID.String(),
					IsPinned:        isPinned,
				}
				return
			}

			// Manejar stub de mensajes fijados en WhatsApp (StubType = 177)
			if v.SourceWebMsg != nil && v.SourceWebMsg.GetMessageStubType() == waProto.WebMessageInfo_PINNED_MESSAGE_IN_CHAT {
				params := v.SourceWebMsg.GetMessageStubParameters()
				if len(params) > 0 && params[0] != "" {
					p.eventCh <- &conduitv1.PluginEvent{
						EventType:       "message.pinned",
						RemoteMessageId: params[0],
						RemoteSenderId:  chatJID.String(),
						ChatJid:         chatJID.String(),
						IsPinned:        true,
					}
					return
				}
			}

			// Manejar encuestas entrantes (todas las versiones y empaquetados)
			var pollCreation *waProto.PollCreationMessage
			if v.Message != nil {
				pollCreation = extractWhatsAppPollCreation(v.Message)
			}
			if pollCreation == nil && v.RawMessage != nil {
				pollCreation = extractWhatsAppPollCreation(v.RawMessage)
			}
			if pollCreation != nil {
				pollProto := parseWhatsAppPollCreation(pollCreation)
				p.rememberPollMeta(v.Info.ID, v.Info.Sender, pollProto)
				p.eventCh <- &conduitv1.PluginEvent{
					EventType:       "message.received",
					RemoteMessageId: v.Info.ID,
					RemoteSenderId:  chatJID.String(),
					SenderName:      v.Info.PushName,
					Body:            pollProto.Question,
					SentAtUnix:      v.Info.Timestamp.Unix(),
					ChatJid:         chatJID.String(),
					ParticipantId:   v.Info.Sender.String(),
					ContentType:     "poll",
					Poll:            pollProto,
				}
				return
			}

			// Manejar votos de encuestas entrantes
			if pollUpdate := v.Message.GetPollUpdateMessage(); pollUpdate != nil {
				targetPollID := pollUpdate.GetPollCreationMessageKey().GetID()
				if client != nil {
					if pollVote, err := client.DecryptPollVote(context.Background(), v); err == nil && pollVote != nil {
						var updatedPoll *conduitv1.ProtoPoll
						if val, ok := p.pollCache.Load(targetPollID); ok {
							if meta, ok := val.(*whatsAppPollMeta); ok {
								optionHashes := whatsmeow.HashPollOptions(meta.OptionNames)
								var selectedNames []string
								for _, selHash := range pollVote.GetSelectedOptions() {
									for i, h := range optionHashes {
										if bytes.Equal(h, selHash) {
											selectedNames = append(selectedNames, meta.OptionNames[i])
											break
										}
									}
								}
								updatedPoll = meta.recordVotes(v.Info.Sender.String(), selectedNames)
							}
						}

						p.eventCh <- &conduitv1.PluginEvent{
							EventType:       "message.poll_update",
							RemoteMessageId: targetPollID,
							RemoteSenderId:  v.Info.Sender.String(),
							ParticipantId:   v.Info.Sender.String(),
							ChatJid:         chatJID.String(),
							Poll:            updatedPoll,
						}
					}
				}
				return
			}

			text := v.Message.GetConversation()
			if text == "" && v.Message.GetExtendedTextMessage() != nil {
				text = v.Message.GetExtendedTextMessage().GetText()
			}

			// Soporte Multimedia: Fotos, Audios, Documentos, Videos, Stickers y GIFs
			var attachments []*conduitv1.ProtoAttachment
			contentType := "text"

			if img := v.Message.GetImageMessage(); img != nil {
				contentType = "image"
				if text == "" && img.GetCaption() != "" {
					text = img.GetCaption()
				}
				fileName := fmt.Sprintf("img_%s.jpg", v.Info.ID)
				mime := img.GetMimetype()
				if mime == "" {
					mime = "image/jpeg"
				}
				// Sin descarga al recibir: el archivo se baja a demanda vía DownloadMedia
				attachments = append(attachments, attachmentFromMedia(v.Info.ID, "image", fileName, mime, img))
			} else if vid := v.Message.GetVideoMessage(); vid != nil {
				contentType = "video"
				if vid.GetGifPlayback() {
					contentType = "image" // Reproduce como GIF animado
				}
				if text == "" && vid.GetCaption() != "" {
					text = vid.GetCaption()
				}
				fileName := fmt.Sprintf("vid_%s.mp4", v.Info.ID)
				mime := vid.GetMimetype()
				if mime == "" {
					mime = "video/mp4"
				}
				// Sin descarga al recibir: el archivo se baja a demanda vía DownloadMedia
				attachments = append(attachments, attachmentFromMedia(v.Info.ID, "video", fileName, mime, vid))
			} else if aud := v.Message.GetAudioMessage(); aud != nil {
				contentType = "audio"
				fileName := fmt.Sprintf("aud_%s.ogg", v.Info.ID)
				mime := aud.GetMimetype()
				if mime == "" {
					mime = "audio/ogg; codecs=opus"
				}
				// Sin descarga al recibir: el archivo se baja a demanda vía DownloadMedia
				attachments = append(attachments, attachmentFromMedia(v.Info.ID, "audio", fileName, mime, aud))
			} else if doc := v.Message.GetDocumentMessage(); doc != nil {
				contentType = "document"
				if text == "" && doc.GetCaption() != "" {
					text = doc.GetCaption()
				}
				fileName := doc.GetFileName()
				if fileName == "" {
					fileName = doc.GetTitle()
				}
				if fileName == "" {
					fileName = fmt.Sprintf("doc_%s", v.Info.ID)
				}
				mime := doc.GetMimetype()
				if mime == "" {
					mime = "application/octet-stream"
				}
				// Sin descarga al recibir: el archivo se baja a demanda vía DownloadMedia
				attachments = append(attachments, attachmentFromMedia(v.Info.ID, "document", fileName, mime, doc))
			} else if stk := v.Message.GetStickerMessage(); stk != nil {
				contentType = "image"
				fileName := fmt.Sprintf("stk_%s.webp", v.Info.ID)
				mime := stk.GetMimetype()
				if mime == "" {
					mime = "image/webp"
				}
				// Sin descarga al recibir: el archivo se baja a demanda vía DownloadMedia
				attachments = append(attachments, attachmentFromMedia(v.Info.ID, "sticker", fileName, mime, stk))
			}

			var locProto *conduitv1.ProtoLocation
			var contactProto *conduitv1.ProtoSharedContact

			if locMsg := v.Message.GetLocationMessage(); locMsg != nil {
				locProto = parseWhatsAppLocation(locMsg)
				contentType = "location"
				if text == "" {
					text = locProto.Title
					if text == "" {
						text = fmt.Sprintf("Ubicación: %.6f, %.6f", locProto.Latitude, locProto.Longitude)
					}
				}
			} else if contMsg := v.Message.GetContactMessage(); contMsg != nil {
				contactProto = parseWhatsAppContact(contMsg)
				contentType = "contact"
				if text == "" {
					text = contactProto.DisplayName
				}
			}

			if text != "" || len(attachments) > 0 || locProto != nil || contactProto != nil {
				isFromMe := v.Info.IsFromMe
				evType := "message.received"
				if isFromMe {
					evType = "message.sent"
				}

				// Si es un grupo, el chat principal es el JID del grupo
				// y el remitente individual lo resolvemos para mostrar quién habló
				var senderDisplayName string
				participantID := ""
				groupTitle := ""
				isGroupChat := v.Info.IsGroup || chatJID.Server == types.GroupServer
				if isGroupChat {
					senderJID := p.resolveCanonicalJID(context.Background(), v.Info.Sender)
					participantID = senderJID.String()
					if isFromMe {
						senderDisplayName = "me"
					} else {
						participantName := p.resolveContactName(context.Background(), senderJID, v.Info.PushName)
						if participantName == "" {
							participantName = senderJID.User
						}
						senderDisplayName = participantName
					}
					groupTitle = p.resolveContactName(context.Background(), chatJID, "")
				} else {
					chatJID = p.resolveCanonicalJID(context.Background(), chatJID)
					contactName := p.resolveContactName(context.Background(), chatJID, v.Info.PushName)
					if isFromMe {
						senderDisplayName = "me"
					} else {
						senderDisplayName = contactName
					}
					groupTitle = contactName
				}

				chType := "dm"
				if isGroupChat {
					chType = "group"
				}

				avatar := p.resolveAvatarURL(context.Background(), chatJID)
				phoneHint := ""
				if !isGroupChat {
					phoneHint = phoneFromCanonicalJID(chatJID)
				}
				quotedStanzaID := extractQuotedStanzaID(v.Message)
				buttonRows := parseWhatsAppButtons(v.Message)

				p.eventCh <- &conduitv1.PluginEvent{
					EventType:       evType,
					RemoteMessageId: v.Info.ID,
					RemoteSenderId:  chatJID.String(),
					SenderName:      senderDisplayName,
					Body:            text,
					SentAtUnix:      v.Info.Timestamp.Unix(),
					ChatJid:         chatJID.String(),
					ParticipantId:   participantID,
					ChannelName:     groupTitle,
					ServerName:      groupTitle,
					ChannelType:     chType,
					AvatarUrl:       avatar,
					ContentType:     contentType,
					Attachments:     attachments,
					IdentityPhone:   phoneHint,
					ReplyToRemoteId: quotedStanzaID,
					ButtonRows:      buttonRows,
					Location:        locProto,
					SharedContact:   contactProto,
				}
			}

		case *events.HistorySync:
			// Teléfono envió bloque histórico de conversaciones y mensajes
			if v.Data != nil {
				go func(syncData *events.HistorySync) {
					for _, conv := range syncData.Data.GetConversations() {
						chatJIDStr := conv.GetID()
						if chatJIDStr == "" {
							continue
						}
						chatJID, err := types.ParseJID(chatJIDStr)
						if err != nil {
							continue
						}
						// Ignorar estados / broadcasts en sincronización de historial
						if chatJID.Server == types.BroadcastServer || chatJID.String() == "status@broadcast" {
							continue
						}
						if chatJID.Server != types.GroupServer {
							chatJID = p.resolveCanonicalJID(context.Background(), chatJID)
						}

						chatName := conv.GetName()
						if chatName == "" {
							chatName = p.resolveContactName(context.Background(), chatJID, "")
						}

						for _, hMsg := range conv.GetMessages() {
							for _, ev := range p.processHistoryMessage(chatJID, chatName, hMsg) {
								p.eventCh <- ev
							}
						}
					}
				}(v)
			}

		case *events.Receipt:
			// Solo las lecturas propias (otro dispositivo) se reflejan como leídos locales.
			if ev := readReceiptEvent(v, func(j types.JID) types.JID {
				return p.resolveCanonicalJID(context.Background(), j)
			}); ev != nil {
				p.eventCh <- ev
			}

		case *events.Connected:
			p.eventCh <- &conduitv1.PluginEvent{
				EventType:       "channel.ready",
				RemoteMessageId: "connected",
				RemoteSenderId:  "whatsapp-system",
				Body:            "Conexión a WhatsApp activa",
				SentAtUnix:      time.Now().Unix(),
			}
			go p.syncContacts(context.Background())
		}
	})

	if client.Store.ID == nil {
		// Nuevo emparejamiento requerido: generar QR
		qrChan, _ := client.GetQRChannel(context.Background())
		if err := client.Connect(); err != nil {
			return nil, status.Errorf(codes.Internal, "error al conectar socket: %v", err)
		}

		go func() {
			for evt := range qrChan {
				if evt.Event == "code" {
					qrFile := filepath.Join(dbDir, "qr.png")
					_ = qrcode.WriteFile(evt.Code, qrcode.Medium, 256, qrFile)

					qr, _ := qrcode.New(evt.Code, qrcode.Medium)
					fmt.Println("\n================= ESCANEA EL CÓDIGO QR CON WHATSAPP =================")
					fmt.Println(qr.ToSmallString(false))
					fmt.Printf("📷 O abre la imagen generada en: %s\n", qrFile)
					fmt.Println("=====================================================================")

					p.eventCh <- &conduitv1.PluginEvent{
						EventType:       "channel.auth_qr",
						RemoteMessageId: "qr-code",
						RemoteSenderId:  "whatsapp-system",
						Body:            evt.Code,
						SentAtUnix:      time.Now().Unix(),
					}
				}
			}
		}()

		return &conduitv1.ConnectResponse{
			Success: true,
			Message: "Esperando escaneo de código QR en terminal...",
		}, nil
	}

	// Sesión guardada previa: conectar directo
	if err := client.Connect(); err != nil {
		return nil, status.Errorf(codes.Internal, "error conectando sesión existente: %v", err)
	}

	go p.syncContacts(context.Background())

	return &conduitv1.ConnectResponse{
		Success: true,
		Message: "Sesión previa de WhatsApp reconectada exitosamente",
	}, nil
}

// processHistoryMessage extrae eventos a despachar a partir de un mensaje recibido en la sincronización de historial (HistorySync).
func (p *WhatsAppPlugin) processHistoryMessage(chatJID types.JID, chatName string, hMsg *waHistorySync.HistorySyncMsg) []*conduitv1.PluginEvent {
	if hMsg == nil {
		return nil
	}
	wMsg := hMsg.GetMessage()
	if wMsg == nil {
		return nil
	}

	var events []*conduitv1.PluginEvent

	// 1. Detectar eventos de mensajes fijados en el historial
	if pinInChat := wMsg.GetPinInChat(); pinInChat != nil && pinInChat.GetKey() != nil {
		targetID := pinInChat.GetKey().GetID()
		if targetID != "" {
			isPinned := pinInChat.GetType() == waProto.PinInChat_PIN_FOR_ALL
			events = append(events, &conduitv1.PluginEvent{
				EventType:       "message.pinned",
				RemoteMessageId: targetID,
				RemoteSenderId:  chatJID.String(),
				ChatJid:         chatJID.String(),
				IsPinned:        isPinned,
			})
		}
	}
	if wMsg.GetMessage() != nil {
		if pinMsg := extractWhatsAppPinInChatMessage(wMsg.GetMessage()); pinMsg != nil && pinMsg.GetKey() != nil {
			targetID := pinMsg.GetKey().GetID()
			if targetID != "" {
				isPinned := pinMsg.GetType() == waProto.PinInChatMessage_PIN_FOR_ALL
				events = append(events, &conduitv1.PluginEvent{
					EventType:       "message.pinned",
					RemoteMessageId: targetID,
					RemoteSenderId:  chatJID.String(),
					ChatJid:         chatJID.String(),
					IsPinned:        isPinned,
				})
			}
		}
	}

	// Detectar si el mensaje es un stub de fijado (PINNED_MESSAGE_IN_CHAT = 177)
	if wMsg.GetMessageStubType() == waProto.WebMessageInfo_PINNED_MESSAGE_IN_CHAT {
		params := wMsg.GetMessageStubParameters()
		if len(params) > 0 && params[0] != "" {
			events = append(events, &conduitv1.PluginEvent{
				EventType:       "message.pinned",
				RemoteMessageId: params[0],
				RemoteSenderId:  chatJID.String(),
				ChatJid:         chatJID.String(),
				IsPinned:        true,
			})
		}
	}

	if wMsg.GetMessage() == nil {
		return events
	}

	mText := wMsg.GetMessage().GetConversation()
	if mText == "" && wMsg.GetMessage().GetExtendedTextMessage() != nil {
		mText = wMsg.GetMessage().GetExtendedTextMessage().GetText()
	}

	var pollProto *conduitv1.ProtoPoll
	if pollCreation := extractWhatsAppPollCreation(wMsg.GetMessage()); pollCreation != nil {
		pollProto = parseWhatsAppPollCreation(pollCreation)
		mText = pollProto.Question
	}

	if mText == "" && pollProto == nil {
		return events
	}

	msgTimestamp := int64(wMsg.GetMessageTimestamp())
	if msgTimestamp == 0 {
		msgTimestamp = time.Now().Unix()
	}
	msgID := wMsg.GetKey().GetID()
	if msgID == "" {
		msgID = fmt.Sprintf("h-%d", time.Now().UnixNano())
	}

	fromMe := wMsg.GetKey().GetFromMe()
	evType := "message.received"
	if fromMe {
		evType = "message.sent"
	}

	participantStr := wMsg.GetKey().GetParticipant()
	senderName := chatName
	if participantStr != "" {
		if partJID, err := types.ParseJID(participantStr); err == nil {
			partJID = p.resolveCanonicalJID(context.Background(), partJID)
			participantStr = partJID.String()
			pName := p.resolveContactName(context.Background(), partJID, "")
			if pName != "" {
				senderName = pName
			} else {
				senderName = partJID.User
			}
		}
	}

	chType := "dm"
	if chatJID.Server == types.GroupServer {
		chType = "group"
	}

	avatar := p.resolveAvatarURL(context.Background(), chatJID)
	phoneHint := ""
	if chatJID.Server != types.GroupServer {
		phoneHint = phoneFromCanonicalJID(chatJID)
	}

	contentType := "text"
	if pollProto != nil {
		contentType = "poll"
	}

	isPinned := false
	if pinInChat := wMsg.GetPinInChat(); pinInChat != nil && pinInChat.GetKey() != nil {
		if pinInChat.GetKey().GetID() == msgID {
			isPinned = pinInChat.GetType() == waProto.PinInChat_PIN_FOR_ALL
		}
	}

	if pollProto != nil {
		senderJID := chatJID
		if participantStr != "" {
			if pJID, err := types.ParseJID(participantStr); err == nil {
				senderJID = pJID
			}
		}
		p.rememberPollMeta(msgID, senderJID, pollProto)
	}

	events = append(events, &conduitv1.PluginEvent{
		EventType:       evType,
		RemoteMessageId: msgID,
		RemoteSenderId:  chatJID.String(),
		SenderName:      senderName,
		Body:            mText,
		SentAtUnix:      msgTimestamp,
		ChatJid:         chatJID.String(),
		ParticipantId:   participantStr,
		ChannelName:     chatName,
		ServerName:      chatName,
		ChannelType:     chType,
		AvatarUrl:       avatar,
		IdentityPhone:   phoneHint,
		ContentType:     contentType,
		Poll:            pollProto,
		IsPinned:        isPinned,
	})

	return events
}

// Disconnect cierra la conexión del cliente whatsmeow.
func (p *WhatsAppPlugin) Disconnect(ctx context.Context, req *conduitv1.DisconnectRequest) (*conduitv1.DisconnectResponse, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.client != nil {
		p.client.Disconnect()
	}
	return &conduitv1.DisconnectResponse{Success: true}, nil
}

// SendOutgoingMessage envía un mensaje real (texto y/o adjuntos) a un destinatario de WhatsApp.
func (p *WhatsAppPlugin) SendOutgoingMessage(ctx context.Context, req *conduitv1.OutgoingMessageRequest) (*conduitv1.OutgoingMessageResponse, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()

	if p.client == nil || !p.client.IsConnected() {
		return nil, status.Error(codes.Unavailable, "el cliente de WhatsApp no está conectado")
	}

	recipient := req.RemoteRecipientId
	if err := validateOutgoingRequest(req.GetBody(), len(req.GetAttachments())); err != nil {
		return &conduitv1.OutgoingMessageResponse{
			Success:      false,
			ErrorMessage: err.Error(),
		}, nil
	}

	jid, ok := resolveRecipientJID(recipient)
	if !ok {
		return &conduitv1.OutgoingMessageResponse{
			Success:      false,
			ErrorMessage: fmt.Sprintf("destinatario de WhatsApp inválido: %s", recipient),
		}, nil
	}

	replyToRemoteID := req.GetReplyToRemoteId()
	if len(req.GetAttachments()) == 0 {
		resp, err := p.client.SendMessage(ctx, jid, buildTextMessageWithReply(req.GetBody(), replyToRemoteID))
		if err != nil {
			return &conduitv1.OutgoingMessageResponse{
				Success:      false,
				ErrorMessage: fmt.Sprintf("error enviando mensaje en WhatsApp: %v", err),
			}, nil
		}
		return &conduitv1.OutgoingMessageResponse{
			Success:         true,
			RemoteMessageId: resp.ID,
		}, nil
	}

	firstMessageID := ""
	for i, att := range req.GetAttachments() {
		fileName := attachmentFileName(att)
		data, err := os.ReadFile(att.GetFilePath())
		if err != nil {
			return &conduitv1.OutgoingMessageResponse{
				Success:      false,
				ErrorMessage: fmt.Sprintf("no se pudo leer el adjunto %s: %v", fileName, err),
			}, nil
		}

		mimeType := attachmentMIMEType(att)
		data, mimeType, fileName = prepareOutgoingAudioData(att.GetFilePath(), data, mimeType, fileName)
		upload, err := p.client.Upload(ctx, data, whatsmeowMediaTypeForMIME(mimeType))
		if err != nil {
			return &conduitv1.OutgoingMessageResponse{
				Success:      false,
				ErrorMessage: fmt.Sprintf("error subiendo el adjunto %s: %v", fileName, err),
			}, nil
		}

		// El caption solo acompaña al primer adjunto para no duplicar el texto.
		caption := ""
		if i == 0 {
			caption = req.GetBody()
		}

		mediaMsg := buildMediaMessage(upload, mimeType, fileName, caption)
		if replyToRemoteID != "" {
			attachQuotedStanzaID(mediaMsg, replyToRemoteID)
		}

		resp, err := p.client.SendMessage(ctx, jid, mediaMsg)
		if err != nil {
			return &conduitv1.OutgoingMessageResponse{
				Success:      false,
				ErrorMessage: fmt.Sprintf("error enviando el adjunto %s: %v", fileName, err),
			}, nil
		}
		if i == 0 {
			firstMessageID = resp.ID
		}
	}

	return &conduitv1.OutgoingMessageResponse{
		Success:         true,
		RemoteMessageId: firstMessageID,
	}, nil
}

// SendReaction envía o quita una reacción a un mensaje en WhatsApp.
func (p *WhatsAppPlugin) SendReaction(ctx context.Context, req *conduitv1.PluginReactionRequest) (*conduitv1.PluginReactionResponse, error) {
	p.mu.RLock()
	client := p.client
	p.mu.RUnlock()

	if client == nil || !client.IsConnected() {
		return nil, status.Error(codes.Unavailable, "el cliente de WhatsApp no está conectado")
	}

	chatJID, ok := resolveRecipientJID(req.GetRemoteChatId())
	if !ok {
		return &conduitv1.PluginReactionResponse{
			Success:      false,
			ErrorMessage: fmt.Sprintf("destinatario de WhatsApp inválido: %s", req.GetRemoteChatId()),
		}, nil
	}

	targetMsgID := types.MessageID(req.GetRemoteMessageId())
	emoji := strings.TrimSpace(req.GetEmoji())

	var senderJID types.JID
	if chatJID.Server != types.GroupServer {
		senderJID = chatJID
	}

	reactionMsg := client.BuildReaction(chatJID, senderJID, targetMsgID, emoji)
	_, err := client.SendMessage(ctx, chatJID, reactionMsg)
	if err != nil {
		return &conduitv1.PluginReactionResponse{
			Success:      false,
			ErrorMessage: fmt.Sprintf("error enviando reacción a WhatsApp: %v", err),
		}, nil
	}

	return &conduitv1.PluginReactionResponse{Success: true}, nil
}

// SendVote envía un voto en una encuesta de WhatsApp.
func (p *WhatsAppPlugin) SendVote(ctx context.Context, req *conduitv1.PluginVoteRequest) (*conduitv1.PluginVoteResponse, error) {
	p.mu.RLock()
	client := p.client
	p.mu.RUnlock()

	if client == nil || !client.IsConnected() {
		return nil, status.Error(codes.Unavailable, "el cliente de WhatsApp no está conectado")
	}

	chatJID, ok := resolveRecipientJID(req.GetRemoteChatId())
	if !ok {
		return &conduitv1.PluginVoteResponse{
			Success:      false,
			ErrorMessage: fmt.Sprintf("destinatario de WhatsApp inválido: %s", req.GetRemoteChatId()),
		}, nil
	}

	targetPollID := req.GetRemoteMessageId()
	var senderJID types.JID
	var optionNames []string
	var meta *whatsAppPollMeta

	if val, ok := p.pollCache.Load(targetPollID); ok {
		if m, ok := val.(*whatsAppPollMeta); ok {
			meta = m
			senderJID = m.SenderJID
			optionNames = m.OptionNames
		}
	}
	if senderJID.IsEmpty() {
		senderJID = chatJID
	}

	pollInfo := &types.MessageInfo{
		MessageSource: types.MessageSource{
			Chat:     chatJID,
			Sender:   senderJID,
			IsFromMe: false,
		},
		ID: types.MessageID(targetPollID),
	}

	var selectedOptionNames []string
	for _, opt := range req.GetSelectedOptions() {
		if idx, err := strconv.Atoi(opt); err == nil && idx >= 0 && idx < len(optionNames) {
			selectedOptionNames = append(selectedOptionNames, optionNames[idx])
		} else {
			selectedOptionNames = append(selectedOptionNames, opt)
		}
	}

	voteMsg, err := client.BuildPollVote(ctx, pollInfo, selectedOptionNames)
	if err != nil {
		return &conduitv1.PluginVoteResponse{
			Success:      false,
			ErrorMessage: fmt.Sprintf("error construyendo voto de WhatsApp: %v", err),
		}, nil
	}

	_, err = client.SendMessage(ctx, chatJID, voteMsg)
	if err != nil {
		return &conduitv1.PluginVoteResponse{
			Success:      false,
			ErrorMessage: fmt.Sprintf("error enviando voto a WhatsApp: %v", err),
		}, nil
	}

	if meta != nil {
		meta.recordVotes("me", selectedOptionNames)
	}

	return &conduitv1.PluginVoteResponse{
		Success: true,
	}, nil
}

// ClickButton envía la respuesta de un botón presionado a WhatsApp.
func (p *WhatsAppPlugin) ClickButton(ctx context.Context, req *conduitv1.PluginClickButtonRequest) (*conduitv1.PluginClickButtonResponse, error) {
	p.mu.RLock()
	client := p.client
	p.mu.RUnlock()

	if client == nil || !client.IsConnected() {
		return nil, status.Error(codes.Unavailable, "el cliente de WhatsApp no está conectado")
	}

	chatJID, ok := resolveRecipientJID(req.GetRemoteChatId())
	if !ok {
		return &conduitv1.PluginClickButtonResponse{
			Success:      false,
			ErrorMessage: fmt.Sprintf("destinatario de WhatsApp inválido: %s", req.GetRemoteChatId()),
		}, nil
	}

	btnMsg := &waProto.Message{
		ButtonsResponseMessage: &waProto.ButtonsResponseMessage{
			SelectedButtonID: proto.String(req.ButtonId),
			Response: &waProto.ButtonsResponseMessage_SelectedDisplayText{
				SelectedDisplayText: req.ButtonText,
			},
			ContextInfo: &waProto.ContextInfo{
				StanzaID: proto.String(req.RemoteMessageId),
			},
		},
	}

	_, err := client.SendMessage(ctx, chatJID, btnMsg)
	if err != nil {
		return &conduitv1.PluginClickButtonResponse{
			Success:      false,
			ErrorMessage: fmt.Sprintf("error enviando respuesta de botón a WhatsApp: %v", err),
		}, nil
	}

	return &conduitv1.PluginClickButtonResponse{
		Success:         true,
		FeedbackMessage: "Opción enviada",
	}, nil
}

// EditMessage edita el texto de un mensaje en WhatsApp.
func (p *WhatsAppPlugin) EditMessage(ctx context.Context, req *conduitv1.PluginEditMessageRequest) (*conduitv1.PluginEditMessageResponse, error) {
	p.mu.RLock()
	client := p.client
	p.mu.RUnlock()

	if client == nil || !client.IsConnected() {
		return nil, status.Error(codes.Unavailable, "el cliente de WhatsApp no está conectado")
	}

	chatJID, ok := resolveRecipientJID(req.GetRemoteChatId())
	if !ok {
		return &conduitv1.PluginEditMessageResponse{
			Success:      false,
			ErrorMessage: fmt.Sprintf("destinatario de WhatsApp inválido: %s", req.GetRemoteChatId()),
		}, nil
	}

	targetMsgID := types.MessageID(req.GetRemoteMessageId())
	editMsg := client.BuildEdit(chatJID, targetMsgID, buildTextMessage(req.GetNewBody()))

	_, err := client.SendMessage(ctx, chatJID, editMsg)
	if err != nil {
		return &conduitv1.PluginEditMessageResponse{
			Success:      false,
			ErrorMessage: fmt.Sprintf("error editando mensaje en WhatsApp: %v", err),
		}, nil
	}

	return &conduitv1.PluginEditMessageResponse{Success: true}, nil
}

// DeleteMessage revoca un mensaje para todos en WhatsApp.
func (p *WhatsAppPlugin) DeleteMessage(ctx context.Context, req *conduitv1.PluginDeleteMessageRequest) (*conduitv1.PluginDeleteMessageResponse, error) {
	p.mu.RLock()
	client := p.client
	p.mu.RUnlock()

	if client == nil || !client.IsConnected() {
		return nil, status.Error(codes.Unavailable, "el cliente de WhatsApp no está conectado")
	}

	chatJID, ok := resolveRecipientJID(req.GetRemoteChatId())
	if !ok {
		return &conduitv1.PluginDeleteMessageResponse{
			Success:      false,
			ErrorMessage: fmt.Sprintf("destinatario de WhatsApp inválido: %s", req.GetRemoteChatId()),
		}, nil
	}

	targetMsgID := types.MessageID(req.GetRemoteMessageId())
	revokeMsg := client.BuildRevoke(chatJID, types.EmptyJID, targetMsgID)

	_, err := client.SendMessage(ctx, chatJID, revokeMsg)
	if err != nil {
		return &conduitv1.PluginDeleteMessageResponse{
			Success:      false,
			ErrorMessage: fmt.Sprintf("error eliminando mensaje en WhatsApp: %v", err),
		}, nil
	}

	return &conduitv1.PluginDeleteMessageResponse{Success: true}, nil
}

// PinMessage fija o desfija un mensaje en WhatsApp.
func (p *WhatsAppPlugin) PinMessage(ctx context.Context, req *conduitv1.PluginPinMessageRequest) (*conduitv1.PluginPinMessageResponse, error) {
	p.mu.RLock()
	client := p.client
	p.mu.RUnlock()

	if client == nil || !client.IsConnected() {
		return nil, status.Error(codes.Unavailable, "el cliente de WhatsApp no está conectado")
	}

	chatJID, ok := resolveRecipientJID(req.GetRemoteChatId())
	if !ok {
		return &conduitv1.PluginPinMessageResponse{
			Success:      false,
			ErrorMessage: fmt.Sprintf("destinatario de WhatsApp inválido: %s", req.GetRemoteChatId()),
		}, nil
	}

	pinType := waProto.PinInChatMessage_PIN_FOR_ALL
	if !req.GetPinned() {
		pinType = waProto.PinInChatMessage_UNPIN_FOR_ALL
	}

	key := buildPinMessageKey(chatJID, req)

	pinMsg := &waProto.Message{
		PinInChatMessage: &waProto.PinInChatMessage{
			Key:               key,
			Type:              pinType.Enum(),
			SenderTimestampMS: proto.Int64(time.Now().UnixMilli()),
		},
	}

	_, err := client.SendMessage(ctx, chatJID, pinMsg)
	if err != nil {
		return &conduitv1.PluginPinMessageResponse{
			Success:      false,
			ErrorMessage: fmt.Sprintf("error actualizando mensaje fijado en WhatsApp: %v", err),
		}, nil
	}

	return &conduitv1.PluginPinMessageResponse{Success: true}, nil
}

func buildPinMessageKey(chatJID types.JID, req *conduitv1.PluginPinMessageRequest) *waCommon.MessageKey {
	key := &waCommon.MessageKey{
		RemoteJID: proto.String(chatJID.String()),
		FromMe:    proto.Bool(req.GetFromMe()),
		ID:        proto.String(req.GetRemoteMessageId()),
	}
	if !req.GetFromMe() && req.GetParticipantId() != "" && chatJID.Server == types.GroupServer {
		key.Participant = proto.String(req.GetParticipantId())
	}
	return key
}

// StreamIncomingEvents transmite los eventos reales hacia el Core.
func (p *WhatsAppPlugin) StreamIncomingEvents(req *conduitv1.PluginStreamRequest, stream conduitv1.PluginService_StreamIncomingEventsServer) error {
	for {
		select {
		case <-stream.Context().Done():
			return nil
		case ev := <-p.eventCh:
			if err := stream.Send(ev); err != nil {
				return err
			}
		}
	}
}

// SyncChatHistory solicita mensajes históricos previos al teléfono principal usando HistorySync On-Demand.
func (p *WhatsAppPlugin) SyncChatHistory(ctx context.Context, req *conduitv1.SyncChatHistoryRequest) (*conduitv1.SyncChatHistoryResponse, error) {
	p.mu.RLock()
	client := p.client
	p.mu.RUnlock()

	if client == nil || !client.IsConnected() {
		return nil, status.Error(codes.Unavailable, "cliente WhatsApp no conectado")
	}

	chatJID, err := types.ParseJID(req.RemoteChatId)
	if err != nil || chatJID.Server == "" {
		cleanNum := ""
		for _, r := range req.RemoteChatId {
			if r >= '0' && r <= '9' {
				cleanNum += string(r)
			}
		}
		if cleanNum == "" {
			return &conduitv1.SyncChatHistoryResponse{
				Success: false,
				Message: fmt.Sprintf("JID de chat inválido: %s", req.RemoteChatId),
			}, nil
		}
		chatJID = types.NewJID(cleanNum, types.DefaultUserServer)
	}

	if strings.TrimSpace(req.OldestRemoteMessageId) == "" {
		// Sin un mensaje conocido no hay desde dónde pedir: WhatsApp reenvía los
		// mensajes pendientes solo cuando el dispositivo vuelve a estar en línea.
		return &conduitv1.SyncChatHistoryResponse{
			Success: false,
			Message: "WhatsApp necesita un mensaje de referencia para pedir historial",
		}, nil
	}

	count := int(req.Limit)
	if count <= 0 {
		count = 50
	}
	if count > 100 {
		count = 100
	}

	ts := time.Unix(req.OldestMessageTimestampUnix, 0)
	if req.OldestMessageTimestampUnix <= 0 {
		ts = time.Now()
	}

	msgInfo := &types.MessageInfo{
		MessageSource: types.MessageSource{
			Chat:     chatJID,
			IsFromMe: req.OldestMessageFromMe,
			IsGroup:  chatJID.Server == types.GroupServer,
		},
		ID:        req.OldestRemoteMessageId,
		Timestamp: ts,
	}

	syncReqMsg := client.BuildHistorySyncRequest(msgInfo, count)
	if syncReqMsg == nil {
		return &conduitv1.SyncChatHistoryResponse{
			Success: false,
			Message: "no se pudo construir la solicitud HistorySync",
		}, nil
	}

	_, err = client.SendPeerMessage(ctx, syncReqMsg)
	if err != nil {
		return &conduitv1.SyncChatHistoryResponse{
			Success: false,
			Message: fmt.Sprintf("error enviando solicitud On-Demand al teléfono: %v", err),
		}, nil
	}

	return &conduitv1.SyncChatHistoryResponse{
		Success: true,
		Message: "Solicitud de sincronización histórica enviada al teléfono principal",
	}, nil
}

// MarkRead envía confirmación de lectura a los servidores de WhatsApp para los mensajes indicados.
func (p *WhatsAppPlugin) MarkRead(ctx context.Context, req *conduitv1.PluginMarkReadRequest) (*conduitv1.PluginMarkReadResponse, error) {
	p.mu.RLock()
	client := p.client
	p.mu.RUnlock()

	if client == nil || !client.IsConnected() {
		return &conduitv1.PluginMarkReadResponse{
			Success:      false,
			ErrorMessage: "cliente WhatsApp desconectado",
		}, nil
	}

	if req.RemoteChatId == "" || len(req.RemoteMessageIds) == 0 {
		return &conduitv1.PluginMarkReadResponse{Success: true}, nil
	}

	chatJID, err := types.ParseJID(req.RemoteChatId)
	if err != nil {
		cleanNum := strings.TrimPrefix(req.RemoteChatId, "+")
		chatJID = types.NewJID(cleanNum, types.DefaultUserServer)
	}

	// Con remitentes paralelos (grupos) se envía un recibo por remitente.
	var batches []readBatch
	if len(req.RemoteSenderIds) == len(req.RemoteMessageIds) {
		batches = groupReadIDsBySender(req.RemoteMessageIds, req.RemoteSenderIds, req.SenderJid)
	} else {
		batches = []readBatch{{Sender: req.SenderJid, IDs: req.RemoteMessageIds}}
	}

	for _, batch := range batches {
		senderJID := types.EmptyJID
		if batch.Sender != "" {
			if parsed, perr := types.ParseJID(batch.Sender); perr == nil {
				senderJID = parsed
			}
		}
		messageIDs := make([]types.MessageID, len(batch.IDs))
		for i, id := range batch.IDs {
			messageIDs[i] = types.MessageID(id)
		}
		if err := client.MarkRead(ctx, messageIDs, time.Now(), chatJID, senderJID); err != nil {
			return &conduitv1.PluginMarkReadResponse{
				Success:      false,
				ErrorMessage: fmt.Sprintf("error enviando recibo de lectura (remitente %q): %v", batch.Sender, err),
			}, nil
		}
	}

	return &conduitv1.PluginMarkReadResponse{Success: true}, nil
}

// readBatch agrupa IDs de mensajes que comparten remitente.
type readBatch struct {
	Sender string
	IDs    []string
}

// groupReadIDsBySender agrupa los IDs por remitente conservando el orden de aparición.
// Un remitente vacío usa fallback.
func groupReadIDsBySender(ids, senders []string, fallback string) []readBatch {
	var batches []readBatch
	index := map[string]int{}
	for i, id := range ids {
		sender := fallback
		if i < len(senders) && senders[i] != "" {
			sender = senders[i]
		}
		pos, ok := index[sender]
		if !ok {
			pos = len(batches)
			index[sender] = pos
			batches = append(batches, readBatch{Sender: sender})
		}
		batches[pos].IDs = append(batches[pos].IDs, id)
	}
	return batches
}

// readReceiptEvent convierte un recibo en evento messages.read. Devuelve nil si debe ignorarse:
// solo cuenta la lectura propia (IsFromMe) hecha desde otro dispositivo.
func readReceiptEvent(v *events.Receipt, canonical func(types.JID) types.JID) *conduitv1.PluginEvent {
	if v == nil || (v.Type != types.ReceiptTypeRead && v.Type != types.ReceiptTypeReadSelf) || !v.IsFromMe {
		return nil
	}
	if v.Chat.Server == types.BroadcastServer || v.Chat.User == "status" {
		return nil
	}
	chat := v.Chat
	channelType := "dm"
	if chat.Server == types.GroupServer {
		channelType = "group"
	} else {
		if canonical != nil {
			chat = canonical(chat)
		}
		// Si el store no conoce el LID, WhatsApp manda el número real en el propio recibo.
		if chat.Server == types.HiddenUserServer && v.RecipientAlt.Server == types.DefaultUserServer {
			chat = v.RecipientAlt.ToNonAD()
		}
	}
	msgIDs := make([]string, len(v.MessageIDs))
	for i, id := range v.MessageIDs {
		msgIDs[i] = string(id)
	}
	chatStr := chat.String()
	return &conduitv1.PluginEvent{
		EventType:       "messages.read",
		RemoteSenderId:  chatStr,
		ChatJid:         chatStr,
		ChannelType:     channelType,
		SentAtUnix:      v.Timestamp.Unix(),
		RemoteMessageId: strings.Join(msgIDs, ","),
	}
}
