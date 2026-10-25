// Package mcs implements the MCS framing, login and heartbeat of one connection.
package mcs

import (
	"bufio"
	"encoding/binary"
	"fmt"
	"io"

	"google.golang.org/protobuf/proto"
)

// Version is the protocol version byte sent first; the legacy 38 is also accepted on read.
const Version = 41

// maxStanzaSize bounds a single inbound payload. The length prefix is attacker
// controlled, so without a cap a hostile or corrupt peer could announce a huge
// length and force an unbounded allocation. FCM data payloads run to a few KB;
// 1 MiB is far above any legitimate stanza.
const maxStanzaSize = 1 << 20

// MCS stanza tags, the ordinal of the message in the protocol.
const (
	TagHeartbeatPing     = byte(0)
	TagHeartbeatAck      = byte(1)
	TagLoginRequest      = byte(2)
	TagLoginResponse     = byte(3)
	TagClose             = byte(4)
	TagIqStanza          = byte(7)
	TagDataMessageStanza = byte(8)
	TagStreamErrorStanza = byte(10)
)

// Writer writes stanzas to one stream; the first one is preceded by the version byte.
type Writer struct {
	out         io.Writer
	versionSent bool
}

// NewWriter returns a Writer on w.
func NewWriter(w io.Writer) *Writer { return &Writer{out: w} }

// WriteStanza writes msg with the given tag as one packet.
func (w *Writer) WriteStanza(tag byte, msg proto.Message) error {
	// Version, tag, length and body go out in one Write: one TLS record and one
	// syscall per stanza instead of two.
	opts := proto.MarshalOptions{AllowPartial: true}
	size := opts.Size(msg)
	buf := make([]byte, 0, 2+binary.MaxVarintLen64+size)
	if !w.versionSent {
		buf = append(buf, Version)
		w.versionSent = true
	}
	buf = append(buf, tag)
	buf = binary.AppendUvarint(buf, uint64(size))
	buf, err := opts.MarshalAppend(buf, msg)
	if err != nil {
		return fmt.Errorf("marshal stanza %d: %w", tag, err)
	}
	if _, err = w.out.Write(buf); err != nil {
		return fmt.Errorf("write stanza %d: %w", tag, err)
	}
	return nil
}

// Reader reads stanzas from one stream.
type Reader struct {
	in *bufio.Reader
}

// NewReader returns a Reader on r.
func NewReader(r *bufio.Reader) *Reader { return &Reader{in: r} }

// ReadVersion reads the version byte the server sends before its first stanza.
func (r *Reader) ReadVersion() (byte, error) {
	v, err := r.in.ReadByte()
	if err != nil {
		return 0, fmt.Errorf("mcs: read version: %w", err)
	}
	if v < Version && v != 38 {
		return v, fmt.Errorf("mcs: bad protocol version %d", v)
	}
	return v, nil
}

// midStanzaError wraps a read error that hit after the tag byte. The stream is
// out of sync from that point, so the caller must reset rather than retry.
type midStanzaError struct{ err error }

func (e midStanzaError) Error() string { return "mcs: read failed mid-stanza: " + e.err.Error() }
func (e midStanzaError) Unwrap() error { return e.err }

// ReadStanza returns the tag and the raw protobuf payload. Caller unmarshals by tag.
// An error on the tag byte is returned as is, so a read-deadline timeout there
// means "idle" and leaves the stream intact; any later error is midStanzaError.
func (r *Reader) ReadStanza() (byte, []byte, error) {
	tag, err := r.in.ReadByte()
	if err != nil {
		return 0, nil, err
	}
	n, err := binary.ReadUvarint(r.in) // incremental; bufio.Reader is an io.ByteReader
	if err != nil {
		return 0, nil, midStanzaError{err}
	}
	if n > maxStanzaSize {
		return 0, nil, fmt.Errorf("mcs: stanza length %d exceeds limit %d", n, maxStanzaSize)
	}
	payload := make([]byte, n)
	if _, err := io.ReadFull(r.in, payload); err != nil {
		return 0, nil, midStanzaError{err}
	}
	return tag, payload, nil
}
