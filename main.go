package main

import (
	"jobify/cmd/jobify"
)

func main() {
	jobify.SetupCommand().Execute()
}
