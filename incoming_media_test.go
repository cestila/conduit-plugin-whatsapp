package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	conduitv1 "github.com/cestila/ConduitOmniChat/sdk/conduitv1"
	waProto "go.mau.fi/whatsmeow/binary/proto"
	"google.golang.org/protobuf/proto"
)

func TestAttachmentFromMediaPerType(t *testing.T) {
	key := []byte("0123456789abcdef0123456789abcdef")
	enc := []byte("enc-sha-enc-sha-enc-sha-enc-sha!")
	plain := []byte("plain-sha-plain-sha-plain-sha-!!")

	tests := []struct {
		name string
		kind string
		src  mediaSource
		size int64
	}{
		{"image", "image", &waProto.ImageMessage{DirectPath: proto.String("/v/img"), MediaKey: key, FileEncSHA256: enc, FileSHA256: plain, FileLength: proto.Uint64(111)}, 111},
		{"video", "video", &waProto.VideoMessage{DirectPath: proto.String("/v/vid"), MediaKey: key, FileEncSHA256: enc, FileSHA256: plain, FileLength: proto.Uint64(222)}, 222},
		{"audio", "audio", &waProto.AudioMessage{DirectPath: proto.String("/v/aud"), MediaKey: key, FileEncSHA256: enc, FileSHA256: plain, FileLength: proto.Uint64(333)}, 333},
		{"document", "document", &waProto.DocumentMessage{DirectPath: proto.String("/v/doc"), MediaKey: key, FileEncSHA256: enc, FileSHA256: plain, FileLength: proto.Uint64(444)}, 444},
		{"sticker", "sticker", &waProto.StickerMessage{DirectPath: proto.String("/v/stk"), MediaKey: key, FileEncSHA256: enc, FileSHA256: plain, FileLength: proto.Uint64(555)}, 555},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			att := attachmentFromMedia("MSG1", tt.kind, "../../evil/"+tt.name+".bin", "application/x-test", tt.src)

			if att.Id != "att-MSG1" {
				t.Errorf("Id = %q", att.Id)
			}
			if att.DownloadStatus != "remote_only" {
				t.Errorf("DownloadStatus = %q, se esperaba remote_only", att.DownloadStatus)
			}
			if att.LocalPath != "" {
				t.Errorf("LocalPath = %q, debe estar vacío", att.LocalPath)
			}
			if att.FileName != tt.name+".bin" {
				t.Errorf("FileName = %q, se esperaba %q", att.FileName, tt.name+".bin")
			}
			if att.MimeType != "application/x-test" {
				t.Errorf("MimeType = %q", att.MimeType)
			}
			if att.FileSize != tt.size {
				t.Errorf("FileSize = %d, se esperaba %d", att.FileSize, tt.size)
			}

			var r mediaRecipe
			if err := json.Unmarshal([]byte(att.RemoteRef), &r); err != nil {
				t.Fatalf("RemoteRef no es JSON válido: %v", err)
			}
			if r.Type != tt.kind || r.FileLength != uint64(tt.size) || r.Mimetype != "application/x-test" {
				t.Errorf("receta inesperada: %+v", r)
			}
			if r.DirectPath == "" {
				t.Errorf("direct_path vacío")
			}
			for name, pair := range map[string]struct {
				got  string
				want []byte
			}{
				"media_key":       {r.MediaKey, key},
				"file_enc_sha256": {r.FileEncSHA256, enc},
				"file_sha256":     {r.FileSHA256, plain},
			} {
				dec, err := base64.StdEncoding.DecodeString(pair.got)
				if err != nil || string(dec) != string(pair.want) {
					t.Errorf("%s no hace round-trip (err=%v)", name, err)
				}
			}
		})
	}
}

func TestDownloadMediaInvalidRef(t *testing.T) {
	for _, ref := range []string{"", "no es json", `{"type":"banana","direct_path":"/x"}`, `{"type":"image"}`} {
		dir := t.TempDir()
		p := &WhatsAppPlugin{mediaDir: dir}
		resp, err := p.DownloadMedia(context.Background(), &conduitv1.PluginDownloadMediaRequest{RemoteRef: ref, FileName: "a.jpg", AttachmentId: "att-1"})
		if err != nil {
			t.Fatalf("ref %q: error gRPC inesperado: %v", ref, err)
		}
		if resp.Success || resp.ErrorMessage == "" || resp.LocalPath != "" || resp.FileSize != 0 {
			t.Errorf("ref %q: respuesta inesperada %+v", ref, resp)
		}
		if entries, _ := os.ReadDir(dir); len(entries) != 0 {
			t.Errorf("ref %q: no debe escribir archivos, hay %d", ref, len(entries))
		}
	}
}

func TestDownloadMediaDisconnectedClient(t *testing.T) {
	dir := t.TempDir()
	p := &WhatsAppPlugin{mediaDir: dir} // client nil
	ref := `{"type":"image","direct_path":"/v/x","media_key":"AAAA","file_enc_sha256":"AAAA","file_sha256":"AAAA","file_length":3,"mimetype":"image/jpeg"}`
	resp, err := p.DownloadMedia(context.Background(), &conduitv1.PluginDownloadMediaRequest{RemoteRef: ref, FileName: "a.jpg", AttachmentId: "att-1"})
	if err != nil {
		t.Fatalf("error gRPC inesperado: %v", err)
	}
	if resp.Success || resp.ErrorMessage == "" || resp.LocalPath != "" {
		t.Errorf("respuesta inesperada %+v", resp)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Errorf("no debe escribir archivos, hay %d", len(entries))
	}
}

func TestSafeMediaPath(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "media")
	tests := []struct {
		name, id, in, ext string
		want              string
	}{
		{"traversal", "att-1", "../../.bashrc", ".bin", "att-1_.bashrc"},
		{"subdir", "att-2", "a/b.pdf", ".bin", "att-2_b.pdf"},
		{"empty", "att-3", "", ".jpg", "att-3_media.jpg"},
		{"dotdot", "att-4", "..", ".bin", "att-4_media.bin"},
		{"dot", "att-5", ".", ".bin", "att-5_media.bin"},
		{"backslash", "att-6", `..\..\x.txt`, ".bin", "att-6_.._.._x.txt"},
		{"normal", "att-7", "foto.png", ".jpg", "att-7_foto.png"},
		{"id traversal", "../x", "f.txt", ".bin", "x_f.txt"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := safeMediaPath(dir, tt.id, tt.in, tt.ext)
			if filepath.Dir(got) != dir {
				t.Fatalf("%q sale de %q (Dir=%q)", got, dir, filepath.Dir(got))
			}
			if base := filepath.Base(got); base != tt.want {
				t.Errorf("base = %q, se esperaba %q", base, tt.want)
			}
			if strings.Contains(filepath.Base(got), "/") {
				t.Errorf("el nombre contiene separador: %q", got)
			}
		})
	}
}

func TestWriteMediaAtomic(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "att-1_f.bin")
	if err := writeMediaAtomic(target, []byte("hola")); err != nil {
		t.Fatalf("writeMediaAtomic: %v", err)
	}
	got, err := os.ReadFile(target)
	if err != nil || string(got) != "hola" {
		t.Fatalf("contenido = %q, err=%v", got, err)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Errorf("deben quedar solo el destino, hay %d entradas (temporales sin limpiar)", len(entries))
	}
}
