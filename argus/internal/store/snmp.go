package store

import (
	"context"
	"database/sql"
	"time"
)

// SNMPDefault is a proxy's default SNMP credentials (the source of truth for hosts that inherit).
// Community and the v3 passphrases are held in plaintext here but stored encrypted.
type SNMPDefault struct {
	Version     int    `json:"version"`
	Community    string `json:"community"`
	Bulk         int    `json:"bulk"`
	SecName      string `json:"security_name"`
	SecLevel     int    `json:"security_level"`
	AuthProto    int    `json:"auth_protocol"`
	AuthPass     string `json:"auth_passphrase"`
	PrivProto    int    `json:"priv_protocol"`
	PrivPass     string `json:"priv_passphrase"`
	ContextName  string `json:"context_name"`
}

// SNMPDefaultFor returns a proxy's SNMP default (decrypted), and whether one is set.
func (s *Store) SNMPDefaultFor(ctx context.Context, proxyID string) (SNMPDefault, bool, error) {
	var d SNMPDefault
	var community, authPass, privPass sql.NullString
	err := s.db.QueryRowContext(ctx,
		`SELECT version, community, bulk, sec_name, sec_level, auth_proto, auth_pass, priv_proto, priv_pass, context_name
		 FROM snmp_defaults WHERE proxy_id=?`, proxyID).
		Scan(&d.Version, &community, &d.Bulk, &d.SecName, &d.SecLevel, &d.AuthProto, &authPass, &d.PrivProto, &privPass, &d.ContextName)
	if err == sql.ErrNoRows {
		return SNMPDefault{}, false, nil
	}
	if err != nil {
		return SNMPDefault{}, false, err
	}
	d.Community = s.cipher.Decrypt(community.String)
	d.AuthPass = s.cipher.Decrypt(authPass.String)
	d.PrivPass = s.cipher.Decrypt(privPass.String)
	return d, true, nil
}

// SetSNMPDefault upserts a proxy's SNMP default, encrypting the secret fields.
func (s *Store) SetSNMPDefault(ctx context.Context, proxyID string, d SNMPDefault) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO snmp_defaults(proxy_id, version, community, bulk, sec_name, sec_level, auth_proto, auth_pass, priv_proto, priv_pass, context_name, updated_at)
		 VALUES(?,?,?,?,?,?,?,?,?,?,?,?)
		 ON CONFLICT(proxy_id) DO UPDATE SET
		   version=excluded.version, community=excluded.community, bulk=excluded.bulk,
		   sec_name=excluded.sec_name, sec_level=excluded.sec_level,
		   auth_proto=excluded.auth_proto, auth_pass=excluded.auth_pass,
		   priv_proto=excluded.priv_proto, priv_pass=excluded.priv_pass,
		   context_name=excluded.context_name, updated_at=excluded.updated_at`,
		proxyID, d.Version, s.cipher.Encrypt(d.Community), d.Bulk, d.SecName, d.SecLevel,
		d.AuthProto, s.cipher.Encrypt(d.AuthPass), d.PrivProto, s.cipher.Encrypt(d.PrivPass),
		d.ContextName, time.Now().Unix())
	return err
}

// SNMPInheritMap returns the inherit flag for every tracked SNMP interface (interface_id -> inherit).
func (s *Store) SNMPInheritMap(ctx context.Context) (map[string]bool, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT interface_id, inherit FROM snmp_iface`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var id string
		var inherit int
		if err := rows.Scan(&id, &inherit); err != nil {
			return nil, err
		}
		out[id] = inherit != 0
	}
	return out, rows.Err()
}

// SetSNMPInherit records whether an SNMP interface inherits its proxy's default.
func (s *Store) SetSNMPInherit(ctx context.Context, interfaceID string, inherit bool) error {
	v := 0
	if inherit {
		v = 1
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO snmp_iface(interface_id, inherit) VALUES(?,?)
		 ON CONFLICT(interface_id) DO UPDATE SET inherit=excluded.inherit`, interfaceID, v)
	return err
}

// DeleteSNMPInherit forgets an interface's inherit state (call when the interface is deleted).
func (s *Store) DeleteSNMPInherit(ctx context.Context, interfaceID string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM snmp_iface WHERE interface_id=?`, interfaceID)
	return err
}
