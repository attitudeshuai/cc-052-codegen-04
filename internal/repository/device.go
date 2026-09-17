package repository

import (
	"cc-052/internal/model"
	"database/sql"
	"time"

	"github.com/jmoiron/sqlx"
)

type DeviceRepo struct {
	db *sqlx.DB
}

func NewDeviceRepo(db *sqlx.DB) *DeviceRepo {
	return &DeviceRepo{db: db}
}

func (r *DeviceRepo) Create(d *model.Device) error {
	query := `INSERT INTO device (device_code, plot_id, operator, status)
	          VALUES ($1, $2, $3, $4)
	          RETURNING id, created_at, updated_at`
	return r.db.QueryRow(query, d.DeviceCode, d.PlotID, d.Operator, d.Status).
		Scan(&d.ID, &d.CreatedAt, &d.UpdatedAt)
}

func (r *DeviceRepo) GetByID(id int64) (*model.Device, error) {
	var d model.Device
	query := `SELECT id, device_code, plot_id, operator, status, created_at, updated_at
	          FROM device WHERE id = $1`
	if err := r.db.Get(&d, query, id); err != nil {
		return nil, err
	}
	return &d, nil
}

// GetByCode returns (nil, nil) when the code is not registered.
func (r *DeviceRepo) GetByCode(code string) (*model.Device, error) {
	var d model.Device
	query := `SELECT id, device_code, plot_id, operator, status, created_at, updated_at
	          FROM device WHERE device_code = $1`
	if err := r.db.Get(&d, query, code); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	return &d, nil
}

func (r *DeviceRepo) List(plotID *int64, status *string) ([]model.DeviceView, error) {
	var devices []model.DeviceView
	query := `SELECT d.id, d.device_code, d.plot_id, d.operator, d.status, d.created_at, d.updated_at,
	                 p.name AS plot_name, p.farm_id AS farm_id
	          FROM device d JOIN plot p ON p.id = d.plot_id
	          WHERE ($1::bigint IS NULL OR d.plot_id = $1)
	            AND ($2::text IS NULL OR d.status = $2)
	          ORDER BY d.id`
	if err := r.db.Select(&devices, query, plotID, status); err != nil {
		return nil, err
	}
	return devices, nil
}

// Update writes the mutable registry fields; upload progress is untouched.
func (r *DeviceRepo) Update(d *model.Device) error {
	query := `UPDATE device SET plot_id = $1, operator = $2, status = $3, updated_at = NOW()
	          WHERE id = $4 RETURNING updated_at`
	return r.db.QueryRow(query, d.PlotID, d.Operator, d.Status, d.ID).Scan(&d.UpdatedAt)
}

// GetUploadBySeq returns (nil, nil) when the seq has not been recorded yet.
func (r *DeviceRepo) GetUploadBySeq(deviceID, seq int64) (*model.DeviceUpload, error) {
	var u model.DeviceUpload
	query := `SELECT id, device_id, seq, client_uuid, activity_id, status, reason, received_at
	          FROM device_upload WHERE device_id = $1 AND seq = $2`
	if err := r.db.Get(&u, query, deviceID, seq); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	return &u, nil
}

// GetUploadByUUID returns (nil, nil) when the client_uuid is unknown.
func (r *DeviceRepo) GetUploadByUUID(deviceID int64, clientUUID string) (*model.DeviceUpload, error) {
	var u model.DeviceUpload
	query := `SELECT id, device_id, seq, client_uuid, activity_id, status, reason, received_at
	          FROM device_upload WHERE device_id = $1 AND client_uuid = $2`
	if err := r.db.Get(&u, query, deviceID, clientUUID); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	return &u, nil
}

// RecordRejected writes a rejected ledger entry (first write wins).
func (r *DeviceRepo) RecordRejected(deviceID, seq int64, clientUUID, reason string) error {
	query := `INSERT INTO device_upload (device_id, seq, client_uuid, activity_id, status, reason)
	          VALUES ($1, $2, $3, NULL, 'rejected', $4)
	          ON CONFLICT (device_id, seq) DO NOTHING`
	_, err := r.db.Exec(query, deviceID, seq, clientUUID, reason)
	return err
}

