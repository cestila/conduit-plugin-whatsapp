package main

import (
	"strings"
	"testing"

	conduitv1 "github.com/cestila/ConduitOmniChat/sdk/conduitv1"
	"go.mau.fi/whatsmeow"
	waProto "go.mau.fi/whatsmeow/binary/proto"
	waCommon "go.mau.fi/whatsmeow/proto/waCommon"
	waHistorySync "go.mau.fi/whatsmeow/proto/waHistorySync"
	"go.mau.fi/whatsmeow/types"
	"google.golang.org/protobuf/proto"
)

func TestBuildTextMessageKeepsConversation(t *testing.T) {
	msg := buildTextMessage("hola mundo")

	if got := msg.GetConversation(); got != "hola mundo" {
		t.Fatalf("Conversation = %q, se esperaba %q", got, "hola mundo")
	}
	if msg.GetExtendedTextMessage() != nil || msg.GetImageMessage() != nil {
		t.Fatalf("un mensaje de solo texto no debe llevar otros payloads: %v", msg)
	}
}

func TestMediaKindForMIME(t *testing.T) {
	tests := []struct {
		name     string
		mimeType string
		want     mediaKind
	}{
		{name: "imagen", mimeType: "image/png", want: mediaKindImage},
		{name: "imagen con parámetros", mimeType: "image/jpeg; charset=binary", want: mediaKindImage},
		{name: "video", mimeType: "video/mp4", want: mediaKindVideo},
		{name: "audio", mimeType: "audio/mpeg", want: mediaKindAudio},
		{name: "audio opus", mimeType: "audio/ogg; codecs=opus", want: mediaKindAudio},
		{name: "documento", mimeType: "application/pdf", want: mediaKindDocument},
		{name: "desconocido", mimeType: "", want: mediaKindDocument},
		{name: "mayúsculas", mimeType: "IMAGE/GIF", want: mediaKindImage},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := mediaKindForMIME(tt.mimeType); got != tt.want {
				t.Fatalf("mediaKindForMIME(%q) = %v, se esperaba %v", tt.mimeType, got, tt.want)
			}
		})
	}
}

func TestIsVoiceNoteMIME(t *testing.T) {
	tests := []struct {
		mimeType string
		want     bool
	}{
		{mimeType: "audio/ogg; codecs=opus", want: true},
		{mimeType: "audio/ogg", want: true},
		{mimeType: "audio/opus", want: true},
		{mimeType: "audio/webm; codecs=opus", want: false},
		{mimeType: "audio/webm", want: false},
		{mimeType: "audio/mpeg", want: false},
		{mimeType: "audio/mp4", want: false},
		{mimeType: "audio/mp4; codecs=opus", want: false},
		{mimeType: "", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.mimeType, func(t *testing.T) {
			if got := isVoiceNoteMIME(tt.mimeType); got != tt.want {
				t.Fatalf("isVoiceNoteMIME(%q) = %v, se esperaba %v", tt.mimeType, got, tt.want)
			}
		})
	}
}

func TestWhatsmeowMediaTypeForMIME(t *testing.T) {
	tests := []struct {
		mimeType string
		want     whatsmeow.MediaType
	}{
		{mimeType: "image/png", want: whatsmeow.MediaImage},
		{mimeType: "video/mp4", want: whatsmeow.MediaVideo},
		{mimeType: "audio/ogg; codecs=opus", want: whatsmeow.MediaAudio},
		{mimeType: "application/pdf", want: whatsmeow.MediaDocument},
		{mimeType: "", want: whatsmeow.MediaDocument},
	}

	for _, tt := range tests {
		t.Run(tt.mimeType, func(t *testing.T) {
			if got := whatsmeowMediaTypeForMIME(tt.mimeType); got != tt.want {
				t.Fatalf("whatsmeowMediaTypeForMIME(%q) = %v, se esperaba %v", tt.mimeType, got, tt.want)
			}
		})
	}
}

