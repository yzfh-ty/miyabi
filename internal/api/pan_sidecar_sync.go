package api

import (
	"context"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/ppxb/miyabi/internal/sidecarsync"
)

type SidecarSyncManager interface {
	Config(context.Context) (sidecarsync.Config, error)
	Update(context.Context, sidecarsync.Update) (sidecarsync.Config, error)
	Start(context.Context) (sidecarsync.Config, error)
}

func sidecarSyncConfigHandler(service SidecarSyncManager) gin.HandlerFunc {
	return func(c *gin.Context) {
		config, err := service.Config(c.Request.Context())
		respond(c, config, err)
	}
}

func sidecarSyncUpdateHandler(service SidecarSyncManager) gin.HandlerFunc {
	return func(c *gin.Context) {
		input, ok := bindJSON[sidecarsync.Update](c)
		if !ok {
			return
		}
		config, err := service.Update(c.Request.Context(), input)
		respond(c, config, err)
	}
}

func sidecarSyncNowHandler(service SidecarSyncManager) gin.HandlerFunc {
	return func(c *gin.Context) {
		config, err := service.Start(c.Request.Context())
		if err != nil {
			c.Error(err)
			return
		}
		c.JSON(http.StatusAccepted, config)
	}
}
