package main

import (
	"fmt"
	"mime"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	conduitv1 "github.com/cestila/ConduitOmniChat/sdk/conduitv1"
	"go.mau.fi/whatsmeow"
	waProto "go.mau.fi/whatsmeow/binary/proto"
	"go.mau.fi/whatsmeow/types"
	"google.golang.org/protobuf/proto"
)

// mediaKind clasifica un adjunto en el tipo de mensaje multimedia de WhatsApp.
type mediaKind int

const (
	mediaKindImage mediaKind = iota
	mediaKindVideo
	mediaKindAudio
	mediaKindDocument
)

// mediaKindForMIME clasifica un MIME en imagen, video, audio o documento.
func mediaKindForMIME(mimeType string) mediaKind {
	base := strings.ToLower(strings.TrimSpace(mimeType))
	if i := strings.Index(base, ";"); i >= 0 {
		base = strings.TrimSpace(base[:i])
	}
	switch {
	case strings.HasPrefix(base, "image/"):
		return mediaKindImage
	case strings.HasPrefix(base, "video/"):
		return mediaKindVideo
	case strings.HasPrefix(base, "audio/"):
		return mediaKindAudio
	default:
		return mediaKindDocument
	}
}

// isVoiceNoteMIME detecta notas de voz: WhatsApp exige contenedor Ogg con codec Opus para PTT.
func isVoiceNoteMIME(mimeType string) bool {
	lower := strings.ToLower(strings.TrimSpace(mimeType))
	if lower == "" {
		return false
	}
	base := lower
	if i := strings.Index(base, ";"); i >= 0 {
		base = strings.TrimSpace(base[:i])
	}
	return base == "audio/ogg" || base == "audio/opus"
}

// whatsmeowMediaTypeForMIME elige el tipo de subida que espera whatsmeow.
func whatsmeowMediaTypeForMIME(mimeType string) whatsmeow.MediaType {
	switch mediaKindForMIME(mimeType) {
	case mediaKindImage:
		return whatsmeow.MediaImage
	case mediaKindVideo:
		return whatsmeow.MediaVideo
	case mediaKindAudio:
		return whatsmeow.MediaAudio
	default:
		return whatsmeow.MediaDocument
	}
}

// buildTextMessage arma el mensaje de solo texto, sin cambios respecto del envío histórico.
func buildTextMessage(body string) *waProto.Message {
	return &waProto.Message{
		Conversation: proto.String(body),
	}
}

// buildTextMessageWithReply arma un mensaje de texto con cita (ExtendedTextMessage) si hay stanzaID.
func buildTextMessageWithReply(body, replyToRemoteID string) *waProto.Message {
	if replyToRemoteID == "" {
		return buildTextMessage(body)
	}
	return &waProto.Message{
		ExtendedTextMessage: &waProto.ExtendedTextMessage{
			Text: proto.String(body),
			ContextInfo: &waProto.ContextInfo{
				StanzaID: proto.String(replyToRemoteID),
			},
		},
	}
}

// attachQuotedStanzaID vincula el stanzaID citado a un mensaje multimedia existente.
func attachQuotedStanzaID(msg *waProto.Message, replyToRemoteID string) {
	if msg == nil || replyToRemoteID == "" {
		return
	}
	ctxInfo := &waProto.ContextInfo{
		StanzaID: proto.String(replyToRemoteID),
	}
	if img := msg.GetImageMessage(); img != nil {
		img.ContextInfo = ctxInfo
	} else if vid := msg.GetVideoMessage(); vid != nil {
		vid.ContextInfo = ctxInfo
	} else if aud := msg.GetAudioMessage(); aud != nil {
		aud.ContextInfo = ctxInfo
	} else if doc := msg.GetDocumentMessage(); doc != nil {
		doc.ContextInfo = ctxInfo
	}
}

