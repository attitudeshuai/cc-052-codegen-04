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

// POST /api/v1/devices
func (h *DeviceHandler) Register(c *gin.Context) {
	var req model.CreateDeviceRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	d, err := h.svc.Register(&req)
	if err != nil {
		if errors.Is(err, service.ErrSerialDuplicate) {
			response.Conflict(c, err.Error())
			return
		}
		response.InternalError(c, err.Error())
		return
	}
	response.Created(c, d)
}

// GET /api/v1/devices
func (h *DeviceHandler) List(c *gin.Context) {
	devices, err := h.svc.List()
	if err != nil {
		response.InternalError(c, err.Error())
		return
	}
	response.Success(c, devices)
}

// GET /api/v1/devices/:id
func (h *DeviceHandler) Get(c *gin.Context) {
	id, ok := parseDeviceID(c)
	if !ok {
		return
	}
	d, err := h.svc.Get(id)
	if err != nil {
		response.NotFound(c, "设备未登记")
		return
	}
	response.Success(c, d)
}

// POST /api/v1/devices/:id/handover
func (h *DeviceHandler) Handover(c *gin.Context) {
	id, ok := parseDeviceID(c)
	if !ok {
		return
	}
	var req model.HandoverRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	hRecord, err := h.svc.Handover(id, &req)
	if err != nil {
		if errors.Is(err, service.ErrDeviceNotFound) {
			response.NotFound(c, err.Error())
			return
		}
		response.InternalError(c, err.Error())
		return
	}
	response.Created(c, hRecord)
}

// GET /api/v1/devices/:id/handovers
func (h *DeviceHandler) Handovers(c *gin.Context) {
	id, ok := parseDeviceID(c)
	if !ok {
		return
	}
	hs, err := h.svc.ListHandovers(id)
	if err != nil {
		response.NotFound(c, "设备未登记")
		return
	}
	response.Success(c, hs)
}

// POST /api/v1/devices/:serial/sync
func (h *DeviceHandler) Sync(c *gin.Context) {
	serial := c.Param("serial")
	var req model.SyncRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	resp, err := h.svc.Sync(serial, &req)
	if err != nil {
		switch {
		case errors.Is(err, service.ErrDeviceNotFound):
			response.NotFound(c, err.Error())
		case errors.Is(err, service.ErrDeviceRetired):
			response.Forbidden(c, err.Error())
		default:
			response.InternalError(c, err.Error())
		}
		return
	}
	response.Success(c, resp)
}

// GET /api/v1/devices/:id/progress
func (h *DeviceHandler) Progress(c *gin.Context) {
	id, ok := parseDeviceID(c)
	if !ok {
		return
	}
	resp, err := h.svc.Progress(id, c.Query("install_id"))
	if err != nil {
		response.NotFound(c, "设备未登记")
		return
	}
	response.Success(c, resp)
}

// GET /api/v1/devices/:id/records
func (h *DeviceHandler) Records(c *gin.Context) {
	id, ok := parseDeviceID(c)
	if !ok {
		return
	}
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "100"))
	offset, _ := strconv.Atoi(c.DefaultQuery("offset", "0"))
	rows, err := h.svc.Records(id, c.Query("install_id"), limit, offset)
	if err != nil {
		response.NotFound(c, "设备未登记")
		return
	}
	response.Success(c, rows)
}

func parseDeviceID(c *gin.Context) (int64, bool) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		response.BadRequest(c, "invalid device id")
		return 0, false
	}
	return id, true
}
