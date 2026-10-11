// Package bridge streams a running Android emulator to a browser and forwards input back.
package bridge

// proto/emulator_controller.proto is a copy of the Android SDK's emulator/lib/emulator_controller.proto.
//go:generate go run github.com/bufbuild/buf/cmd/buf@v1.72.0 generate