// extractQuotedStanzaID inspecciona cualquier mensaje entrante de WhatsApp para extraer el stanzaID citado.
func extractQuotedStanzaID(msg *waProto.Message) string {
	if msg == nil {
		return ""
	}
	if ext := msg.GetExtendedTextMessage(); ext != nil && ext.GetContextInfo() != nil {
		return ext.GetContextInfo().GetStanzaID()
	}
	if img := msg.GetImageMessage(); img != nil && img.GetContextInfo() != nil {
		return img.GetContextInfo().GetStanzaID()
	}
	if vid := msg.GetVideoMessage(); vid != nil && vid.GetContextInfo() != nil {
		return vid.GetContextInfo().GetStanzaID()
	}
	if aud := msg.GetAudioMessage(); aud != nil && aud.GetContextInfo() != nil {
		return aud.GetContextInfo().GetStanzaID()
	}
	if doc := msg.GetDocumentMessage(); doc != nil && doc.GetContextInfo() != nil {
		return doc.GetContextInfo().GetStanzaID()
	}
	if stk := msg.GetStickerMessage(); stk != nil && stk.GetContextInfo() != nil {
		return stk.GetContextInfo().GetStanzaID()
	}
	return ""
}

// unwrapWhatsAppMessage desenvuelve mensajes de WhatsApp encapsulados en efímeros, view-once o documentos con caption.
func unwrapWhatsAppMessage(msg *waProto.Message) *waProto.Message {
	if msg == nil {
		return nil
	}
	if dev := msg.GetDeviceSentMessage(); dev != nil && dev.GetMessage() != nil {
		return unwrapWhatsAppMessage(dev.GetMessage())
	}
	if eph := msg.GetEphemeralMessage(); eph != nil && eph.GetMessage() != nil {
		return unwrapWhatsAppMessage(eph.GetMessage())
	}
	if vo := msg.GetViewOnceMessage(); vo != nil && vo.GetMessage() != nil {
		return unwrapWhatsAppMessage(vo.GetMessage())
	}
	if vo2 := msg.GetViewOnceMessageV2(); vo2 != nil && vo2.GetMessage() != nil {
		return unwrapWhatsAppMessage(vo2.GetMessage())
	}
	if vo2Ext := msg.GetViewOnceMessageV2Extension(); vo2Ext != nil && vo2Ext.GetMessage() != nil {
		return unwrapWhatsAppMessage(vo2Ext.GetMessage())
	}
	if gm := msg.GetGroupMentionedMessage(); gm != nil && gm.GetMessage() != nil {
		return unwrapWhatsAppMessage(gm.GetMessage())
	}
	if bot := msg.GetBotInvokeMessage(); bot != nil && bot.GetMessage() != nil {
		return unwrapWhatsAppMessage(bot.GetMessage())
	}
	if doc := msg.GetDocumentWithCaptionMessage(); doc != nil && doc.GetMessage() != nil {
		return unwrapWhatsAppMessage(doc.GetMessage())
	}
	return msg
}

// extractWhatsAppPollCreation extrae una creación de encuesta de WhatsApp de cualquier versión del protocolo (V1, V2, V3, V4/FutureProof, V5, V6).
func extractWhatsAppPollCreation(msg *waProto.Message) *waProto.PollCreationMessage {
	if msg == nil {
		return nil
	}
	msg = unwrapWhatsAppMessage(msg)
	if p := msg.GetPollCreationMessage(); p != nil {
		return p
	}
	if p := msg.GetPollCreationMessageV2(); p != nil {
		return p
	}
	if p := msg.GetPollCreationMessageV3(); p != nil {
		return p
	}
	if p := msg.GetPollCreationMessageV5(); p != nil {
		return p
	}
	if p := msg.GetPollCreationMessageV6(); p != nil {
		return p
	}
	if p4 := msg.GetPollCreationMessageV4(); p4 != nil && p4.GetMessage() != nil {
		return extractWhatsAppPollCreation(p4.GetMessage())
	}
	if pImg := msg.GetPollCreationOptionImageMessage(); pImg != nil && pImg.GetMessage() != nil {
		return extractWhatsAppPollCreation(pImg.GetMessage())
	}
	return nil
}

