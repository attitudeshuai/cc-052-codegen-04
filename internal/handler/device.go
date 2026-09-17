package handler

import (
	"cc-052/internal/model"
	"cc-052/internal/service"
	"cc-052/pkg/response"
	"errors"
	"strconv"

	"github.com/gin-gonic/gin"
)

type DeviceHandler struct {
	svc *service.DeviceService
}

func NewDeviceHandler(svc *service.DeviceService) *DeviceHandler {
	return &DeviceHandler{svc: svc}
}

func (h *DeviceHandler) Register(c *gin.Context) {
	var req model.RegisterDeviceRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	device, already, err := h.svc.Register(&req)
	if err != nil {
		deviceError(c, err)
		return
	}
	if already {
		// Same device_code re-registers (e.g. after app reinstall): return the
		// existing record so the device keeps its upload progress.
		response.Success(c, gin.H{"already_registered": true, "device": device})
		return
	}
	response.Created(c, gin.H{"already_registered": false, "device": device})
}

func (h *DeviceHandler) List(c *gin.Context) {
	var plotID *int64
	if v := c.Query("plot_id"); v != "" {
		id, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			response.BadRequest(c, "invalid plot_id")
			return
		}
		plotID = &id
	}
	var status *string
	if v := c.Query("status"); v != "" {
		status = &v
	}
	devices, err := h.svc.List(plotID, status)
	if err != nil {
		response.InternalError(c, err.Error())
		return
	}
	response.Success(c, devices)
}

func (h *DeviceHandler) GetByID(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		response.BadRequest(c, "invalid device id")
		return
	}
	device, err := h.svc.GetByID(id)
	if err != nil {
		deviceError(c, err)
		return
	}
	response.Success(c, device)
}

func (h *DeviceHandler) Update(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		response.BadRequest(c, "invalid device id")
		return
	}
	var req model.UpdateDeviceRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	device, err := h.svc.Update(id, &req)
	if err != nil {
		deviceError(c, err)
		return
	}
	response.Success(c, device)
}

func (h *DeviceHandler) Sync(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		response.BadRequest(c, "invalid device id")
		return
	}
	var req model.SyncRecordsRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	result, err := h.svc.Sync(id, req.Records)
	if err != nil {
		deviceError(c, err)
		return
	}
	response.Success(c, result)
}

func (h *DeviceHandler) SyncStatus(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		response.BadRequest(c, "invalid device id")
		return
	}
	status, err := h.svc.SyncStatus(id)
	if err != nil {
		deviceError(c, err)
		return
	}
	response.Success(c, status)
}

func (h *DeviceHandler) Reconcile(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		response.BadRequest(c, "invalid device id")
		return
	}
	maxSeq, err := strconv.ParseInt(c.Query("max_seq"), 10, 64)
	if err != nil {
		response.BadRequest(c, "max_seq is required and must be an integer")
		return
	}
	result, err := h.svc.Reconcile(id, maxSeq)
	if err != nil {
		deviceError(c, err)
		return
	}
	response.Success(c, result)
}

// deviceError maps service sentinel errors to HTTP statuses.
func deviceError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, service.ErrDeviceNotFound):
		response.NotFound(c, err.Error())
	case errors.Is(err, service.ErrDeviceDisabled):
		response.Forbidden(c, err.Error())
	case errors.Is(err, service.ErrPlotNotFound),
		errors.Is(err, service.ErrInvalidStatus),
		errors.Is(err, service.ErrEmptyOperator),
		errors.Is(err, service.ErrClaimOutOfRange):
		response.BadRequest(c, err.Error())
	default:
		response.InternalError(c, err.Error())
	}
}
