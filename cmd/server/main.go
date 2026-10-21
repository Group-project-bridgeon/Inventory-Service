package main

import (
	"github.com/Group-project-bridgeon/Inventory-Service/internal/app"
	"github.com/Group-project-bridgeon/Inventory-Service/internal/platform/logger"
	"os"
)

func main() {
	if err := app.Run(); err != nil {
		log := logger.New()
		logger.LogFailure(log, err)
		os.Exit(1)
	}
}