// extractWhatsAppPinInChatMessage extrae un mensaje de fijado de chat desempacando contenedores si aplica.
func extractWhatsAppPinInChatMessage(msg *waProto.Message) *waProto.PinInChatMessage {
	if msg == nil {
		return nil
	}
	msg = unwrapWhatsAppMessage(msg)
	return msg.GetPinInChatMessage()
}

// parseWhatsAppPollCreation convierte una creación de encuesta de WhatsApp al modelo unificado ProtoPoll.
func parseWhatsAppPollCreation(poll *waProto.PollCreationMessage) *conduitv1.ProtoPoll {
	if poll == nil {
		return nil
	}
	var options []*conduitv1.ProtoPollOption
	for idx, opt := range poll.GetOptions() {
		options = append(options, &conduitv1.ProtoPollOption{
			Id:        strconv.Itoa(idx),
			Text:      opt.GetOptionName(),
			VoteCount: 0,
		})
	}
	selectable := int32(poll.GetSelectableOptionsCount())
	if selectable == 0 {
		selectable = 1
	}
	return &conduitv1.ProtoPoll{
		Question:        poll.GetName(),
		Options:         options,
		SelectableCount: selectable,
	}
}

// parseWhatsAppButtons extrae filas de botones desde mensajes de plantilla o interactivos de WhatsApp.
func parseWhatsAppButtons(msg *waProto.Message) []*conduitv1.ProtoButtonRow {
	if msg == nil {
		return nil
	}
	var buttons []*conduitv1.ProtoButton

	if bMsg := msg.GetButtonsMessage(); bMsg != nil {
		for _, b := range bMsg.GetButtons() {
			text := ""
			if b.GetButtonText() != nil {
				text = b.GetButtonText().GetDisplayText()
			}
			buttons = append(buttons, &conduitv1.ProtoButton{
				Id:   b.GetButtonID(),
				Text: text,
				Type: "callback",
			})
		}
	} else if tMsg := msg.GetTemplateMessage(); tMsg != nil && tMsg.GetHydratedTemplate() != nil {
		for _, hb := range tMsg.GetHydratedTemplate().GetHydratedButtons() {
			if qr := hb.GetQuickReplyButton(); qr != nil {
				buttons = append(buttons, &conduitv1.ProtoButton{
					Id:   qr.GetID(),
					Text: qr.GetDisplayText(),
					Type: "quick_reply",
				})
			} else if url := hb.GetUrlButton(); url != nil {
				buttons = append(buttons, &conduitv1.ProtoButton{
					Id:   url.GetURL(),
					Text: url.GetDisplayText(),
					Type: "url",
					Url:  url.GetURL(),
				})
			}
		}
	}

	if len(buttons) == 0 {
		return nil
	}
	var rows []*conduitv1.ProtoButtonRow
	var curRow []*conduitv1.ProtoButton
	for _, btn := range buttons {
		curRow = append(curRow, btn)
		if len(curRow) >= 3 {
			rows = append(rows, &conduitv1.ProtoButtonRow{Buttons: curRow})
			curRow = nil
		}
	}
	if len(curRow) > 0 {
		rows = append(rows, &conduitv1.ProtoButtonRow{Buttons: curRow})
	}
	return rows
}

// parseWhatsAppLocation convierte un LocationMessage de WhatsApp a ProtoLocation.
func parseWhatsAppLocation(loc *waProto.LocationMessage) *conduitv1.ProtoLocation {
	if loc == nil {
		return nil
	}
	return &conduitv1.ProtoLocation{
		Latitude:  loc.GetDegreesLatitude(),
		Longitude: loc.GetDegreesLongitude(),
		Title:     loc.GetName(),
		Address:   loc.GetAddress(),
	}
}