// RecordAccepted stores the activity (idempotent on client_uuid) and the
// ledger entry in one transaction, so a dropped connection leaves the
// ledger exactly reflecting what landed — the basis for resumable sync.
//
// ledgerWritten=false means a concurrent sync recorded the same
// (device_id, seq) first; the activity insert is rolled back and the caller
// should re-read the ledger and classify.
func (r *DeviceRepo) RecordAccepted(deviceID, seq int64, clientUUID string, a *model.Activity) (activityID int64, activityInserted bool, ledgerWritten bool, err error) {
	tx, err := r.db.Beginx()
	if err != nil {
		return 0, false, false, err
	}
	defer tx.Rollback()

	err = tx.QueryRow(`INSERT INTO activity (batch_id, client_uuid, kind, happened_at, input_id, dose, dose_unit, operator, photos, geo)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
		ON CONFLICT (client_uuid) DO NOTHING
		RETURNING id`,
		a.BatchID, a.ClientUUID, a.Kind, a.HappenedAt,
		a.InputID, a.Dose, a.DoseUnit, a.Operator, a.Photos, a.Geo).Scan(&activityID)
	if err == sql.ErrNoRows {
		activityInserted = false
		if err = tx.QueryRow(`SELECT id FROM activity WHERE client_uuid = $1`, clientUUID).Scan(&activityID); err != nil {
			return 0, false, false, err
		}
	} else if err != nil {
		return 0, false, false, err
	} else {
		activityInserted = true
	}

	var ledgerID int64
	err = tx.QueryRow(`INSERT INTO device_upload (device_id, seq, client_uuid, activity_id, status)
		VALUES ($1, $2, $3, $4, 'accepted')
		ON CONFLICT (device_id, seq) DO NOTHING
		RETURNING id`, deviceID, seq, clientUUID, activityID).Scan(&ledgerID)
	if err == sql.ErrNoRows {
		// Lost a race on (device_id, seq): roll back so the existing ledger
		// entry stays the single source of truth.
		return 0, false, false, nil
	}
	if err != nil {
		return 0, false, false, err
	}

	if err := tx.Commit(); err != nil {
		return 0, false, false, err
	}
	return activityID, activityInserted, true, nil
}

// ListSeqs returns all recorded seqs of a device, ascending.
func (r *DeviceRepo) ListSeqs(deviceID int64) ([]int64, error) {
	var seqs []int64
	query := `SELECT seq FROM device_upload WHERE device_id = $1 ORDER BY seq ASC`
	if err := r.db.Select(&seqs, query, deviceID); err != nil {
		return nil, err
	}
	return seqs, nil
}

// ListUploadsUpTo returns ledger entries with seq <= maxSeq, ascending.
func (r *DeviceRepo) ListUploadsUpTo(deviceID, maxSeq int64) ([]model.DeviceUpload, error) {
	var uploads []model.DeviceUpload
	query := `SELECT id, device_id, seq, client_uuid, activity_id, status, reason, received_at
	          FROM device_upload WHERE device_id = $1 AND seq <= $2 ORDER BY seq ASC`
	if err := r.db.Select(&uploads, query, deviceID, maxSeq); err != nil {
		return nil, err
	}
	return uploads, nil
}

// CountAbove counts ledger entries with seq > maxSeq (records beyond the device's claim).
func (r *DeviceRepo) CountAbove(deviceID, maxSeq int64) (int, error) {
	var n int
	query := `SELECT COUNT(*) FROM device_upload WHERE device_id = $1 AND seq > $2`
	if err := r.db.Get(&n, query, deviceID, maxSeq); err != nil {
		return 0, err
	}
	return n, nil
}

// Stats returns total ledger entries and the last received time.
func (r *DeviceRepo) Stats(deviceID int64) (total int, lastReceivedAt *time.Time, err error) {
	var row struct {
		Total          int        `db:"total"`
		LastReceivedAt *time.Time `db:"last_received_at"`
	}
	query := `SELECT COUNT(*) AS total, MAX(received_at) AS last_received_at
	          FROM device_upload WHERE device_id = $1`
	if err := r.db.Get(&row, query, deviceID); err != nil {
		return 0, nil, err
	}
	return row.Total, row.LastReceivedAt, nil
}
