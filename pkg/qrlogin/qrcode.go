package qrlogin

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net/url"
	"unicode/utf8"
)

// Intent tells which device shows the QR code.
type Intent int

const (
	// IntentLogin: the new device shows the QR code.
	IntentLogin Intent = iota
	// IntentReciprocate: the signed-in device shows the QR code.
	IntentReciprocate
)

// Variant is the version of the QR code and of the secure channel.
type Variant int

const (
	// VariantMSC4108 is the MSC4108 (2024) format: an ECIES channel over a
	// rendezvous URL.
	VariantMSC4108 Variant = iota
	// VariantMSC4388 is the MSC4388 format: an HPKE channel over a rendezvous
	// ID on the homeserver.
	VariantMSC4388
)

func (v Variant) String() string {
	if v == VariantMSC4388 {
		return "msc4388"
	}
	return "msc4108"
}

var (
	msc4108Prefix = []byte("MATRIX")
	msc4388Prefix = []byte("IO_ELEMENT_MSC4388")
)

const (
	msc4108Version           = 0x02
	msc4108IntentLogin       = 0x03
	msc4108IntentReciprocate = 0x04

	msc4388Type              = 0x03
	msc4388IntentLogin       = 0x00
	msc4388IntentReciprocate = 0x01
)

// ErrInvalidQRCode is wrapped by all QR code decoding errors.
var ErrInvalidQRCode = errors.New("invalid login QR code")

// QRCodeData is the content of a login QR code.
type QRCodeData struct {
	Variant   Variant
	Intent    Intent
	PublicKey [32]byte

	// RendezvousURL is set for VariantMSC4108.
	RendezvousURL string
	// ServerName is set for VariantMSC4108 with IntentReciprocate. It is the
	// homeserver of the signed-in device.
	ServerName string

	// RendezvousID and BaseURL are set for VariantMSC4388.
	RendezvousID string
	BaseURL      string
}

// Bytes encodes the QR code data. Put the result in a QR code in byte mode.
func (q *QRCodeData) Bytes() ([]byte, error) {
	var buf bytes.Buffer
	switch q.Variant {
	case VariantMSC4108:
		if len(q.RendezvousURL) > 0xffff || len(q.ServerName) > 0xffff {
			return nil, fmt.Errorf("%w: URL too long", ErrInvalidQRCode)
		}
		buf.Write(msc4108Prefix)
		buf.WriteByte(msc4108Version)
		if q.Intent == IntentReciprocate {
			buf.WriteByte(msc4108IntentReciprocate)
		} else {
			buf.WriteByte(msc4108IntentLogin)
		}
		buf.Write(q.PublicKey[:])
		_ = binary.Write(&buf, binary.BigEndian, uint16(len(q.RendezvousURL)))
		buf.WriteString(q.RendezvousURL)
		if q.Intent == IntentReciprocate {
			_ = binary.Write(&buf, binary.BigEndian, uint16(len(q.ServerName)))
			buf.WriteString(q.ServerName)
		}
	case VariantMSC4388:
		if len(q.RendezvousID) > 0xff || len(q.BaseURL) > 0xff {
			return nil, fmt.Errorf("%w: rendezvous ID or base URL too long", ErrInvalidQRCode)
		}
		buf.Write(msc4388Prefix)
		buf.WriteByte(msc4388Type)
		if q.Intent == IntentReciprocate {
			buf.WriteByte(msc4388IntentReciprocate)
		} else {
			buf.WriteByte(msc4388IntentLogin)
		}
		buf.Write(q.PublicKey[:])
		buf.WriteByte(byte(len(q.RendezvousID)))
		buf.WriteString(q.RendezvousID)
		buf.WriteByte(byte(len(q.BaseURL)))
		buf.WriteString(q.BaseURL)
	default:
		return nil, fmt.Errorf("%w: unknown variant %d", ErrInvalidQRCode, q.Variant)
	}
	return buf.Bytes(), nil
}

// Base64 encodes the QR code data as unpadded standard base64, as vodozemac
// does.
func (q *QRCodeData) Base64() (string, error) {
	data, err := q.Bytes()
	if err != nil {
		return "", err
	}
	return base64.RawStdEncoding.EncodeToString(data), nil
}

// ParseQRCode decodes QR code data in either format.
func ParseQRCode(data []byte) (*QRCodeData, error) {
	r := bytes.NewReader(data)
	if bytes.HasPrefix(data, msc4108Prefix) {
		return parseMSC4108(r)
	}
	return parseMSC4388(r)
}

