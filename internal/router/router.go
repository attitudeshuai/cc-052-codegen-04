package router

import (
	"cc-052/internal/handler"
	"cc-052/internal/middleware"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/go-redis/redis/v8"
)

func Setup(
	farmH *handler.FarmHandler,
	plotH *handler.PlotHandler,
	batchH *handler.BatchHandler,
	activityH *handler.ActivityHandler,
	inspectionH *handler.InspectionHandler,
	traceCodeH *handler.TraceCodeHandler,
	deviceH *handler.DeviceHandler,
	healthH *handler.HealthHandler,
	rdb *redis.Client,
) *gin.Engine {
	r := gin.Default()

	// Health check
	r.GET("/healthz", healthH.Health)

	// API v1
	v1 := r.Group("/api/v1")
	{
		// Farms
		v1.POST("/farms", farmH.Create)
		v1.GET("/farms", farmH.List)
		v1.GET("/farms/:id", farmH.GetByID)

		// Plots
		v1.POST("/plots", plotH.Create)
		v1.GET("/plots/:id", plotH.GetByID)
		v1.GET("/plots", plotH.ListByFarm)

		// Batches
		v1.POST("/batches", batchH.Create)
		v1.GET("/batches/:id", batchH.GetByID)

		// Activities
		v1.POST("/batches/:id/activities", activityH.Create)
		v1.POST("/batches/:id/activities/batch", activityH.BatchCreate)
		v1.GET("/batches/:id/activities", activityH.ListByBatch)

		// Inspections
		v1.POST("/batches/:id/inspection", inspectionH.Create)

		// Trace codes
		v1.POST("/batches/:id/codes", traceCodeH.Generate)

		// 采集设备登记、交接、上报同步与进度核对
		v1.POST("/devices", deviceH.Register)
		v1.GET("/devices", deviceH.List)
		v1.GET("/devices/:id", deviceH.Get)
		v1.POST("/devices/:id/handover", deviceH.Handover)
		v1.GET("/devices/:id/handovers", deviceH.Handovers)
		v1.GET("/devices/:id/progress", deviceH.Progress)
		v1.GET("/devices/:id/records", deviceH.Records)
		v1.POST("/device-sync/:serial", deviceH.Sync)
	}

	// Public trace endpoints with rate limiting
	traceGroup := r.Group("/api/v1/trace")
	traceGroup.Use(middleware.RateLimit(rdb, 30, time.Minute))
	{
		traceGroup.GET("/:code", traceCodeH.Trace)
		traceGroup.GET("/:code/validate", traceCodeH.Validate)
	}

	return r
}
