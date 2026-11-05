package relay

import (
	"encoding/hex"
	"errors"

	"github.com/mc373906408/codex-mobile-bridge-server/internal/security"
)

const Version byte = 2
const Open byte = 1
const Record byte = 2
const Close byte = 3
const HeaderSize = 34
const MaxPlaintext = 32768
const MaxRecord = MaxPlaintext + 16
const MaxFrame = HeaderSize + MaxRecord

type Frame struct {
	Kind                 byte
	BindingID, ChannelID string
	Payload              []byte
}

func (f Frame) Encode() ([]byte, error) {
	if f.Kind < Open || f.Kind > Close || len(f.Payload) > MaxRecord {
		return nil, errors.New("invalid frame")
	}
	binding, err := security.IDBytes(f.BindingID)
	if err != nil {
		return nil, err
	}
	channel, err := security.IDBytes(f.ChannelID)
	if err != nil {
		return nil, err
	}
	out := make([]byte, 0, HeaderSize+len(f.Payload))
	out = append(out, Version, f.Kind)
	out = append(out, binding...)
	out = append(out, channel...)
	out = append(out, f.Payload...)
	return out, nil
}

func Decode(data []byte) (Frame, error) {
	if len(data) < HeaderSize || len(data) > MaxFrame || data[0] != Version || data[1] < Open || data[1] > Close {
		return Frame{}, errors.New("invalid frame")
	}
	id := func(b []byte) string {
		s := hex.EncodeToString(b)
		return s[:8] + "-" + s[8:12] + "-" + s[12:16] + "-" + s[16:20] + "-" + s[20:]
	}
	f := Frame{Kind: data[1], BindingID: id(data[2:18]), ChannelID: id(data[18:34]), Payload: data[34:]}
	if (f.Kind == Open && len(f.Payload) != 32) || (f.Kind == Close && len(f.Payload) != 0) || (f.Kind == Record && len(f.Payload) < 16) {
		return Frame{}, errors.New("invalid frame payload")
	}
	return f, nil
}