func TestBuildMediaMessage(t *testing.T) {
	upload := whatsmeow.UploadResponse{
		URL:           "https://mmg.whatsapp.net/d/f/test",
		DirectPath:    "/v/t62/test",
		MediaKey:      []byte{1, 2, 3},
		FileEncSHA256: []byte{4, 5, 6},
		FileSHA256:    []byte{7, 8, 9},
		FileLength:    1234,
	}

	assertUploadFields := func(t *testing.T, msg *messageFields) {
		t.Helper()
		if msg.url != upload.URL || msg.directPath != upload.DirectPath {
			t.Fatalf("URL/DirectPath = %q/%q, se esperaban %q/%q", msg.url, msg.directPath, upload.URL, upload.DirectPath)
		}
		if string(msg.mediaKey) != string(upload.MediaKey) {
			t.Fatalf("MediaKey = %v, se esperaba %v", msg.mediaKey, upload.MediaKey)
		}
		if string(msg.fileEncSHA256) != string(upload.FileEncSHA256) {
			t.Fatalf("FileEncSHA256 = %v, se esperaba %v", msg.fileEncSHA256, upload.FileEncSHA256)
		}
		if string(msg.fileSHA256) != string(upload.FileSHA256) {
			t.Fatalf("FileSHA256 = %v, se esperaba %v", msg.fileSHA256, upload.FileSHA256)
		}
		if msg.fileLength != upload.FileLength {
			t.Fatalf("FileLength = %d, se esperaba %d", msg.fileLength, upload.FileLength)
		}
	}

	fieldsFor := func(t *testing.T, msgKind mediaKind, mimeType, fileName, caption string) *messageFields {
		t.Helper()
		msg := buildMediaMessage(upload, mimeType, fileName, caption)
		switch msgKind {
		case mediaKindImage:
			img := msg.GetImageMessage()
			if img == nil {
				t.Fatalf("se esperaba ImageMessage, se obtuvo %v", msg)
			}
			return &messageFields{
				url: img.GetURL(), directPath: img.GetDirectPath(), mediaKey: img.GetMediaKey(),
				fileEncSHA256: img.GetFileEncSHA256(), fileSHA256: img.GetFileSHA256(),
				fileLength: img.GetFileLength(), mimeType: img.GetMimetype(), caption: img.GetCaption(),
			}
		case mediaKindVideo:
			vid := msg.GetVideoMessage()
			if vid == nil {
				t.Fatalf("se esperaba VideoMessage, se obtuvo %v", msg)
			}
			return &messageFields{
				url: vid.GetURL(), directPath: vid.GetDirectPath(), mediaKey: vid.GetMediaKey(),
				fileEncSHA256: vid.GetFileEncSHA256(), fileSHA256: vid.GetFileSHA256(),
				fileLength: vid.GetFileLength(), mimeType: vid.GetMimetype(), caption: vid.GetCaption(),
			}
		case mediaKindAudio:
			aud := msg.GetAudioMessage()
			if aud == nil {
				t.Fatalf("se esperaba AudioMessage, se obtuvo %v", msg)
			}
			return &messageFields{
				url: aud.GetURL(), directPath: aud.GetDirectPath(), mediaKey: aud.GetMediaKey(),
				fileEncSHA256: aud.GetFileEncSHA256(), fileSHA256: aud.GetFileSHA256(),
				fileLength: aud.GetFileLength(), mimeType: aud.GetMimetype(), ptt: aud.GetPTT(),
			}
		default:
			doc := msg.GetDocumentMessage()
			if doc == nil {
				t.Fatalf("se esperaba DocumentMessage, se obtuvo %v", msg)
			}
			return &messageFields{
				url: doc.GetURL(), directPath: doc.GetDirectPath(), mediaKey: doc.GetMediaKey(),
				fileEncSHA256: doc.GetFileEncSHA256(), fileSHA256: doc.GetFileSHA256(),
				fileLength: doc.GetFileLength(), mimeType: doc.GetMimetype(), caption: doc.GetCaption(),
				fileName: doc.GetFileName(), title: doc.GetTitle(),
			}
		}
	}

	t.Run("imagen con caption", func(t *testing.T) {
		fields := fieldsFor(t, mediaKindImage, "image/png", "foto.png", "mirá esto")
		assertUploadFields(t, fields)
		if fields.mimeType != "image/png" || fields.caption != "mirá esto" {
			t.Fatalf("Mimetype/Caption = %q/%q", fields.mimeType, fields.caption)
		}
	})

	t.Run("video con caption", func(t *testing.T) {
		fields := fieldsFor(t, mediaKindVideo, "video/mp4", "clip.mp4", "mirá esto")
		assertUploadFields(t, fields)
		if fields.mimeType != "video/mp4" || fields.caption != "mirá esto" {
			t.Fatalf("Mimetype/Caption = %q/%q", fields.mimeType, fields.caption)
		}
	})

	t.Run("nota de voz es PTT", func(t *testing.T) {
		fields := fieldsFor(t, mediaKindAudio, "audio/ogg; codecs=opus", "nota.ogg", "")
		assertUploadFields(t, fields)
		if fields.mimeType != "audio/ogg; codecs=opus" || !fields.ptt {
			t.Fatalf("Mimetype/PTT = %q/%v, se esperaba la nota de voz como PTT", fields.mimeType, fields.ptt)
		}
	})

	t.Run("audio común no es PTT", func(t *testing.T) {
		fields := fieldsFor(t, mediaKindAudio, "audio/mpeg", "tema.mp3", "")
		assertUploadFields(t, fields)
		if fields.ptt {
			t.Fatalf("un audio común no debe marcarse como PTT")
		}
	})

	t.Run("documento lleva nombre y título", func(t *testing.T) {
		fields := fieldsFor(t, mediaKindDocument, "application/pdf", "informe.pdf", "adjunto")
		assertUploadFields(t, fields)
		if fields.fileName != "informe.pdf" || fields.title != "informe.pdf" {
			t.Fatalf("FileName/Title = %q/%q", fields.fileName, fields.title)
		}
		if fields.caption != "adjunto" {
			t.Fatalf("Caption = %q, se esperaba adjunto", fields.caption)
		}
	})

	t.Run("sin caption el campo queda vacío", func(t *testing.T) {
		fields := fieldsFor(t, mediaKindDocument, "application/octet-stream", "binario", "")
		if fields.caption != "" {
			t.Fatalf("Caption = %q, se esperaba vacío", fields.caption)
		}
	})
}