// ParseQRCodeBase64 decodes base64 QR code data in either format.
func ParseQRCodeBase64(data string) (*QRCodeData, error) {
	raw, err := decodeBase64(data)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidQRCode, err)
	}
	return ParseQRCode(raw)
}

func parseMSC4108(r *bytes.Reader) (*QRCodeData, error) {
	q := &QRCodeData{Variant: VariantMSC4108}
	if _, err := r.Seek(int64(len(msc4108Prefix)), io.SeekStart); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidQRCode, err)
	}
	version, err := r.ReadByte()
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidQRCode, err)
	} else if version != msc4108Version {
		return nil, fmt.Errorf("%w: unsupported version 0x%02x", ErrInvalidQRCode, version)
	}
	intent, err := r.ReadByte()
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidQRCode, err)
	}
	switch intent {
	case msc4108IntentLogin:
		q.Intent = IntentLogin
	case msc4108IntentReciprocate:
		q.Intent = IntentReciprocate
	default:
		return nil, fmt.Errorf("%w: unknown intent 0x%02x", ErrInvalidQRCode, intent)
	}
	if _, err = io.ReadFull(r, q.PublicKey[:]); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidQRCode, err)
	}
	if q.RendezvousURL, err = readString16(r); err != nil {
		return nil, err
	} else if _, err = url.Parse(q.RendezvousURL); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidQRCode, err)
	}
	if q.Intent == IntentReciprocate {
		if q.ServerName, err = readString16(r); err != nil {
			return nil, err
		}
	}
	return q, nil
}

func parseMSC4388(r *bytes.Reader) (*QRCodeData, error) {
	q := &QRCodeData{Variant: VariantMSC4388}
	prefix := make([]byte, len(msc4388Prefix))
	if _, err := io.ReadFull(r, prefix); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidQRCode, err)
	} else if !bytes.Equal(prefix, msc4388Prefix) {
		return nil, fmt.Errorf("%w: unknown prefix", ErrInvalidQRCode)
	}
	qrType, err := r.ReadByte()
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidQRCode, err)
	} else if qrType != msc4388Type {
		return nil, fmt.Errorf("%w: unsupported type 0x%02x", ErrInvalidQRCode, qrType)
	}
	intent, err := r.ReadByte()
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidQRCode, err)
	}
	switch intent {
	case msc4388IntentLogin:
		q.Intent = IntentLogin
	case msc4388IntentReciprocate:
		q.Intent = IntentReciprocate
	default:
		return nil, fmt.Errorf("%w: unknown intent 0x%02x", ErrInvalidQRCode, intent)
	}
	if _, err = io.ReadFull(r, q.PublicKey[:]); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidQRCode, err)
	}
	if q.RendezvousID, err = readString8(r); err != nil {
		return nil, err
	}
	if q.BaseURL, err = readString8(r); err != nil {
		return nil, err
	} else if _, err = url.Parse(q.BaseURL); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidQRCode, err)
	}
	return q, nil
}

func readString16(r *bytes.Reader) (string, error) {
	var n uint16
	if err := binary.Read(r, binary.BigEndian, &n); err != nil {
		return "", fmt.Errorf("%w: %w", ErrInvalidQRCode, err)
	}
	return readN(r, int(n))
}

func readString8(r *bytes.Reader) (string, error) {
	n, err := r.ReadByte()
	if err != nil {
		return "", fmt.Errorf("%w: %w", ErrInvalidQRCode, err)
	}
	return readN(r, int(n))
}

func readN(r *bytes.Reader, n int) (string, error) {
	buf := make([]byte, n)
	if _, err := io.ReadFull(r, buf); err != nil {
		return "", fmt.Errorf("%w: %w", ErrInvalidQRCode, err)
	} else if !utf8.Valid(buf) {
		return "", fmt.Errorf("%w: string is not UTF-8", ErrInvalidQRCode)
	}
	return string(buf), nil
}

// decodeBase64 accepts standard base64 with or without padding. vodozemac
// encodes without padding.
func decodeBase64(s string) ([]byte, error) {
	if len(s)%4 == 0 {
		if out, err := base64.StdEncoding.DecodeString(s); err == nil {
			return out, nil
		}
	}
	return base64.RawStdEncoding.DecodeString(s)
}

func encodeBase64(b []byte) string {
	return base64.RawStdEncoding.EncodeToString(b)
}
