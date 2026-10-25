package fakedpi

import (
	"encoding/binary"
)

type helloStatus int

const (
	helloShort helloStatus = iota // a ClientHello so far, more bytes needed
	helloBad                      // not a TLS ClientHello, or malformed
	helloOK                       // complete ClientHello parsed
)

const (
	recordHandshake    = 22
	handshakeHello     = 1
	maxRecordLen       = 1<<14 + 2048
	extServerName      = 0
	serverNameHostName = 0
)

// isHelloStart reports whether p starts a TLS handshake record carrying a ClientHello.
func isHelloStart(p []byte) bool {
	return len(p) >= 6 && p[0] == recordHandshake && p[1] == 3 && p[5] == handshakeHello
}

// parseHello parses the TLS ClientHello at the start of b and returns its SNI ("" when it has none).
// With oneRecord the hello must be complete in the first record; otherwise handshake fragments of
// consecutive records are joined.
func parseHello(b []byte, oneRecord bool) (string, helloStatus) {
	var msg []byte
	need := -1
	for {
		if len(b) < 5 {
			return "", helloShort
		}
		if b[0] != recordHandshake || b[1] != 3 {
			return "", helloBad
		}
		n := int(binary.BigEndian.Uint16(b[3:5]))
		if n == 0 || n > maxRecordLen {
			return "", helloBad
		}
		if len(b) < 5+n {
			if oneRecord {
				return "", helloBad
			}
			return "", helloShort
		}
		msg = append(msg, b[5:5+n]...)
		b = b[5+n:]
		if msg[0] != handshakeHello {
			return "", helloBad
		}
		if need < 0 && len(msg) >= 4 {
			need = 4 + (int(msg[1])<<16 | int(msg[2])<<8 | int(msg[3]))
		}
		if need >= 0 && len(msg) >= need {
			sni, ok := helloSNI(msg[4:need])
			if !ok {
				return "", helloBad
			}
			return sni, helloOK
		}
		if oneRecord {
			return "", helloBad
		}
	}
}

// helloSNI walks a ClientHello body to the server_name extension.
func helloSNI(h []byte) (string, bool) {
	if len(h) < 2+32 {
		return "", false
	}
	h = h[34:] // legacy_version, random
	var ok bool
	if _, h, ok = take8(h); !ok { // session id
		return "", false
	}
	if _, h, ok = take16(h); !ok { // cipher suites
		return "", false
	}
	if _, h, ok = take8(h); !ok { // compression methods
		return "", false
	}
	if len(h) == 0 {
		return "", true
	}
	exts, _, ok := take16(h)
	if !ok {
		return "", false
	}
	for len(exts) >= 4 {
		typ := binary.BigEndian.Uint16(exts[0:2])
		body, rest, ok := take16(exts[2:])
		if !ok {
			return "", false
		}
		if typ == extServerName {
			return serverName(body)
		}
		exts = rest
	}
	return "", true
}

func serverName(b []byte) (string, bool) {
	list, _, ok := take16(b)
	if !ok {
		return "", false
	}
	for len(list) >= 3 {
		typ := list[0]
		name, rest, ok := take16(list[1:])
		if !ok {
			return "", false
		}
		if typ == serverNameHostName {
			s := normalizeName(string(name))
			if s == "" || len(s) > 255 {
				return "", false
			}
			return s, true
		}
		list = rest
	}
	return "", true
}

func take8(b []byte) (field, rest []byte, ok bool) {
	if len(b) < 1 || len(b) < 1+int(b[0]) {
		return nil, nil, false
	}
	n := int(b[0])
	return b[1 : 1+n], b[1+n:], true
}

func take16(b []byte) (field, rest []byte, ok bool) {
	if len(b) < 2 {
		return nil, nil, false
	}
	n := int(binary.BigEndian.Uint16(b[0:2]))
	if len(b) < 2+n {
		return nil, nil, false
	}
	return b[2 : 2+n], b[2+n:], true
}