type messageFields struct {
	url           string
	directPath    string
	mediaKey      []byte
	fileEncSHA256 []byte
	fileSHA256    []byte
	fileLength    uint64
	mimeType      string
	caption       string
	fileName      string
	title         string
	ptt           bool
}

func TestValidateOutgoingRequest(t *testing.T) {
	tests := []struct {
		name             string
		body             string
		attachmentCount  int
		wantErrSubstring string
	}{
		{name: "solo texto", body: "hola", attachmentCount: 0},
		{name: "solo adjuntos", body: "", attachmentCount: 1},
		{name: "texto y adjuntos", body: "hola", attachmentCount: 2},
		{name: "vacío", body: "   ", attachmentCount: 0, wantErrSubstring: "adjunto"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateOutgoingRequest(tt.body, tt.attachmentCount)
			if tt.wantErrSubstring == "" {
				if err != nil {
					t.Fatalf("error inesperado: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("se esperaba error accionable")
			}
			if !strings.Contains(strings.ToLower(err.Error()), strings.ToLower(tt.wantErrSubstring)) {
				t.Fatalf("error %q no menciona %q", err, tt.wantErrSubstring)
			}
		})
	}
}

func TestResolveRecipientJID(t *testing.T) {
	tests := []struct {
		name   string
		input  string
		want   types.JID
		wantOK bool
	}{
		{
			name:   "jid completo",
			input:  "5491123456789@s.whatsapp.net",
			want:   types.NewJID("5491123456789", types.DefaultUserServer),
			wantOK: true,
		},
		{
			name:   "número con formato y servidor vacío",
			input:  "+54 9 11 2345-6789@",
			want:   types.NewJID("5491123456789", types.DefaultUserServer),
			wantOK: true,
		},
		{
			name:   "sin dígitos",
			input:  "abc@",
			wantOK: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := resolveRecipientJID(tt.input)
			if ok != tt.wantOK {
				t.Fatalf("ok = %v, se esperaba %v", ok, tt.wantOK)
			}
			if !ok {
				return
			}
			if got.User != tt.want.User || got.Server != tt.want.Server {
				t.Fatalf("jid = %s, se esperaba %s", got.String(), tt.want.String())
			}
		})
	}
}