// parseWhatsAppContact convierte un ContactMessage de WhatsApp a ProtoSharedContact.
func parseWhatsAppContact(cont *waProto.ContactMessage) *conduitv1.ProtoSharedContact {
	if cont == nil {
		return nil
	}
	return &conduitv1.ProtoSharedContact{
		DisplayName: cont.GetDisplayName(),
		Vcard:       cont.GetVcard(),
	}
}

// buildMediaMessage arma el mensaje de WhatsApp para un adjunto ya subido.
func buildMediaMessage(upload whatsmeow.UploadResponse, mimeType, fileName, caption string) *waProto.Message {
	switch mediaKindForMIME(mimeType) {
	case mediaKindImage:
		return &waProto.Message{
			ImageMessage: &waProto.ImageMessage{
				URL:           proto.String(upload.URL),
				DirectPath:    proto.String(upload.DirectPath),
				MediaKey:      upload.MediaKey,
				FileEncSHA256: upload.FileEncSHA256,
				FileSHA256:    upload.FileSHA256,
				FileLength:    proto.Uint64(upload.FileLength),
				Mimetype:      proto.String(mimeType),
				Caption:       proto.String(caption),
			},
		}
	case mediaKindVideo:
		return &waProto.Message{
			VideoMessage: &waProto.VideoMessage{
				URL:           proto.String(upload.URL),
				DirectPath:    proto.String(upload.DirectPath),
				MediaKey:      upload.MediaKey,
				FileEncSHA256: upload.FileEncSHA256,
				FileSHA256:    upload.FileSHA256,
				FileLength:    proto.Uint64(upload.FileLength),
				Mimetype:      proto.String(mimeType),
				Caption:       proto.String(caption),
			},
		}
	case mediaKindAudio:
		return &waProto.Message{
			AudioMessage: &waProto.AudioMessage{
				URL:           proto.String(upload.URL),
				DirectPath:    proto.String(upload.DirectPath),
				MediaKey:      upload.MediaKey,
				FileEncSHA256: upload.FileEncSHA256,
				FileSHA256:    upload.FileSHA256,
				FileLength:    proto.Uint64(upload.FileLength),
				Mimetype:      proto.String(mimeType),
				PTT:           proto.Bool(isVoiceNoteMIME(mimeType)),
			},
		}
	default:
		return &waProto.Message{
			DocumentMessage: &waProto.DocumentMessage{
				URL:           proto.String(upload.URL),
				DirectPath:    proto.String(upload.DirectPath),
				MediaKey:      upload.MediaKey,
				FileEncSHA256: upload.FileEncSHA256,
				FileSHA256:    upload.FileSHA256,
				FileLength:    proto.Uint64(upload.FileLength),
				Mimetype:      proto.String(mimeType),
				FileName:      proto.String(fileName),
				Title:         proto.String(fileName),
				Caption:       proto.String(caption),
			},
		}
	}
}

// validateOutgoingRequest rechaza un envío sin contenido accionable.
func validateOutgoingRequest(body string, attachmentCount int) error {
	if strings.TrimSpace(body) == "" && attachmentCount == 0 {
		return fmt.Errorf("el mensaje necesita texto o al menos un adjunto")
	}
	return nil
}

// resolveRecipientJID normaliza el destinatario igual que el envío de texto histórico.
func resolveRecipientJID(recipient string) (types.JID, bool) {
	jid, err := types.ParseJID(recipient)
	if err != nil || jid.Server == "" {
		// Si es solo número de teléfono o ID numérico sin servidor
		cleanNum := ""
		for _, r := range recipient {
			if r >= '0' && r <= '9' {
				cleanNum += string(r)
			}
		}
		if cleanNum == "" {
			return types.JID{}, false
		}
		jid = types.NewJID(cleanNum, types.DefaultUserServer)
	}
	return jid, true
}

