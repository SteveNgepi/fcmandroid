package mcs

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"net"
	"testing"

	"github.com/SteveNgepi/fcmandroid/internal/pb"

	"google.golang.org/protobuf/proto"
)

func TestFrame_FirstPacketVersionByte(t *testing.T) {
	var buf bytes.Buffer
	w := NewWriter(&buf)
	ping := &pb.HeartbeatPing{}
	if err := w.WriteStanza(TagHeartbeatPing, ping); err != nil {
		t.Fatal(err)
	}
	got := buf.Bytes()
	if got[0] != Version {
		t.Fatalf("first byte = %d, want %d (version)", got[0], Version)
	}
	if got[1] != TagHeartbeatPing {
		t.Fatalf("second byte = %d, want tag", got[1])
	}
	// second stanza on the same writer: NO version byte
	buf.Reset()
	if err := w.WriteStanza(TagHeartbeatPing, ping); err != nil {
		t.Fatal(err)
	}
	if buf.Bytes()[0] != TagHeartbeatPing {
		t.Fatalf("second stanza must not re-emit version byte")
	}
}

func TestFrame_RejectsOversizedStanza(t *testing.T) {
	// A hostile or corrupt peer announces a payload far larger than any real
	// stanza. The reader must reject the length rather than try to allocate it.
	var b bytes.Buffer
	b.WriteByte(TagDataMessageStanza)
	var lv [binary.MaxVarintLen64]byte
	n := binary.PutUvarint(lv[:], maxStanzaSize+1)
	b.Write(lv[:n])

	r := NewReader(bufio.NewReader(&b))
	if _, _, err := r.ReadStanza(); err == nil {
		t.Fatal("ReadStanza accepted an oversized length, want error")
	}
}

func TestFrame_ReadSplitAcrossFill(t *testing.T) {
	cli, srv := net.Pipe()
	defer cli.Close()
	defer srv.Close()
	go func() {
		w := NewWriter(srv)
		w.versionSent = true // simulate: server writes its version byte then a stanza
		srv.Write([]byte{Version})
		dm := &pb.DataMessageStanza{PersistentId: new("pid-1")}
		w.WriteStanza(TagDataMessageStanza, dm)
		srv.Close()
	}()
	r := NewReader(bufio.NewReaderSize(cli, 1)) // 1-byte buffer forces split reads
	ver, err := r.ReadVersion()
	if err != nil || ver != Version {
		t.Fatalf("ReadVersion=%d err=%v", ver, err)
	}
	tag, payload, err := r.ReadStanza()
	if err != nil {
		t.Fatalf("ReadStanza: %v", err)
	}
	if tag != TagDataMessageStanza {
		t.Fatalf("tag=%d", tag)
	}
	var dm pb.DataMessageStanza
	if err := (proto.UnmarshalOptions{AllowPartial: true}).Unmarshal(payload, &dm); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if dm.GetPersistentId() != "pid-1" {
		t.Fatalf("payload pid=%q", dm.GetPersistentId())
	}
}
