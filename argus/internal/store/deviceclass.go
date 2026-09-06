package store

import (
	"context"
	"database/sql"
	"time"
)

// device_class is an Argus overlay (like item_priority / snmp_iface): which device class a Zabbix
// host was provisioned as (§C). Keyed by Zabbix host id. class_id is an Argus registry id
// (e.g. "linux-snmp"); source is "manual" (attach UI) or "discovered" (the future §B pipeline).

// SetDeviceClass records (or updates) a host's device class. source defaults to "manual".
func (s *Store) SetDeviceClass(ctx context.Context, hostID, classID, source string) error {
	if source == "" {
		source = "manual"
	}
	now := time.Now().Unix()
	_, err := s.db.ExecContext(ctx, `
INSERT INTO device_class (host_id, class_id, source, created_at, updated_at)
VALUES (?, ?, ?, ?, ?)
ON CONFLICT(host_id) DO UPDATE SET class_id=excluded.class_id, source=excluded.source, updated_at=excluded.updated_at`,
		hostID, classID, source, now, now)
	return err
}

// GetDeviceClass returns a host's class id and true, or ("", false) when the host has no class.
func (s *Store) GetDeviceClass(ctx context.Context, hostID string) (string, bool, error) {
	var classID string
	err := s.db.QueryRowContext(ctx, `SELECT class_id FROM device_class WHERE host_id = ?`, hostID).Scan(&classID)
	if err == sql.ErrNoRows {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return classID, true, nil
}

// DeviceClasses returns host id -> class id for every classified host (for the tree/overlay).
func (s *Store) DeviceClasses(ctx context.Context) (map[string]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT host_id, class_id FROM device_class`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var host, class string
		if err := rows.Scan(&host, &class); err != nil {
			return nil, err
		}
		out[host] = class
	}
	return out, rows.Err()
}

// DeleteDeviceClass removes a host's class overlay (e.g. when the host is deleted in Argus).
func (s *Store) DeleteDeviceClass(ctx context.Context, hostID string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM device_class WHERE host_id = ?`, hostID)
	return err
}