// attachmentFileName devuelve el nombre visible del adjunto.
func attachmentFileName(att *conduitv1.OutgoingAttachment) string {
	if name := strings.TrimSpace(att.GetFileName()); name != "" {
		return name
	}
	return filepath.Base(att.GetFilePath())
}

// attachmentMIMEType usa el MIME provisto por el Core y lo infiere por extensión si falta.
func attachmentMIMEType(att *conduitv1.OutgoingAttachment) string {
	if mimeType := strings.TrimSpace(att.GetMimeType()); mimeType != "" {
		return mimeType
	}
	if inferred := mime.TypeByExtension(strings.ToLower(filepath.Ext(att.GetFilePath()))); inferred != "" {
		return inferred
	}
	return "application/octet-stream"
}

// prepareOutgoingAudioData verifica si el adjunto es audio en contenedor WebM y lo
// convierte a Ogg Opus con ffmpeg si está disponible en el PATH del sistema.
// WhatsApp exige Ogg Opus para poder entregar y reproducir notas de voz (PTT).
func prepareOutgoingAudioData(filePath string, originalData []byte, mimeType, fileName string) ([]byte, string, string) {
	lowerMime := strings.ToLower(strings.TrimSpace(mimeType))
	lowerFile := strings.ToLower(strings.TrimSpace(fileName))

	isAudio := strings.HasPrefix(lowerMime, "audio/") || strings.HasSuffix(lowerFile, ".webm") || strings.HasSuffix(lowerFile, ".ogg")
	if !isAudio {
		return originalData, mimeType, fileName
	}

	// Si ya es ogg u opus, no requiere conversión
	if strings.Contains(lowerMime, "ogg") || strings.Contains(lowerMime, "opus") || strings.HasSuffix(lowerFile, ".ogg") {
		return originalData, mimeType, fileName
	}

	// Si no es WebM u otro formato no-ogg, salimos
	if !strings.Contains(lowerMime, "webm") && !strings.HasSuffix(lowerFile, ".webm") {
		return originalData, mimeType, fileName
	}

	ffmpegPath, err := exec.LookPath("ffmpeg")
	if err != nil {
		return originalData, mimeType, fileName
	}

	tempDir, err := os.MkdirTemp("", "conduit-wa-transcode-*")
	if err != nil {
		return originalData, mimeType, fileName
	}
	defer os.RemoveAll(tempDir)

	inputPath := filePath
	if inputPath == "" || !isNonEmptyFile(inputPath) {
		inputPath = filepath.Join(tempDir, "input.webm")
		if err := os.WriteFile(inputPath, originalData, 0600); err != nil {
			return originalData, mimeType, fileName
		}
	}

	outputPath := filepath.Join(tempDir, "output.ogg")

	// Primero intentamos remux rápido con -c:a copy (si ya viene con Opus en contenedor WebM)
	cmd := exec.Command(ffmpegPath, "-y", "-i", inputPath, "-vn", "-c:a", "copy", outputPath)
	if err := cmd.Run(); err != nil || !isNonEmptyFile(outputPath) {
		// Fallback transcodificando con libopus
		cmd2 := exec.Command(ffmpegPath, "-y", "-i", inputPath, "-vn", "-c:a", "libopus", "-b:a", "64k", outputPath)
		if err2 := cmd2.Run(); err2 != nil || !isNonEmptyFile(outputPath) {
			return originalData, mimeType, fileName
		}
	}

	convertedBytes, err := os.ReadFile(outputPath)
	if err != nil || len(convertedBytes) == 0 {
		return originalData, mimeType, fileName
	}

	baseName := strings.TrimSuffix(fileName, filepath.Ext(fileName))
	if baseName == "" {
		baseName = "voice-note"
	}
	return convertedBytes, "audio/ogg; codecs=opus", baseName + ".ogg"
}

func isNonEmptyFile(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && !fi.IsDir() && fi.Size() > 0
}