func TestAttachmentFileName(t *testing.T) {
	tests := []struct {
		name string
		att  *conduitv1.OutgoingAttachment
		want string
	}{
		{name: "nombre explícito", att: &conduitv1.OutgoingAttachment{FilePath: "/tmp/x", FileName: "informe.pdf"}, want: "informe.pdf"},
		{name: "deriva del path", att: &conduitv1.OutgoingAttachment{FilePath: "/tmp/fotos/playa.jpg"}, want: "playa.jpg"},
		{name: "nombre vacío con espacios", att: &conduitv1.OutgoingAttachment{FilePath: "/tmp/a.txt", FileName: "  "}, want: "a.txt"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := attachmentFileName(tt.att); got != tt.want {
				t.Fatalf("attachmentFileName() = %q, se esperaba %q", got, tt.want)
			}
		})
	}
}

func TestBuildTextMessageWithReply(t *testing.T) {
	// Sin cita
	msgNormal := buildTextMessageWithReply("hola", "")
	if msgNormal.GetConversation() != "hola" {
		t.Fatalf("se esperaba conversation 'hola', obtenido %v", msgNormal)
	}

	// Con cita
	msgReply := buildTextMessageWithReply("hola citando", "stanza-999")
	if msgReply.GetExtendedTextMessage() == nil {
		t.Fatalf("se esperaba ExtendedTextMessage para mensaje con cita")
	}
	if msgReply.GetExtendedTextMessage().GetText() != "hola citando" {
		t.Errorf("texto esperado 'hola citando', obtenido %q", msgReply.GetExtendedTextMessage().GetText())
	}
	if ctxInfo := msgReply.GetExtendedTextMessage().GetContextInfo(); ctxInfo == nil || ctxInfo.GetStanzaID() != "stanza-999" {
		t.Errorf("StanzaID esperado 'stanza-999', obtenido %v", ctxInfo)
	}
}

func TestExtractQuotedStanzaID(t *testing.T) {
	extMsg := &waProto.Message{
		ExtendedTextMessage: &waProto.ExtendedTextMessage{
			Text: proto.String("texto"),
			ContextInfo: &waProto.ContextInfo{
				StanzaID: proto.String("stanza-abc"),
			},
		},
	}
	if got := extractQuotedStanzaID(extMsg); got != "stanza-abc" {
		t.Errorf("extractQuotedStanzaID(extMsg) = %q, se esperaba 'stanza-abc'", got)
	}

	imgMsg := &waProto.Message{
		ImageMessage: &waProto.ImageMessage{
			Caption: proto.String("foto"),
			ContextInfo: &waProto.ContextInfo{
				StanzaID: proto.String("stanza-img"),
			},
		},
	}
	if got := extractQuotedStanzaID(imgMsg); got != "stanza-img" {
		t.Errorf("extractQuotedStanzaID(imgMsg) = %q, se esperaba 'stanza-img'", got)
	}

	plainMsg := &waProto.Message{
		Conversation: proto.String("simple"),
	}
	if got := extractQuotedStanzaID(plainMsg); got != "" {
		t.Errorf("extractQuotedStanzaID(plainMsg) = %q, se esperaba ''", got)
	}
}

func TestParseWhatsAppPollCreation(t *testing.T) {
	pollMsg := &waProto.PollCreationMessage{
		Name: proto.String("¿Día de la reunión?"),
		Options: []*waProto.PollCreationMessage_Option{
			{OptionName: proto.String("Lunes")},
			{OptionName: proto.String("Miércoles")},
			{OptionName: proto.String("Viernes")},
		},
		SelectableOptionsCount: proto.Uint32(1),
	}

	protoPoll := parseWhatsAppPollCreation(pollMsg)
	if protoPoll == nil {
		t.Fatal("se esperaba ProtoPoll no nulo")
	}
	if protoPoll.Question != "¿Día de la reunión?" {
		t.Errorf("pregunta esperada '¿Día de la reunión?', obtenida %q", protoPoll.Question)
	}
	if len(protoPoll.Options) != 3 {
		t.Fatalf("se esperaban 3 opciones, obtenidas %d", len(protoPoll.Options))
	}
	if protoPoll.Options[0].Text != "Lunes" || protoPoll.Options[0].Id != "0" {
		t.Errorf("opción 0 inesperada: %+v", protoPoll.Options[0])
	}
	if protoPoll.SelectableCount != 1 {
		t.Errorf("SelectableCount esperado 1, obtenido %d", protoPoll.SelectableCount)
	}
}

