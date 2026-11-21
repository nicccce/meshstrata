package agent

import (
	"bytes"
	"context"
	"crypto/ecdh"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"

	"github.com/nicccce/meshstrata/agent/internal/checkpoint"
	"github.com/nicccce/meshstrata/internal/config"
	"github.com/nicccce/meshstrata/internal/control"
	"github.com/nicccce/meshstrata/internal/localstate"
)

// ImportMaterials accepts local stdin only; no remote paths, shell or arbitrary
// filenames. References must already be approved in the trusted configuration.
// Import once before Run, or stop the Agent and explicitly import a new reference.
func ImportMaterials(ctx context.Context, cfg config.Agent, input io.Reader) error {
	if cfg.Runtime == nil {
		return errors.New("runtime configuration required")
	}
	unlock, err := localstate.Lock(cfg.StateDir)
	if err != nil {
		return err
	}
	defer unlock()
	raw, err := io.ReadAll(io.LimitReader(input, (512<<10)+1))
	defer clear(raw)
	if err != nil || len(raw) > 512<<10 || !control.UniqueJSON(raw) {
		return errors.New("invalid local material input")
	}
	var in struct {
		Materials []struct {
			Ref  control.LocalMaterial `json:"ref"`
			Data []byte                `json:"data"`
		} `json:"materials"`
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&in) != nil || decoder.Decode(new(any)) != io.EOF || len(in.Materials) < 1 || len(in.Materials) > 32 {
		return errors.New("invalid local material input")
	}
	defer func() {
		for _, m := range in.Materials {
			clear(m.Data)
		}
	}()
	dir, err := checkpoint.OpenPrivateDir(filepath.Join(cfg.StateDir, "runtime", "config", "materials"))
	if err != nil {
		return err
	}
	defer dir.Close()
	seen := map[control.LocalMaterial]bool{}
	for _, m := range in.Materials {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if !m.Ref.ID.Valid() || !slices.Contains(cfg.Runtime.Materials, m.Ref) || seen[m.Ref] || len(m.Data) == 0 || len(m.Data) > 256<<10 {
			return errors.New("material reference not approved")
		}
		seen[m.Ref] = true
		switch m.Ref.Purpose {
		case "tls_certificate":
			block, _ := pem.Decode(m.Data)
			if block == nil || block.Type != "CERTIFICATE" {
				return errors.New("invalid business certificate")
			}
			cert, e := x509.ParseCertificate(block.Bytes)
			if e != nil || cert.IsCA {
				return errors.New("business leaf certificate required")
			}
		case "tls_private_key":
			block, _ := pem.Decode(m.Data)
			if block == nil {
				return errors.New("invalid business key")
			}
			switch block.Type {
			case "PRIVATE KEY":
				_, err = x509.ParsePKCS8PrivateKey(block.Bytes)
			case "RSA PRIVATE KEY":
				_, err = x509.ParsePKCS1PrivateKey(block.Bytes)
			case "EC PRIVATE KEY":
				_, err = x509.ParseECPrivateKey(block.Bytes)
			default:
				err = errors.New("unsupported key")
			}
			if err != nil {
				return errors.New("invalid business key")
			}
		case "reality_private_key":
			value := bytes.TrimSpace(m.Data)
			key, e := base64.RawURLEncoding.Strict().DecodeString(string(value))
			if e != nil {
				return errors.New("invalid reality key")
			}
			_, e = ecdh.X25519().NewPrivateKey(key)
			clear(key)
			if e != nil {
				return errors.New("invalid reality key")
			}
		default:
			return errors.New("unsupported material purpose")
		}
		// Replacing a referenced file could silently change an old immutable Pin.
		name := string(m.Ref.ID) + "." + m.Ref.Purpose
		if old, e := dir.Read(name, 256<<10); e == nil {
			same := bytes.Equal(old, m.Data)
			clear(old)
			if !same {
				return errors.New("material already exists; allocate a new reference")
			}
			continue
		} else if !errors.Is(e, os.ErrNotExist) {
			return e
		}
		if _, err = dir.Replace(name, m.Data); err != nil {
			return err
		}
	}
	return nil
}
