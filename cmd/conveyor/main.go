package main

import (
	"log"
	"os"

	"github.com/conveyorci/conveyor/internal/runner"
)

func main() {
	log.Println("Starting Conveyor CI Runner...")

	configFile := ".conveyor.yml"
	if _, err := os.Stat(configFile); os.IsNotExist(err) {
		log.Fatalf("Config file not found: %s. Please create it in the current directory.", configFile)
	}

	if err := runner.Run(configFile); err != nil {
		log.Fatalf("FATAL: Pipeline execution failed: %v", err)
	}

	log.Println("Conveyor CI Runner finished.")
}