func TestExtractWhatsAppPollCreationVariants(t *testing.T) {
	basePoll := &waProto.PollCreationMessage{
		Name: proto.String("¿Qué comemos hoy?"),
		Options: []*waProto.PollCreationMessage_Option{
			{OptionName: proto.String("Pizza")},
			{OptionName: proto.String("Empanadas")},
		},
		SelectableOptionsCount: proto.Uint32(1),
	}

	// 1. Mensaje directo con PollCreationMessageV2
	msgV2 := &waProto.Message{
		PollCreationMessageV2: basePoll,
	}
	extractedV2 := extractWhatsAppPollCreation(msgV2)
	if extractedV2 == nil || extractedV2.GetName() != "¿Qué comemos hoy?" {
		t.Fatalf("falló extracción de PollCreationMessageV2: %+v", extractedV2)
	}

	// 2. Mensaje directo con PollCreationMessageV3
	msgV3 := &waProto.Message{
		PollCreationMessageV3: basePoll,
	}
	extractedV3 := extractWhatsAppPollCreation(msgV3)
	if extractedV3 == nil || extractedV3.GetName() != "¿Qué comemos hoy?" {
		t.Fatalf("falló extracción de PollCreationMessageV3: %+v", extractedV3)
	}

	// 3. Encapsulado en EphemeralMessage
	ephMsg := &waProto.Message{
		EphemeralMessage: &waProto.FutureProofMessage{
			Message: &waProto.Message{
				PollCreationMessageV3: basePoll,
			},
		},
	}
	extractedEph := extractWhatsAppPollCreation(ephMsg)
	if extractedEph == nil || extractedEph.GetName() != "¿Qué comemos hoy?" {
		t.Fatalf("falló extracción de encuesta encapsulada en EphemeralMessage: %+v", extractedEph)
	}

	// 4. Encapsulado en PollCreationMessageV4 (FutureProofMessage)
	v4Msg := &waProto.Message{
		PollCreationMessageV4: &waProto.FutureProofMessage{
			Message: &waProto.Message{
				PollCreationMessage: basePoll,
			},
		},
	}
	extractedV4 := extractWhatsAppPollCreation(v4Msg)
	if extractedV4 == nil || extractedV4.GetName() != "¿Qué comemos hoy?" {
		t.Fatalf("falló extracción de encuesta encapsulada en PollCreationMessageV4: %+v", extractedV4)
	}

	// 5. Mensaje sin encuesta devuelve nil
	plainMsg := &waProto.Message{
		Conversation: proto.String("hola"),
	}
	if extractWhatsAppPollCreation(plainMsg) != nil {
		t.Errorf("mensaje de texto plano no debería extraer encuesta")
	}
}

func TestProcessHistoryMessage_PollAndPin(t *testing.T) {
	plugin := NewWhatsAppPlugin()
	chatJID, _ := types.ParseJID("5491112345678@s.whatsapp.net")

	// 1. Mensaje histórico con encuesta V2
	pollMsg := &waProto.PollCreationMessage{
		Name: proto.String("¿Cuándo nos vemos?"),
		Options: []*waProto.PollCreationMessage_Option{
			{OptionName: proto.String("Hoy")},
			{OptionName: proto.String("Mañana")},
		},
		SelectableOptionsCount: proto.Uint32(1),
	}
	hMsgPoll := &waHistorySync.HistorySyncMsg{
		Message: &waProto.WebMessageInfo{
			Key: &waCommon.MessageKey{
				RemoteJID: proto.String(chatJID.String()),
				FromMe:    proto.Bool(false),
				ID:        proto.String("msg-hist-poll-1"),
			},
			Message: &waProto.Message{
				PollCreationMessageV2: pollMsg,
			},
		},
	}

	eventsPoll := plugin.processHistoryMessage(chatJID, "Contacto Prueba", hMsgPoll)
	if len(eventsPoll) != 1 {
		t.Fatalf("se esperaba 1 evento para la encuesta histórica, obtenidos %d", len(eventsPoll))
	}
	ev := eventsPoll[0]
	if ev.ContentType != "poll" {
		t.Errorf("ContentType esperado 'poll', obtenido %q", ev.ContentType)
	}
	if ev.Poll == nil || ev.Poll.Question != "¿Cuándo nos vemos?" {
		t.Fatalf("Poll inválido o pregunta incorrecta: %+v", ev.Poll)
	}
	if len(ev.Poll.Options) != 2 {
		t.Fatalf("se esperaban 2 opciones en encuesta histórica, obtenidas %d", len(ev.Poll.Options))
	}

	// 2. Mensaje histórico con evento de fijado (PinInChat)
	hMsgPin := &waHistorySync.HistorySyncMsg{
		Message: &waProto.WebMessageInfo{
			Key: &waCommon.MessageKey{
				RemoteJID: proto.String(chatJID.String()),
				FromMe:    proto.Bool(true),
				ID:        proto.String("msg-pin-event"),
			},
			PinInChat: &waProto.PinInChat{
				Key: &waCommon.MessageKey{
					RemoteJID: proto.String(chatJID.String()),
					FromMe:    proto.Bool(false),
					ID:        proto.String("target-msg-pinned-100"),
				},
				Type: waProto.PinInChat_PIN_FOR_ALL.Enum(),
			},
		},
	}

	eventsPin := plugin.processHistoryMessage(chatJID, "Contacto Prueba", hMsgPin)
	if len(eventsPin) < 1 {
		t.Fatalf("se esperaba al menos 1 evento de fijado, obtenidos %d", len(eventsPin))
	}
	var pinEv *conduitv1.PluginEvent
	for _, e := range eventsPin {
		if e.EventType == "message.pinned" {
			pinEv = e
			break
		}
	}
	if pinEv == nil {
		t.Fatalf("no se encontró evento message.pinned en eventos: %+v", eventsPin)
	}
	if pinEv.RemoteMessageId != "target-msg-pinned-100" || !pinEv.IsPinned {
		t.Errorf("evento de fijado inesperado: %+v", pinEv)
	}
}

