package storage

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ed25519"
	"crypto/x509"
	"crypto/x509/pkix"
	"database/sql"
	"encoding/binary"
	"math/big"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"
)

func TestB08MigrationPreservesCredentialsAndCAAndRollsBack(t *testing.T) {
	const ca = "ca_11111111111111111111111111111111"
	const proxy = "cred_22222222222222222222222222222222"
	const ts = "2026-11-17T10:00:00Z"
	ctx := context.Background()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "controller.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := db.ExecContext(ctx, query, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec("PRAGMA foreign_keys=ON")
	list, err := listMigrations(migrations)
	if err != nil {
		t.Fatal(err)
	}
	old, upgrade, broken := fstest.MapFS{}, fstest.MapFS{}, fstest.MapFS{}
	var newest string
	for _, item := range list {
		if item.version > 10 {
			break
		}
		data, err := migrations.ReadFile(item.name)
		if err != nil {
			t.Fatal(err)
		}
		if item.version <= 7 {
			old[item.name] = &fstest.MapFile{Data: data}
		}
		upgrade[item.name] = &fstest.MapFile{Data: data}
		broken[item.name] = &fstest.MapFile{Data: data}
		if item.version == 10 {
			newest = item.name
		}
	}
	if newest == "" {
		t.Fatal("010 must be installed in the isolated snapshot for this test")
	}
	broken[newest] = &fstest.MapFile{Data: append(bytes.Clone(upgrade[newest].Data), []byte("\nINSERT INTO nonexistent_b08_fixture_table VALUES (1);\n")...)}
	if err := migrate(ctx, db, old); err != nil {
		t.Fatal(err)
	}
	start, err := time.Parse(time.RFC3339, ts)
	if err != nil {
		t.Fatal(err)
	}
	caPrivate := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{3}, ed25519.SeedSize))
	cert := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "B08 test CA"}, NotBefore: start,
		NotAfter: start.Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	der, err := x509.CreateCertificate(bytes.NewReader(make([]byte, 64)), cert, cert, caPrivate.Public(), caPrivate)
	if err != nil {
		t.Fatal(err)
	}
	caPlain, err := x509.MarshalPKCS8PrivateKey(caPrivate)
	if err != nil {
		t.Fatal(err)
	}
	// Existing B01 AES-GCM envelope bytes; exercise binary data, both
	// purposes and the live CA foreign key. This test runs inside storage
	// so it cannot import secrets, which depends on storage.
	aesKey, err := aes.NewCipher(bytes.Repeat([]byte{4}, 32))
	if err != nil {
		t.Fatal(err)
	}
	aead, err := cipher.NewGCM(aesKey)
	if err != nil {
		t.Fatal(err)
	}
	legacySeal := func(n byte, id, purpose string, raw []byte) []byte {
		prefix := []byte("MS\x01\x01")
		nonce := bytes.Repeat([]byte{n}, aead.NonceSize())
		aad := bytes.Clone(prefix)
		for _, s := range []string{id, purpose, "legacy-key"} {
			aad = binary.BigEndian.AppendUint16(aad, uint16(len(s)))
			aad = append(aad, s...)
		}
		return aead.Seal(append(prefix, nonce...), nonce, raw, aad)
	}
	caBytes := legacySeal(1, ca, "management_ca", caPlain)
	proxyBytes := legacySeal(2, proxy, "proxy_entry", []byte(`{"password":"fixture-only-proxy-password","uuid":""}`))
	for _, row := range []struct {
		id, purpose string
		blob        []byte
	}{{ca, "management_ca", caBytes}, {proxy, "proxy_entry", proxyBytes}} {
		exec("INSERT INTO credentials(id,purpose,key_id,ciphertext,created_at) VALUES (?,?,'legacy-key',?,?)", row.id, row.purpose, row.blob, ts)
	}
	exec("INSERT INTO b02_management_ca(singleton,certificate,credential_ref) VALUES (1,?,?)", der, ca)
	exec("INSERT INTO rule_profiles(id,name,revision,updated_at) VALUES ('profile1','legacy',1,?)", ts)
	exec("INSERT INTO rule_profile_revisions(profile_id,revision,fallback_target,source_text) VALUES ('profile1',1,'REJECT','MATCH,REJECT')")
	oldHash := bytes.Repeat([]byte{0xad}, 32)
	exec("INSERT INTO subscriptions(id,name,revision,profile_id,profile_revision,expires_at,download_token_hash,token_version,updated_at) VALUES ('sub1','legacy',1,'profile1',1,?,?,6,?)", ts, oldHash, ts)
	// A populated 001–007 database: node, listener, authorization, B04 task,
	// historical groups/references and the hash-only subscription all survive.
	const node = "node_11111111111111111111111111111111"
	hash := strings.Repeat("a", 64)
	exec("INSERT INTO nodes(id,name,connection_mode,role,control_address,updated_at) VALUES (?,'legacy','direct','node','localhost',?)", node, ts)
	exec("INSERT INTO node_dns_revisions(node_id,revision,outbound,digest) VALUES (?,1,'DIRECT',?)", node, hash)
	exec("INSERT INTO listeners(id,node_id,name,revision,protocol,listen_address,listen_port,outer_transport,credential_ref,config_digest,dns_revision,updated_at) VALUES ('listener',?,'legacy',1,'ss2022','127.0.0.1',24000,'tcp',?,?,1,?)", node, proxy, hash, ts)
	exec("INSERT INTO routes(id,name,revision,updated_at) VALUES ('route','legacy',1,?)", ts)
	exec("INSERT INTO route_revisions(route_id,revision,entry_listener_id,terminal_node_id,digest) VALUES ('route',1,'listener',?,?)", node, hash)
	exec("UPDATE routes SET published_revision=1 WHERE id='route'")
	exec("INSERT INTO subscription_entries(id,subscription_id,route_id,position,listener_id,expires_at) VALUES ('entry','sub1','route',0,'listener','later')")
	exec("INSERT INTO entry_identities(id,entry_id,generation,credential_ref,expires_at) VALUES ('identity','entry',1,?,'later')", proxy)
	exec("INSERT INTO subscription_groups(id,subscription_id,name,selected_entry_id) VALUES ('group','sub1','All-Proxies','entry')")
	exec("INSERT INTO subscription_group_members(group_id,subscription_id,entry_id,position) VALUES ('group','sub1','entry',0)")
	exec("INSERT INTO listener_references(id,listener_id,entry_id) VALUES ('reference','listener','entry')")
	exec("INSERT INTO port_reservations(id,node_id,listener_id,purpose,address,transport,port) VALUES ('port',?,'listener','listener','127.0.0.1','tcp',24000)", node)
	exec("INSERT INTO tasks(id,kind,subject_id,target_revision,request_digest,idempotency_scope,idempotency_key,deadline,created_at) VALUES ('task','publish','route',1,?,'legacy','legacy','later',?)", hash, ts)
	exec("INSERT INTO task_nodes(task_id,node_id,target_revision,target_digest,revocation_epoch) VALUES ('task',?,2,?,1)", node, hash)
	exec("INSERT INTO b04_targets(task_id,node_id) VALUES ('task',?)", node)
	exec("INSERT INTO b04_grants(identity_id,entry_id,listener_id,node_id,generation,expires_at,grant_digest,origin_task_id) VALUES ('identity','entry','listener',?,1,'later',?,'task')", node, hash)
	if err := migrate(ctx, db, broken); err == nil {
		t.Fatal("broken 010 committed")
	}
	count := func(query string, want int) {
		t.Helper()
		var n int
		if err := db.QueryRowContext(ctx, query).Scan(&n); err != nil || n != want {
			t.Fatalf("unexpected count %d (wanted %d), err=%v", n, want, err)
		}
	}
	count("SELECT count(*) FROM schema_migrations", 7)
	count("SELECT count(*) FROM sqlite_schema WHERE name='subscription_download_credentials' OR name='b08_intents'", 0)
	count("SELECT count(*) FROM pragma_table_info('subscriptions') WHERE name='download_token_credential_ref'", 0)
	if err := migrate(ctx, db, upgrade); err != nil {
		t.Fatal(err)
	}
	count("SELECT count(*) FROM schema_migrations", 10)
	count("SELECT count(*) FROM pragma_foreign_key_check", 0)
	count("SELECT count(*) FROM subscription_entries e JOIN entry_identities i ON i.entry_id=e.id JOIN subscription_group_members m ON m.entry_id=e.id JOIN subscription_groups g ON g.selected_entry_id=e.id JOIN listener_references r ON r.entry_id=e.id JOIN b04_grants b ON b.identity_id=i.id WHERE e.id='entry' AND b.origin_task_id='task'", 1)
	count("PRAGMA foreign_keys", 1)

	for _, row := range []struct {
		id, purpose string
		blob        []byte
	}{{ca, "management_ca", caBytes}, {proxy, "proxy_entry", proxyBytes}} {
		var saved []byte
		var purpose, key, created string
		var revoked sql.NullString
		err := db.QueryRow("SELECT purpose,key_id,ciphertext,created_at,revoked_at FROM credentials WHERE id=?", row.id).Scan(&purpose, &key, &saved, &created, &revoked)
		if err != nil || purpose != row.purpose || key != "legacy-key" || created != ts || revoked.Valid || !bytes.Equal(saved, row.blob) {
			t.Fatal("old credential changed")
		}
	}
	var savedDER, savedHash []byte
	var ref string
	if err := db.QueryRow("SELECT certificate,credential_ref FROM b02_management_ca WHERE singleton=1").Scan(&savedDER, &ref); err != nil || !bytes.Equal(savedDER, der) || ref != ca {
		t.Fatal("management CA changed")
	}
	if _, err := db.Exec("DELETE FROM credentials WHERE id=?", ca); err == nil {
		t.Fatal("CA FK stopped protecting credential")
	}
	var downloadRef sql.NullString
	var version int
	if err := db.QueryRow("SELECT download_token_hash,token_version,download_token_credential_ref FROM subscriptions WHERE id='sub1'").Scan(&savedHash, &version, &downloadRef); err != nil || !bytes.Equal(savedHash, oldHash) || version != 6 || downloadRef.Valid {
		t.Fatal("legacy token state changed")
	}
}
