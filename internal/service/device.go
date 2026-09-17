package service

import (
	"cc-052/internal/model"
	"cc-052/internal/repository"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

var (
	ErrDeviceNotFound  = errors.New("device not found")
	ErrDeviceDisabled  = errors.New("device is disabled")
	ErrPlotNotFound    = errors.New("plot not found")
	ErrInvalidStatus   = errors.New("invalid device status")
	ErrEmptyOperator   = errors.New("operator must not be empty")
	ErrClaimOutOfRange = errors.New("max_seq out of range")
)

const (
	// maxClaimedSeq bounds reconcile claims to something a field device can
	// plausibly have produced; larger values are rejected as client bugs.
	maxClaimedSeq = 1000000
	// maxMissingListed caps the individually listed gaps in a reconcile
	// response; MissingCount always reports the true total.
	maxMissingListed = 5000
)

type DeviceService struct {
	repo      *repository.DeviceRepo
	plotRepo  *repository.PlotRepo
	batchRepo *repository.BatchRepo
}

func NewDeviceService(repo *repository.DeviceRepo, plotRepo *repository.PlotRepo, batchRepo *repository.BatchRepo) *DeviceService {
	return &DeviceService{repo: repo, plotRepo: plotRepo, batchRepo: batchRepo}
}

// Register creates a device, or returns the existing one when the
// device_code is already registered — a reinstalled app re-registers with
// the same hardware code and keeps its upload progress.
func (s *DeviceService) Register(req *model.RegisterDeviceRequest) (device *model.Device, alreadyRegistered bool, err error) {
	existing, err := s.repo.GetByCode(req.DeviceCode)
	if err != nil {
		return nil, false, err
	}
	if existing != nil {
		return existing, true, nil
	}
	if _, err := s.plotRepo.GetByID(req.PlotID); err != nil {
		if err == sql.ErrNoRows {
			return nil, false, ErrPlotNotFound
		}
		return nil, false, err
	}
	d := &model.Device{
		DeviceCode: req.DeviceCode,
		PlotID:     req.PlotID,
		Operator:   req.Operator,
		Status:     model.DeviceStatusActive,
	}
	if err := s.repo.Create(d); err != nil {
		return nil, false, err
	}
	return d, false, nil
}

func (s *DeviceService) GetByID(id int64) (*model.Device, error) {
	d, err := s.repo.GetByID(id)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, ErrDeviceNotFound
		}
		return nil, err
	}
	return d, nil
}

func (s *DeviceService) List(plotID *int64, status *string) ([]model.DeviceView, error) {
	return s.repo.List(plotID, status)
}

// Update changes registry fields (plot binding / operator handover /
// disable). Upload progress is keyed by device id and never reset here, so
// an operator change or re-binding cannot cause re-upload of sent ranges.
func (s *DeviceService) Update(id int64, req *model.UpdateDeviceRequest) (*model.Device, error) {
	d, err := s.GetByID(id)
	if err != nil {
		return nil, err
	}
	if req.PlotID != nil {
		if _, err := s.plotRepo.GetByID(*req.PlotID); err != nil {
			if err == sql.ErrNoRows {
				return nil, ErrPlotNotFound
			}
			return nil, err
		}
		d.PlotID = *req.PlotID
	}
	if req.Operator != nil {
		if *req.Operator == "" {
			return nil, ErrEmptyOperator
		}
		d.Operator = *req.Operator
	}
	if req.Status != nil {
		if *req.Status != model.DeviceStatusActive && *req.Status != model.DeviceStatusDisabled {
			return nil, ErrInvalidStatus
		}
		d.Status = *req.Status
	}
	if err := s.repo.Update(d); err != nil {
		return nil, err
	}
	return d, nil
}

// Sync stores a batch of device records one transaction per record: whatever
// was processed before a dropped connection stays in the ledger, and the
// next sync simply continues from the checkpoint.
func (s *DeviceService) Sync(deviceID int64, records []model.SyncRecordRequest) (*model.SyncResult, error) {
	device, err := s.GetByID(deviceID)
	if err != nil {
		return nil, err
	}
	if device.Status != model.DeviceStatusActive {
		return nil, ErrDeviceDisabled
	}

	result := &model.SyncResult{
		DeviceID: deviceID,
		Receipts: make([]model.SyncReceipt, 0, len(records)),
	}
	for _, rec := range records {
		receipt, err := s.syncOne(device, rec)
		if err != nil {
			return nil, err
		}
		switch receipt.Status {
		case model.ReceiptInserted:
			result.Inserted++
		case model.ReceiptDuplicate:
			result.Duplicated++
		case model.ReceiptRejected:
			result.Rejected++
		case model.ReceiptConflict:
			result.Conflicted++
		}
		result.Receipts = append(result.Receipts, receipt)
	}

	checkpoint, err := s.syncStatus(device)
	if err != nil {
		return nil, err
	}
	result.Checkpoint = checkpoint
	return result, nil
}