func TestExtractWhatsAppPollCreationGroupWrappers(t *testing.T) {
	basePoll := &waProto.PollCreationMessage{
		Name: proto.String("¿Cuándo jugamos?"),
		Options: []*waProto.PollCreationMessage_Option{
			{OptionName: proto.String("Sábado")},
			{OptionName: proto.String("Domingo")},
		},
	}

	// Poll sent from the user's own phone arrives wrapped in DeviceSentMessage.
	deviceSent := &waProto.Message{
		DeviceSentMessage: &waProto.DeviceSentMessage{
			Message: &waProto.Message{PollCreationMessageV3: basePoll},
		},
	}
	if got := extractWhatsAppPollCreation(deviceSent); got == nil || got.GetName() != "¿Cuándo jugamos?" {
		t.Fatalf("DeviceSentMessage poll not extracted: %+v", got)
	}

	// Poll that mentions the group arrives wrapped in GroupMentionedMessage.
	groupMentioned := &waProto.Message{
		GroupMentionedMessage: &waProto.FutureProofMessage{
			Message: &waProto.Message{PollCreationMessageV3: basePoll},
		},
	}
	if got := extractWhatsAppPollCreation(groupMentioned); got == nil || got.GetName() != "¿Cuándo jugamos?" {
		t.Fatalf("GroupMentionedMessage poll not extracted: %+v", got)
	}
}

func TestProcessHistoryMessage_PinnedStub(t *testing.T) {
	plugin := NewWhatsAppPlugin()
	groupJID, _ := types.ParseJID("120363425076349982@g.us")

	// Group pin notices come as a stub with no message body and the target ID in the parameters.
	stub := waProto.WebMessageInfo_PINNED_MESSAGE_IN_CHAT
	hMsg := &waHistorySync.HistorySyncMsg{
		Message: &waProto.WebMessageInfo{
			Key: &waCommon.MessageKey{
				RemoteJID: proto.String(groupJID.String()),
				ID:        proto.String("stub-pin-1"),
			},
			MessageStubType:       &stub,
			MessageStubParameters: []string{"target-poll-42"},
		},
	}

	events := plugin.processHistoryMessage(groupJID, "LPFC info", hMsg)
	if len(events) != 1 {
		t.Fatalf("expected 1 event for pin stub, got %d: %+v", len(events), events)
	}
	ev := events[0]
	if ev.EventType != "message.pinned" || ev.RemoteMessageId != "target-poll-42" || !ev.IsPinned {
		t.Errorf("unexpected pin stub event: %+v", ev)
	}
}

