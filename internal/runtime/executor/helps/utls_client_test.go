package helps

import (
	"crypto/md5"
	"fmt"
	"net"
	"strings"
	"testing"

	"golang.org/x/net/proxy"
)

func TestUTLSClientHelloFingerprints(t *testing.T) {
	tests := []struct {
		name       string
		profile    utlsClientProfile
		wantJA3    string
		wantJA3MD5 string
	}{
		{
			name:       "claude code",
			profile:    utlsProfileClaudeCode,
			wantJA3:    "771,4865-4866-4867-49195-49199-49196-49200-52393-52392-49161-49171-49162-49172-156-157-47-53,0-23-65281-10-11-35-16-5-13-18-51-45-43-21,29-23-24,0",
			wantJA3MD5: "d871d02cecbde59abbf8f4806134addf",
		},
		{
			name:       "codex cli",
			profile:    utlsProfileCodexCLI,
			wantJA3:    "771,255-49196-49195-49188-49187-49162-49161-49160-49200-49199-49192-49191-49172-49171-49170-157-156-61-60-53-47-10,0-10-11-13-5-18-23,23-24-25,0",
			wantJA3MD5: "e4d448cdfe06dc1243c1eb026c74ac9a",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clientHello := captureUTLSClientHello(t, tt.profile)
			gotJA3 := clientHelloJA3(t, clientHello)
			if gotJA3 != tt.wantJA3 {
				t.Fatalf("JA3 = %q, want %q", gotJA3, tt.wantJA3)
			}
			if gotJA3MD5 := fmt.Sprintf("%x", md5.Sum([]byte(gotJA3))); gotJA3MD5 != tt.wantJA3MD5 {
				t.Fatalf("JA3 md5 = %q, want %q", gotJA3MD5, tt.wantJA3MD5)
			}
		})
	}
}

func captureUTLSClientHello(t *testing.T, profile utlsClientProfile) []byte {
	t.Helper()

	ln, errListen := net.Listen("tcp", "127.0.0.1:0")
	if errListen != nil {
		t.Fatalf("listen failed: %v", errListen)
	}
	defer ln.Close()

	errCh := make(chan error, 1)
	go func() {
		conn, errDial := dialUTLSConn(proxy.Direct, "example.com", ln.Addr().String(), profile)
		if conn != nil {
			conn.Close()
		}
		errCh <- errDial
	}()

	conn, errAccept := ln.Accept()
	if errAccept != nil {
		t.Fatalf("accept failed: %v", errAccept)
	}
	defer conn.Close()

	header := make([]byte, 5)
	if _, errRead := conn.Read(header); errRead != nil {
		t.Fatalf("read TLS record header failed: %v", errRead)
	}
	recordLen := int(header[3])<<8 | int(header[4])
	body := make([]byte, recordLen)
	read := 0
	for read < recordLen {
		n, errRead := conn.Read(body[read:])
		if errRead != nil {
			t.Fatalf("read TLS record body failed: %v", errRead)
		}
		read += n
	}
	conn.Close()
	<-errCh

	return append(header, body...)
}

func clientHelloJA3(t *testing.T, record []byte) string {
	t.Helper()
	if len(record) < 5 || record[0] != 22 {
		t.Fatalf("not a TLS handshake record: %x", record[:min(len(record), 5)])
	}
	body := record[5:]
	if len(body) < 4 || body[0] != 1 {
		t.Fatalf("not a ClientHello: %x", body[:min(len(body), 4)])
	}

	p := 4
	legacyVersion := uint16(body[p])<<8 | uint16(body[p+1])
	p += 2 + 32
	sessionIDLen := int(body[p])
	p += 1 + sessionIDLen
	cipherLen := int(body[p])<<8 | int(body[p+1])
	p += 2
	ciphers := make([]uint16, 0, cipherLen/2)
	for i := p; i < p+cipherLen; i += 2 {
		ciphers = append(ciphers, uint16(body[i])<<8|uint16(body[i+1]))
	}
	p += cipherLen
	compressionLen := int(body[p])
	p += 1 + compressionLen

	var extensions, curves []uint16
	var points []byte
	if p+2 <= len(body) {
		extensionsLen := int(body[p])<<8 | int(body[p+1])
		p += 2
		end := min(len(body), p+extensionsLen)
		for p+4 <= end {
			extID := uint16(body[p])<<8 | uint16(body[p+1])
			extLen := int(body[p+2])<<8 | int(body[p+3])
			p += 4
			extData := body[p : p+extLen]
			p += extLen
			extensions = append(extensions, extID)
			switch extID {
			case 10:
				if len(extData) >= 2 {
					curvesLen := int(extData[0])<<8 | int(extData[1])
					for i := 2; i+1 < min(len(extData), 2+curvesLen); i += 2 {
						curves = append(curves, uint16(extData[i])<<8|uint16(extData[i+1]))
					}
				}
			case 11:
				if len(extData) >= 1 {
					pointsLen := int(extData[0])
					points = append(points, extData[1:min(len(extData), 1+pointsLen)]...)
				}
			}
		}
	}

	return strings.Join([]string{
		fmt.Sprint(legacyVersion),
		joinJA3Uint16s(ciphers),
		joinJA3Uint16s(extensions),
		joinJA3Uint16s(curves),
		joinJA3Bytes(points),
	}, ",")
}

func joinJA3Uint16s(values []uint16) string {
	parts := make([]string, 0, len(values))
	for _, value := range values {
		if isJA3GREASE(value) {
			continue
		}
		parts = append(parts, fmt.Sprint(value))
	}
	return strings.Join(parts, "-")
}

func joinJA3Bytes(values []byte) string {
	parts := make([]string, 0, len(values))
	for _, value := range values {
		parts = append(parts, fmt.Sprint(value))
	}
	return strings.Join(parts, "-")
}

func isJA3GREASE(value uint16) bool {
	return value == 0x0a0a || value == 0x1a1a || value == 0x2a2a || value == 0x3a3a ||
		value == 0x4a4a || value == 0x5a5a || value == 0x6a6a || value == 0x7a7a ||
		value == 0x8a8a || value == 0x9a9a || value == 0xaaaa || value == 0xbaba ||
		value == 0xcaca || value == 0xdada || value == 0xeaea || value == 0xfafa
}