func (s *DeviceService) syncOne(d *model.Device, rec model.SyncRecordRequest) (model.SyncReceipt, error) {
	r := model.SyncReceipt{Seq: rec.Seq, ClientUUID: rec.ClientUUID}

	// First write wins: this seq already has a ledger entry.
	up, err := s.repo.GetUploadBySeq(d.ID, rec.Seq)
	if err != nil {
		return r, err
	}
	if up != nil {
		classifyExisting(&r, up)
		return r, nil
	}

	// Same client_uuid at another seq (e.g. device renumbered after a
	// reinstall): the record is already safe, say so instead of storing twice.
	upByUUID, err := s.repo.GetUploadByUUID(d.ID, rec.ClientUUID)
	if err != nil {
		return r, err
	}
	if upByUUID != nil {
		r.Status = model.ReceiptDuplicate
		r.ActivityID = upByUUID.ActivityID
		r.Reason = fmt.Sprintf("already recorded as seq %d", upByUUID.Seq)
		return r, nil
	}

	// Content validation; rejections are recorded in the ledger so
	// reconciliation does not report them as lost.
	reason, err := s.validateRecord(d, rec)
	if err != nil {
		return r, err
	}
	if reason != "" {
		if err := s.repo.RecordRejected(d.ID, rec.Seq, rec.ClientUUID, reason); err != nil {
			return r, err
		}
		r.Status = model.ReceiptRejected
		r.Reason = reason
		return r, nil
	}

	happenedAt, _ := parseHappenedAt(rec.HappenedAt) // validated above
	operator := rec.Operator
	if operator == "" {
		operator = d.Operator
	}
	a := &model.Activity{
		BatchID:    rec.BatchID,
		ClientUUID: rec.ClientUUID,
		Kind:       rec.Kind,
		HappenedAt: happenedAt,
		InputID:    rec.InputID,
		Dose:       rec.Dose,
		DoseUnit:   rec.DoseUnit,
		Operator:   operator,
		Photos:     rec.Photos,
		Geo:        rec.Geo,
	}
	activityID, inserted, ledgerWritten, err := s.repo.RecordAccepted(d.ID, rec.Seq, rec.ClientUUID, a)
	if err != nil {
		return r, err
	}
	if !ledgerWritten {
		// A concurrent sync recorded this seq first; re-read and classify.
		up, err := s.repo.GetUploadBySeq(d.ID, rec.Seq)
		if err != nil {
			return r, err
		}
		if up != nil {
			classifyExisting(&r, up)
			return r, nil
		}
		return r, fmt.Errorf("device_upload race for device %d seq %d", d.ID, rec.Seq)
	}

	r.ActivityID = &activityID
	if inserted {
		r.Status = model.ReceiptInserted
	} else {
		r.Status = model.ReceiptDuplicate
	}
	return r, nil
}

// classifyExisting sets the receipt from an existing ledger entry: same
// client_uuid echoes the stored outcome, a different one is a seq conflict
// (device state is out of alignment — it should re-read the checkpoint).
func classifyExisting(r *model.SyncReceipt, up *model.DeviceUpload) {
	if up.ClientUUID != r.ClientUUID {
		r.Status = model.ReceiptConflict
		r.Reason = fmt.Sprintf("seq %d already recorded with a different client_uuid; fetch sync-status and re-align", r.Seq)
		return
	}
	if up.Status == model.UploadAccepted {
		r.Status = model.ReceiptDuplicate
		r.ActivityID = up.ActivityID
	} else {
		r.Status = model.ReceiptRejected
		r.Reason = reasonString(up.Reason)
	}
}

// validateRecord returns a rejection reason, or "" when the record is
// storable. A non-nil error aborts the whole sync (transient db failure).
func (s *DeviceService) validateRecord(d *model.Device, rec model.SyncRecordRequest) (string, error) {
	happenedAt, err := parseHappenedAt(rec.HappenedAt)
	if err != nil {
		return "invalid happened_at", nil
	}
	if !validKind(rec.Kind) {
		return fmt.Sprintf("invalid kind %q", rec.Kind), nil
	}
	batch, err := s.batchRepo.GetByID(rec.BatchID)
	if err != nil {
		if err == sql.ErrNoRows {
			return fmt.Sprintf("batch %d not found", rec.BatchID), nil
		}
		return "", err
	}
	if batch.PlotID != d.PlotID {
		return fmt.Sprintf("batch %d does not belong to the device's bound plot %d", rec.BatchID, d.PlotID), nil
	}
	if happenedAt.Before(batch.SowingDate) {
		return "happened_at before sowing date", nil
	}
	if batch.HarvestDate != nil && happenedAt.After(*batch.HarvestDate) {
		return "happened_at after harvest date", nil
	}
	return "", nil
}

