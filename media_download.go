package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	conduitv1 "github.com/cestila/ConduitOmniChat/sdk/conduitv1"
	"go.mau.fi/whatsmeow"
)

// mediaSource es lo mínimo que necesitamos de un mensaje multimedia de whatsmeow
// (ImageMessage, VideoMessage, AudioMessage, DocumentMessage, StickerMessage).
type mediaSource interface {
	GetDirectPath() string
	GetMediaKey() []byte
	GetFileEncSHA256() []byte
	GetFileSHA256() []byte
	GetFileLength() uint64
}

// mediaRecipe es la receta opaca (JSON) que viaja en ProtoAttachment.RemoteRef
// y permite descargar el archivo más tarde sin volver a consultar el mensaje.
type mediaRecipe struct {
	Type          string `json:"type"`
	DirectPath    string `json:"direct_path"`
	MediaKey      string `json:"media_key"`
	FileEncSHA256 string `json:"file_enc_sha256"`
	FileSHA256    string `json:"file_sha256"`
	FileLength    uint64 `json:"file_length"`
	Mimetype      string `json:"mimetype"`
}

// mediaTypeForKind traduce el tipo de la receta al MediaType de whatsmeow.
// Los stickers se descargan con las claves de imagen.
func mediaTypeForKind(kind string) (whatsmeow.MediaType, bool) {
	switch kind {
	case "image", "sticker":
		return whatsmeow.MediaImage, true
	case "video":
		return whatsmeow.MediaVideo, true
	case "audio":
		return whatsmeow.MediaAudio, true
	case "document":
		return whatsmeow.MediaDocument, true
	}
	return "", false
}

// sanitizeMediaName deja solo un nombre base seguro; devuelve "" si no hay uno válido.
func sanitizeMediaName(name string) string {
	name = strings.ReplaceAll(name, "\x00", "")
	name = strings.ReplaceAll(name, `\`, "_")
	base := filepath.Base(strings.TrimSpace(name))
	if base == "" || base == "." || base == ".." || base == string(filepath.Separator) {
		return ""
	}
	return base
}

// safeMediaPath devuelve una ruta dentro de dir: id + "_" + nombre base saneado.
// Si el nombre no es utilizable se usa "media" + fallbackExt.
func safeMediaPath(dir, id, name, fallbackExt string) string {
	base := sanitizeMediaName(name)
	if base == "" {
		base = "media" + fallbackExt
	}
	if prefix := sanitizeMediaName(id); prefix != "" {
		base = prefix + "_" + base
	}
	return filepath.Join(dir, base)
}

// attachmentFromMedia arma el adjunto "remote_only" de un mensaje entrante sin tocar la red.
func attachmentFromMedia(msgID, kind, fileName, mime string, src mediaSource) *conduitv1.ProtoAttachment {
	recipe := mediaRecipe{
		Type:          kind,
		DirectPath:    src.GetDirectPath(),
		MediaKey:      base64.StdEncoding.EncodeToString(src.GetMediaKey()),
		FileEncSHA256: base64.StdEncoding.EncodeToString(src.GetFileEncSHA256()),
		FileSHA256:    base64.StdEncoding.EncodeToString(src.GetFileSHA256()),
		FileLength:    src.GetFileLength(),
		Mimetype:      mime,
	}
	ref, _ := json.Marshal(recipe)
	return &conduitv1.ProtoAttachment{
		Id:             fmt.Sprintf("att-%s", msgID),
		FileName:       sanitizeMediaName(fileName),
		MimeType:       mime,
		FileSize:       int64(src.GetFileLength()),
		DownloadStatus: "remote_only",
		RemoteRef:      string(ref),
	}
}

// writeMediaAtomic escribe en un temporal del mismo directorio y renombra.
func writeMediaAtomic(target string, data []byte) error {
	var rnd [6]byte
	_, _ = rand.Read(rnd[:])
	tmp := target + ".tmp-" + hex.EncodeToString(rnd[:])
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, target); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

func downloadFailure(format string, args ...any) *conduitv1.PluginDownloadMediaResponse {
	return &conduitv1.PluginDownloadMediaResponse{Success: false, ErrorMessage: fmt.Sprintf(format, args...)}
}

// DownloadMedia baja un adjunto a demanda a partir de la receta emitida al recibirlo.
func (p *WhatsAppPlugin) DownloadMedia(ctx context.Context, req *conduitv1.PluginDownloadMediaRequest) (*conduitv1.PluginDownloadMediaResponse, error) {
	var recipe mediaRecipe
	if err := json.Unmarshal([]byte(req.GetRemoteRef()), &recipe); err != nil {
		return downloadFailure("remote_ref inválida: %v", err), nil
	}
	mediaType, ok := mediaTypeForKind(recipe.Type)
	if !ok {
		return downloadFailure("tipo de medio desconocido: %q", recipe.Type), nil
	}
	if recipe.DirectPath == "" {
		return downloadFailure("remote_ref sin direct_path"), nil
	}
	mediaKey, err1 := base64.StdEncoding.DecodeString(recipe.MediaKey)
	encHash, err2 := base64.StdEncoding.DecodeString(recipe.FileEncSHA256)
	fileHash, err3 := base64.StdEncoding.DecodeString(recipe.FileSHA256)
	if err1 != nil || err2 != nil || err3 != nil {
		return downloadFailure("remote_ref con claves o hashes mal codificados"), nil
	}

	p.mu.RLock()
	client := p.client
	p.mu.RUnlock()
	if client == nil || !client.IsConnected() {
		return downloadFailure("WhatsApp no está conectado"), nil
	}

	// mmsType vacío: whatsmeow lo deriva del MediaType.
	data, err := client.DownloadMediaWithPath(ctx, recipe.DirectPath, encHash, fileHash, mediaKey, mediaType, "", false)
	if err != nil {
		return downloadFailure("falló la descarga: %v", err), nil
	}
	if len(data) == 0 {
		return downloadFailure("la descarga devolvió un archivo vacío"), nil
	}

	if err := os.MkdirAll(p.mediaDir, 0755); err != nil {
		return downloadFailure("no se pudo crear el directorio de medios: %v", err), nil
	}
	target := safeMediaPath(p.mediaDir, req.GetAttachmentId(), req.GetFileName(), extForKind(recipe.Type))
	if err := writeMediaAtomic(target, data); err != nil {
		return downloadFailure("no se pudo guardar el archivo: %v", err), nil
	}
	return &conduitv1.PluginDownloadMediaResponse{Success: true, LocalPath: target, FileSize: int64(len(data))}, nil
}

// extForKind da la extensión de respaldo cuando el remitente no envía un nombre usable.
func extForKind(kind string) string {
	switch kind {
	case "image":
		return ".jpg"
	case "video":
		return ".mp4"
	case "audio":
		return ".ogg"
	case "sticker":
		return ".webp"
	}
	return ".bin"
}
