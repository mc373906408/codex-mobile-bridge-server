package worker

import (
	"bytes"
	"io"
	"mime"
	"mime/quotedprintable"
	"net/mail"
	"testing"

	"github.com/mc373906408/codex-mobile-bridge-server/internal/store"
)

func TestSMTPMessageSupportsLocalizedCodesWithoutRawUTF8Headers(t *testing.T) {
	message := store.MailMessage{Subject: "Bridge code / \u90ae\u7bb1\u9a8c\u8bc1\u7801", Body: "012345\r\n\u5341\u5206\u949f\u5185\u6709\u6548\r\n"}
	var encoded bytes.Buffer
	if err := writeSMTPMessage(&encoded, &mail.Address{Address: "bridge@example.test"}, &mail.Address{Address: "user@example.test"}, message); err != nil {
		t.Fatal(err)
	}
	for _, value := range encoded.Bytes() {
		if value > 127 {
			t.Fatal("localized mail must use MIME encoding, not raw SMTP UTF-8")
		}
	}
	parsed, err := mail.ReadMessage(bytes.NewReader(encoded.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	subject, err := (&mime.WordDecoder{}).DecodeHeader(parsed.Header.Get("Subject"))
	if err != nil || subject != message.Subject {
		t.Fatal("localized subject must round-trip", err)
	}
	decoded, err := io.ReadAll(quotedprintable.NewReader(parsed.Body))
	if err != nil || string(decoded) != message.Body {
		t.Fatal("email codes and localized body must round-trip", err)
	}
}
