package api

import (
	"context"

	"github.com/gin-gonic/gin"
	"github.com/ppxb/miyabi/internal/sidecarsync"
)

type SidecarSyncManager interface {
	Config(context.Context) (sidecarsync.Config, error)
	Update(context.Context, sidecarsync.Update) (sidecarsync.Config, error)
	Sync(context.Context) error
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
		err := service.Sync(c.Request.Context())
		config, configErr := service.Config(c.Request.Context())
		if err == nil {
			err = configErr
		}
		respond(c, config, err)
	}
}