// SyncStatus is the checkpoint a device resumes from: fill the gaps first,
// then continue from next_seq.
func (s *DeviceService) SyncStatus(deviceID int64) (*model.SyncStatusResponse, error) {
	device, err := s.GetByID(deviceID)
	if err != nil {
		return nil, err
	}
	return s.syncStatus(device)
}

func (s *DeviceService) syncStatus(d *model.Device) (*model.SyncStatusResponse, error) {
	seqs, err := s.repo.ListSeqs(d.ID)
	if err != nil {
		return nil, err
	}
	total, lastReceivedAt, err := s.repo.Stats(d.ID)
	if err != nil {
		return nil, err
	}
	contiguous := maxContiguous(seqs)
	var maxSeq int64
	if len(seqs) > 0 {
		maxSeq = seqs[len(seqs)-1]
	}
	return &model.SyncStatusResponse{
		DeviceID:         d.ID,
		DeviceCode:       d.DeviceCode,
		NextSeq:          contiguous + 1,
		MaxContiguousSeq: contiguous,
		MaxSeq:           maxSeq,
		TotalRecords:     total,
		Gaps:             missingSeqs(seqs, maxSeq),
		LastReceivedAt:   lastReceivedAt,
	}, nil
}

// Reconcile compares the device's claim ("I sent seq 1..claimedMaxSeq") with
// the server-side ledger and lists the gaps one by one.
func (s *DeviceService) Reconcile(deviceID, claimedMaxSeq int64) (*model.ReconcileResponse, error) {
	if _, err := s.GetByID(deviceID); err != nil {
		return nil, err
	}
	if claimedMaxSeq < 1 || claimedMaxSeq > maxClaimedSeq {
		return nil, ErrClaimOutOfRange
	}
	uploads, err := s.repo.ListUploadsUpTo(deviceID, claimedMaxSeq)
	if err != nil {
		return nil, err
	}
	extra, err := s.repo.CountAbove(deviceID, claimedMaxSeq)
	if err != nil {
		return nil, err
	}

	seqs := make([]int64, 0, len(uploads))
	accepted := 0
	rejected := make([]model.RejectedSeq, 0)
	for _, u := range uploads {
		seqs = append(seqs, u.Seq)
		if u.Status == model.UploadAccepted {
			accepted++
		} else {
			rejected = append(rejected, model.RejectedSeq{Seq: u.Seq, Reason: reasonString(u.Reason)})
		}
	}

	missing := missingSeqs(seqs, claimedMaxSeq)
	missingCount := len(missing)
	truncated := false
	if len(missing) > maxMissingListed {
		missing = missing[:maxMissingListed]
		truncated = true
	}

	return &model.ReconcileResponse{
		DeviceID:         deviceID,
		ClaimedMaxSeq:    claimedMaxSeq,
		Received:         len(uploads),
		Accepted:         accepted,
		Rejected:         rejected,
		MissingCount:     missingCount,
		MissingSeqs:      missing,
		MissingTruncated: truncated,
		ExtraBeyondClaim: extra,
	}, nil
}

// maxContiguous returns the largest S such that every seq in 1..S is present.
func maxContiguous(seqs []int64) int64 {
	have := make(map[int64]struct{}, len(seqs))
	var max int64
	for _, s := range seqs {
		have[s] = struct{}{}
		if s > max {
			max = s
		}
	}
	n := int64(1)
	for n <= max {
		if _, ok := have[n]; !ok {
			break
		}
		n++
	}
	return n - 1
}

// missingSeqs returns the numbers in 1..upTo absent from seqs, ascending.
func missingSeqs(seqs []int64, upTo int64) []int64 {
	have := make(map[int64]struct{}, len(seqs))
	for _, s := range seqs {
		have[s] = struct{}{}
	}
	missing := make([]int64, 0)
	for n := int64(1); n <= upTo; n++ {
		if _, ok := have[n]; !ok {
			missing = append(missing, n)
		}
	}
	return missing
}

func parseHappenedAt(s string) (time.Time, error) {
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, nil
	}
	return time.Parse("2006-01-02", s)
}

func validKind(k model.ActivityKind) bool {
	switch k {
	case model.ActivityFertilize, model.ActivityPesticide, model.ActivityIrrigation, model.ActivityWeed:
		return true
	}
	return false
}

func reasonString(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}