func TestBuildWhatsAppPinMessageKey(t *testing.T) {
	// 1. Mensaje entrante de otro usuario en grupo
	groupJID, _ := types.ParseJID("12036302@g.us")
	reqIncomingGroup := &conduitv1.PluginPinMessageRequest{
		RemoteChatId:    groupJID.String(),
		RemoteMessageId: "msg-incoming-999",
		Pinned:          true,
		FromMe:          false,
		ParticipantId:   "5491112345678@s.whatsapp.net",
	}

	keyGroup := buildPinMessageKey(groupJID, reqIncomingGroup)
	if keyGroup.GetFromMe() != false {
		t.Errorf("FromMe esperado false, obtenido %v", keyGroup.GetFromMe())
	}
	if keyGroup.GetParticipant() != "5491112345678@s.whatsapp.net" {
		t.Errorf("Participant esperado '5491112345678@s.whatsapp.net', obtenido %q", keyGroup.GetParticipant())
	}
	if keyGroup.GetID() != "msg-incoming-999" {
		t.Errorf("ID esperado 'msg-incoming-999', obtenido %q", keyGroup.GetID())
	}

	// 2. Mensaje propio saliente
	dmJID, _ := types.ParseJID("5491198765432@s.whatsapp.net")
	reqOutgoingDM := &conduitv1.PluginPinMessageRequest{
		RemoteChatId:    dmJID.String(),
		RemoteMessageId: "msg-own-111",
		Pinned:          true,
		FromMe:          true,
	}

	keyDM := buildPinMessageKey(dmJID, reqOutgoingDM)
	if keyDM.GetFromMe() != true {
		t.Errorf("FromMe esperado true, obtenido %v", keyDM.GetFromMe())
	}
	if keyDM.GetParticipant() != "" {
		t.Errorf("Participant en DM propio debería estar vacío, obtenido %q", keyDM.GetParticipant())
	}
}

func TestParseWhatsAppButtons(t *testing.T) {
	msg := &waProto.Message{
		ButtonsMessage: &waProto.ButtonsMessage{
			ContentText: proto.String("Elige una opción"),
			Buttons: []*waProto.ButtonsMessage_Button{
				{
					ButtonID: proto.String("btn_si"),
					ButtonText: &waProto.ButtonsMessage_Button_ButtonText{
						DisplayText: proto.String("Sí"),
					},
				},
				{
					ButtonID: proto.String("btn_no"),
					ButtonText: &waProto.ButtonsMessage_Button_ButtonText{
						DisplayText: proto.String("No"),
					},
				},
			},
		},
	}

	rows := parseWhatsAppButtons(msg)
	if len(rows) != 1 {
		t.Fatalf("se esperaba 1 fila de botones, obtenidas %d", len(rows))
	}
	if len(rows[0].Buttons) != 2 {
		t.Fatalf("se esperaban 2 botones, obtenidos %d", len(rows[0].Buttons))
	}
	if rows[0].Buttons[0].Id != "btn_si" || rows[0].Buttons[0].Text != "Sí" {
		t.Errorf("botón 0 inesperado: %+v", rows[0].Buttons[0])
	}
}

func TestParseWhatsAppLocation(t *testing.T) {
	locMsg := &waProto.LocationMessage{
		DegreesLatitude:  proto.Float64(-34.6037),
		DegreesLongitude: proto.Float64(-58.3816),
		Name:             proto.String("Obelisco"),
		Address:          proto.String("Av. 9 de Julio"),
	}

	loc := parseWhatsAppLocation(locMsg)
	if loc == nil {
		t.Fatal("se esperaba ProtoLocation no nulo")
	}
	if loc.Latitude != -34.6037 || loc.Longitude != -58.3816 || loc.Title != "Obelisco" || loc.Address != "Av. 9 de Julio" {
		t.Errorf("ProtoLocation inesperado: %+v", loc)
	}
}

func TestParseWhatsAppContact(t *testing.T) {
	contMsg := &waProto.ContactMessage{
		DisplayName: proto.String("Ana Ejemplo"),
		Vcard:       proto.String("BEGIN:VCARD...TEL:+549110000..."),
	}

	cont := parseWhatsAppContact(contMsg)
	if cont == nil {
		t.Fatal("se esperaba ProtoSharedContact no nulo")
	}
	if cont.DisplayName != "Ana Ejemplo" || cont.Vcard != "BEGIN:VCARD...TEL:+549110000..." {
		t.Errorf("ProtoSharedContact inesperado: %+v", cont)
	}
}
