package mihomo

import (
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestProtocolFields(t *testing.T) {
	k, _ := ecdh.X25519().GenerateKey(rand.Reader)
	for _, p := range []Protocol{SS2022, Trojan, VLESS, Hysteria2} {
		e := Entry{NodeID: "node_a", ListenerID: "listener_a", IdentityID: "identity_a", Address: "127.0.0.1", Port: 20001, Protocol: p, Cipher: "2022-blake3-aes-128-gcm", Password: base64.StdEncoding.EncodeToString(make([]byte, 16)), UUID: "00112233-4455-6677-8899-aabbccddeeff", Certificate: "/private/cert.pem", PrivateKey: "/private/key.pem", RealityPrivateKey: base64.RawURLEncoding.EncodeToString(k.Bytes()), RealityDestination: "127.0.0.1:4443", ServerNames: []string{"test.local"}, ShortIDs: []string{"0123456789abcdef"}}
		c, exp, err := Generate([]Entry{e}, DNS{Enable: true}, "/private/api.sock", []string{"MATCH,REJECT"})
		if err != nil {
			t.Fatal(p, err)
		}
		b, _ := json.Marshal(c)
		var round Config
		if err = json.Unmarshal(b, &round); err != nil {
			t.Fatal(p, err)
		}
		again, _ := json.Marshal(round)
		if string(b) != string(again) {
			t.Fatal("roundtrip")
		}
		if exp[0].TCP == (p == Hysteria2) {
			t.Fatal("outer transport")
		}
		if exp[0].BusinessUDP {
			t.Fatal("QUIC is not business UDP")
		}
		e.Password = "bad"
		e.UUID = "bad"
		if _, err = BuildListener(e); err == nil {
			t.Fatal(p, "invalid credential accepted")
		}
	}
}
func TestErrorsAndIsolation(t *testing.T) {
	e := Entry{NodeID: "node_a", ListenerID: "listener_a", IdentityID: "identity_a", Address: "127.0.0.1", Port: 20001, Protocol: SS2022, Cipher: "2022-blake3-aes-128-gcm", Password: "SECRET_VALUE"}
	_, err := BuildListener(e)
	if !errors.Is(err, ErrInvalid) || strings.Contains(err.Error(), e.Password) {
		t.Fatal("unsafe error")
	}
	e.Password = base64.StdEncoding.EncodeToString(make([]byte, 16))
	if _, _, err = Generate([]Entry{e, e}, DNS{}, "", nil); err == nil {
		t.Fatal("shared identity/listener accepted")
	}
}
